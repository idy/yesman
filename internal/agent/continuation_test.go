package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func noticesOf(got []string) []string {
	var out []string
	for _, s := range got {
		if strings.HasPrefix(s, noticeHeader) {
			out = append(out, s)
		}
	}
	return out
}

func quiet(t *testing.T, prompts func(string) []string, id string, want int) {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	if got := noticesOf(prompts(id)); len(got) != want {
		t.Fatalf("%d notices, want %d: %q", len(got), want, got)
	}
}

func addTasks(t *testing.T, store *yesmen.Store, tasks ...yesmen.TaskEdit) {
	t.Helper()
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: tasks}); err != nil {
		t.Fatal(err)
	}
}

// What the lead was told survives a restart: the app starting again does
// not start the lead to repeat it.
func TestContinuationDedupeOutlivesRestart(t *testing.T) {
	m, store := newTestManager(t, 0)
	prompts := promptsTo(m)
	addTasks(t, store, yesmen.TaskEdit{Title: str("Orphan")})
	lead := ID("alice", yesmen.LeadName)
	if _, _, err := m.ensure(lead); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice")
	waitNoticeListing(t, prompts, lead, "t1")
	c, _ := store.Continuation("alice")
	if r := c.Tasks["t1"]; !r.Delivered || r.Count != 1 || len(c.Attempts) != 1 {
		t.Fatalf("record after delivery: %+v", c)
	}
	m.StopAll()
	again := New(m.cfg)
	t.Cleanup(again.StopAll)
	after := promptsTo(again)
	again.Resume()
	quiet(t, after, lead, 0)
	if again.running(lead) != nil {
		t.Fatal("the app start woke the lead to repeat a delivered notice")
	}
}

// The budget is kept in the workspace: a restart does not reset it, and a
// lead over it gets no notice, only a local timer.
func TestContinuationBudgetOutlivesRestart(t *testing.T) {
	m, store := newTestManager(t, 0)
	addTasks(t, store, yesmen.TaskEdit{Title: str("Orphan")})
	y, _ := store.Get("alice")
	now := time.Now().UTC()
	if _, err := store.UpdateContinuation("alice", y.HiredAt, func(c *yesmen.Continuation) bool {
		for i := range noticeBudget {
			c.Attempts = append(c.Attempts, now.Add(-time.Duration(i)*time.Minute))
		}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	m.StopAll()
	again := New(m.cfg)
	t.Cleanup(again.StopAll)
	prompts := promptsTo(again)
	again.Resume()
	lead := ID("alice", yesmen.LeadName)
	quiet(t, prompts, lead, 0)
	if again.running(lead) != nil {
		t.Fatal("started the lead over budget")
	}
	again.mu.Lock()
	timer := again.continuations["alice"] != nil && again.continuations["alice"].timer != nil
	again.mu.Unlock()
	if !timer {
		t.Fatal("no local timer for when the budget allows")
	}
	if c, _ := store.Continuation("alice"); len(c.Attempts) != noticeBudget || len(c.Tasks) != 0 {
		t.Fatalf("over budget changed the record: %+v", c)
	}
}

// A notice pi refuses is recorded as attempted, never as delivered, tried
// again a bounded number of times, and then left.
func TestContinuationFailedDispatchIsBounded(t *testing.T) {
	prev := noticeRetries
	noticeRetries = []time.Duration{20 * time.Millisecond, 20 * time.Millisecond}
	t.Cleanup(func() { noticeRetries = prev })
	m, store := newTestManager(t, 0, "FAKE_PI_FAIL_PROMPTS=1")
	addTasks(t, store, yesmen.TaskEdit{Title: str("Orphan")})
	lead := ID("alice", yesmen.LeadName)
	if _, _, err := m.ensure(lead); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, _ := store.Continuation("alice"); c.Tasks["t1"].Count == noticesPerState {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	m.continueTasks("alice") // a later event: over the per-state bound
	c, _ := store.Continuation("alice")
	if r := c.Tasks["t1"]; r.Delivered || r.Count != noticesPerState || len(c.Attempts) != noticesPerState {
		t.Fatalf("record after refused notices: %+v", c)
	}
}

// A task whose owner keeps failing is told about at most noticesPerState
// times while its status, owner and wait stay the same.
func TestContinuationNoProgressIsBounded(t *testing.T) {
	m, store := newTestManager(t, 0)
	prompts := promptsTo(m)
	start := time.Now().UTC()
	if err := store.AddWorker("alice", yesmen.Worker{Name: "w1", Task: "synthetic", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	addTasks(t, store, yesmen.TaskEdit{Title: str("Flaky"), Status: str("doing"), Owner: str("w1")})
	lead := ID("alice", yesmen.LeadName)
	if _, _, err := m.ensure(lead); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if err := store.FailWorker("alice", "w1", start, start.Add(time.Duration(i+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, m, lead, Idle)
		time.Sleep(100 * time.Millisecond)
		m.continueTasks("alice")
		time.Sleep(100 * time.Millisecond)
	}
	quiet(t, prompts, lead, noticesPerState)
	got := noticesOf(prompts(lead))
	if !strings.Contains(got[0], "owner w1 failed") || !strings.Contains(got[len(got)-1], "will not tell you again") {
		t.Fatalf("notices: %q", got)
	}
	// Progress (a new owner) lets it be told again.
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Owner: str("")}}}); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice")
	quiet(t, prompts, lead, noticesPerState+1)
}

// The user closing a Needs you request lets the lead check the result; it
// does not unblock the task, and an open request keeps it quiet.
func TestContinuationNeedsYouCloseAsksToVerify(t *testing.T) {
	m, store := newTestManager(t, 0)
	prompts := promptsTo(m)
	addTasks(t, store, yesmen.TaskEdit{Title: str("Install the bot"), Wait: &yesmen.WaitEdit{Kind: yesmen.WaitUser}})
	h, _, err := store.AddHandoff("alice", yesmen.Handoff{Type: yesmen.HandoffInstall, Title: "Install the GitHub App", Task: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	lead := ID("alice", yesmen.LeadName)
	if _, _, err := m.ensure(lead); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice")
	quiet(t, prompts, lead, 0)
	if _, err := store.CloseHandoff("alice", h.ID, yesmen.HandoffDone, "user", ""); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice")
	waitNoticeListing(t, prompts, lead, "t1")
	if got := noticesOf(prompts(lead))[0]; !strings.Contains(got, h.ID+" done") || !strings.Contains(got, "does not unblock it") {
		t.Fatalf("notice: %s", got)
	}
	if b, _ := store.Board("alice"); b.Tasks[0].Status != yesmen.TaskBlocked || b.Tasks[0].Wait == nil {
		t.Fatalf("the task was unblocked: %+v", b.Tasks[0])
	}
}

// A task the user paused, or waiting outside yesman, is never told about;
// a worker cannot lift the pause.
func TestContinuationPausedTaskStaysPaused(t *testing.T) {
	m, store := newTestManager(t, 0)
	prompts := promptsTo(m)
	addTasks(t, store,
		yesmen.TaskEdit{Title: str("History fix"), Owner: str("w1"), Wait: &yesmen.WaitEdit{Kind: yesmen.WaitPaused, Reason: "The user said: do not fix it yet"}},
		yesmen.TaskEdit{Title: str("CI"), Wait: &yesmen.WaitEdit{Kind: yesmen.WaitExternal, Reason: "CI on the PR"}},
	)
	if _, err := store.ChangeBoardBy("alice", yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: str("doing")}}}, false); !errors.Is(err, yesmen.ErrPauseByLead) {
		t.Fatalf("a worker lifted the pause: %v", err)
	}
	lead := ID("alice", yesmen.LeadName)
	if _, _, err := m.ensure(lead); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice")
	m.Resume()
	quiet(t, prompts, lead, 0)
	if b, _ := store.Board("alice"); !b.Tasks[0].Paused() {
		t.Fatalf("the pause changed: %+v", b.Tasks[0])
	}
}

// A lead the user stopped is not woken, after a restart either, until the
// user writes to it.
func TestContinuationLeadPausedByUser(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_SLOW_RUNS=1")
	addTasks(t, store, yesmen.TaskEdit{Title: str("Orphan")})
	lead := ID("alice", yesmen.LeadName)
	if _, _, err := m.ensure(lead); err != nil {
		t.Fatal(err)
	}
	m.StopByUser(lead)
	m.continueTasks("alice")
	m.StopAll()
	again := New(m.cfg)
	t.Cleanup(again.StopAll)
	prompts := promptsTo(again)
	again.Resume()
	quiet(t, prompts, lead, 0)
	if again.running(lead) != nil {
		t.Fatal("woke a lead the user stopped")
	}
	if err := again.Prompt(context.Background(), lead, "go on"); err != nil {
		t.Fatal(err)
	}
	waitNoticeListing(t, prompts, lead, "t1")
	if c, _ := store.Continuation("alice"); !c.LeadPausedAt.IsZero() {
		t.Fatalf("the user's message did not lift the pause: %+v", c)
	}
}

// A worker hired again under the same name is a new owner: the old
// generation's pause does not reach it, and its finished run is told about.
func TestContinuationRehiredOwnerIsNew(t *testing.T) {
	m, store := newTestManager(t, 0)
	prompts := promptsTo(m)
	old := time.Now().UTC().Add(-time.Hour)
	if err := store.AddWorker("alice", yesmen.Worker{Name: "w1", Task: "first", StartedAt: old, FinishedAt: old.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	addTasks(t, store, yesmen.TaskEdit{Title: str("Review"), Status: str("doing"), Owner: str("w1")})
	lead := ID("alice", yesmen.LeadName)
	if _, _, err := m.ensure(lead); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice")
	waitNoticeListing(t, prompts, lead, "t1")
	waitStatus(t, m, lead, Idle)
	time.Sleep(200 * time.Millisecond)
	if err := store.RemoveWorker("alice", "w1"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.AddWorker("alice", yesmen.Worker{Name: "w1", Task: "second", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.PauseWorker("alice", "w1", old, now); err != nil { // a late stop of the old one
		t.Fatal(err)
	}
	if err := store.FinishWorker("alice", "w1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice")
	quiet(t, prompts, lead, 2)
}

// The release goal stays open when its status sub-task is done, and is
// told about while another line of work runs.
func TestContinuationReleaseGoalOutlivesStatusTask(t *testing.T) {
	m, store := newTestManager(t, 0)
	prompts := promptsTo(m)
	at := time.Now().UTC()
	for _, w := range []string{"publisher", "status"} {
		if err := store.AddWorker("alice", yesmen.Worker{Name: w, Task: "synthetic", StartedAt: at, FinishedAt: at.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	addTasks(t, store,
		yesmen.TaskEdit{Title: str("Publish 0.1.38"), Status: str("doing"), Owner: str("publisher")},
		yesmen.TaskEdit{Title: str("Check whether 0.1.38 is published"), Status: str("done"), Owner: str("status")},
		yesmen.TaskEdit{Title: str("Receipts"), Status: str("doing"), Owner: str("busy")},
	)
	if _, err := m.StartWorker(context.Background(), "alice", "busy", "Receipts", "test-run:retry"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, ID("alice", "busy"), Working)
	lead := ID("alice", yesmen.LeadName)
	if _, _, err := m.ensure(lead); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice")
	waitNoticeListing(t, prompts, lead, "t1")
	got := noticesOf(prompts(lead))[0]
	if strings.Contains(got, "- t2 [") || strings.Contains(got, "- t3 [") || !strings.Contains(got, "a report does not finish the task") {
		t.Fatalf("notice: %s", got)
	}
}

// A worker's process failing is recorded for its generation, outlives the
// app, and the lead hears its task needs a next step.
func TestContinuationFailedOwner(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_CRASH_WORKERS=1")
	prompts := promptsTo(m)
	addTasks(t, store, yesmen.TaskEdit{Title: str("Crashy"), Status: str("doing"), Owner: str("w1")})
	if _, err := m.StartWorker(context.Background(), "alice", "w1", "Crashy", "a task"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, ID("alice", "w1"), Failed)
	lead := ID("alice", yesmen.LeadName)
	waitPrompt(t, prompts, lead, `[yesman] Worker "w1" stopped unexpectedly`)
	waitNoticeListing(t, prompts, lead, "t1")
	if !strings.Contains(noticesOf(prompts(lead))[0], "owner w1 failed") {
		t.Fatalf("notice: %q", prompts(lead))
	}
	ws, _ := store.Workers("alice")
	if ws[0].FailedAt.IsZero() {
		t.Fatalf("failure not recorded: %+v", ws[0])
	}
}
