package agent

// The six counterexamples of the t152 audit, as what the user expects of
// task continuation. They use only the adapter in
// continuation_adapter_test.go, so the same assertions run against v0.1.37
// (b0d5e6e) with an adapter for its API, where each of them fails.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// noticesListing returns the lead's continuation notices listing a task.
func noticesListing(got []string, task string) int {
	n := 0
	for _, s := range got {
		if isNotice(s) && strings.Contains(s, "- "+task+" [") {
			n++
		}
	}
	return n
}

func waitNoticeListing(t *testing.T, prompts func(string) []string, id, task string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if noticesListing(prompts(id), task) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no notice lists %s: %q", task, prompts(id))
}

// An unrelated worker working, waiting or starting does not hide a task
// nobody carries.
func TestExpectBusyWorkerDoesNotHideOrphan(t *testing.T) {
	for _, state := range []Status{Working, Waiting, Starting} {
		t.Run(string(state), func(t *testing.T) {
			m, store := newTestManager(t, 0)
			prompts := promptsTo(m)
			if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: str("Orphan task")}}}); err != nil {
				t.Fatal(err)
			}
			id := ID("alice", yesmen.LeadName)
			if _, _, err := m.ensure(id); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			m.agents["alice/unrelated"] = &agent{info: Info{ID: "alice/unrelated", Yesman: "alice", Name: "unrelated", Role: RoleWorker, Status: state}}
			m.mu.Unlock()
			leadSettled(t, m)
			waitNoticeListing(t, prompts, id, "t1")
		})
	}
}

// Task A's worker working does not hide task B, whose owner stopped.
func TestExpectTaskAWorkingDoesNotHideTaskB(t *testing.T) {
	m, store := newTestManager(t, 0)
	prompts := promptsTo(m)
	if err := store.AddWorker("alice", yesmen.Worker{Name: "orphan", Title: "Task B", Task: "synthetic", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{
		{Title: str("Task A"), Status: str("doing"), Owner: str("busy")},
		{Title: str("Task B"), Status: str("doing"), Owner: str("orphan")},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorker(context.Background(), "alice", "busy", "Task A", "test-run:retry"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, ID("alice", "busy"), Working)
	id := ID("alice", yesmen.LeadName)
	if _, _, err := m.ensure(id); err != nil {
		t.Fatal(err)
	}
	leadSettled(t, m)
	waitNoticeListing(t, prompts, id, "t2")
	if n := noticesListing(prompts(id), "t1"); n != 0 {
		t.Fatalf("listed task A, which its worker carries: %q", prompts(id))
	}
}

// A predecessor being done tells the lead that the task waiting for it can
// go on, and only that one.
func TestExpectDonePredecessorWakesItsDependent(t *testing.T) {
	m, store := newTestManager(t, 0)
	prompts := promptsTo(m)
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{
		{Title: str("Publish predecessor"), Status: str("doing"), Owner: str("publisher")},
		{Title: str("Continue after it"), Status: str("blocked")},
		{Title: str("Unrelated wait"), Status: str("blocked")},
		{Title: str("Still running"), Status: str("doing"), Owner: str("publisher")},
	}}); err != nil {
		t.Fatal(err)
	}
	blockOn(t, store, "t2", "t1")
	blockOn(t, store, "t3", "t4")
	if err := store.AddWorker("alice", yesmen.Worker{Name: "publisher", Task: "synthetic", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock() // the publisher works on t4
	m.agents["alice/publisher"] = &agent{info: Info{ID: "alice/publisher", Yesman: "alice", Name: "publisher", Role: RoleWorker, Status: Working, Since: workerSince(t, store, "publisher")}}
	m.mu.Unlock()
	id := ID("alice", yesmen.LeadName)
	if _, _, err := m.ensure(id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: str("done")}}}); err != nil {
		t.Fatal(err)
	}
	leadSettled(t, m)
	waitNoticeListing(t, prompts, id, "t2")
	if n := noticesListing(prompts(id), "t3"); n != 0 {
		t.Fatalf("woke a task waiting for an open one: %q", prompts(id))
	}
	if b, _ := store.Board("alice"); b.Tasks[1].Status != yesmen.TaskBlocked {
		t.Fatalf("yesman changed the dependent itself: %+v", b.Tasks[1])
	}
}

// A task added after a first notice is told about too.
func TestExpectNewTaskAfterANoticeIsTold(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_SLOW_RUNS=1")
	prompts := promptsTo(m)
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: str("First open task")}}}); err != nil {
		t.Fatal(err)
	}
	id := ID("alice", yesmen.LeadName)
	if _, _, err := m.ensure(id); err != nil {
		t.Fatal(err)
	}
	leadSettled(t, m)
	waitNoticeListing(t, prompts, id, "t1")
	waitStatus(t, m, id, Idle)
	time.Sleep(300 * time.Millisecond)
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: str("New orphan after the first notice")}}}); err != nil {
		t.Fatal(err)
	}
	leadSettled(t, m)
	waitNoticeListing(t, prompts, id, "t2")
}

// A worker whose run ended on a provider error (429) finished its run, not
// its task: the lead hears that the task needs a next step even while
// another worker waits for an answer.
func TestExpectModelErrorLeavesTaskToContinue(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_ASK_WORKERS=1")
	prompts := promptsTo(m)
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{
		{Title: str("Review exact source"), Status: str("doing"), Owner: str("w1")},
		{Title: str("Other line"), Status: str("doing"), Owner: str("w2")},
	}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := m.StartWorker(ctx, "alice", "w2", "Other", "a task"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, ID("alice", "w2"), Waiting)
	if _, err := m.StartWorker(ctx, "alice", "w1", "Review", "test-run:error"); err != nil {
		t.Fatal(err)
	}
	info := waitFinished(t, m, ID("alice", "w1"), time.Time{})
	id := ID("alice", yesmen.LeadName)
	waitNoticeListing(t, prompts, id, "t1")
	if n := noticesListing(prompts(id), "t2"); n != 0 {
		t.Fatalf("listed the task of a worker waiting for an answer: %q", prompts(id))
	}
	board, _ := store.Board("alice")
	if info.RunError == nil || board.Tasks[0].Status != yesmen.TaskDoing {
		t.Fatalf("worker=%+v board=%+v", info, board)
	}
}

// A worker the user stopped stays stopped, and its task untold, after the
// app restarts.
func TestExpectUserStopOutlivesRestart(t *testing.T) {
	m, store := newTestManager(t, 0)
	if err := store.AddWorker("alice", yesmen.Worker{Name: "w1", Title: "Paused task", Task: "synthetic", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	wid := ID("alice", "w1")
	if _, _, err := m.ensure(wid); err != nil {
		t.Fatal(err)
	}
	userStop(m, wid)
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: str("Paused task"), Status: str("doing"), Owner: str("w1")}}}); err != nil {
		t.Fatal(err)
	}
	m.StopAll()
	again := New(m.cfg)
	t.Cleanup(again.StopAll)
	prompts := promptsTo(again)
	appStarted(t, again)
	leadSettled(t, again)
	time.Sleep(300 * time.Millisecond)
	id := ID("alice", yesmen.LeadName)
	if n := noticesListing(prompts(id), "t1"); n != 0 {
		t.Fatalf("told the lead to resume what the user stopped: %q", prompts(id))
	}
	if again.running(wid) != nil {
		t.Fatal("the stopped worker was started")
	}
}

// Work a worker carried when the app quit is not forgotten: once the app
// starts again, the lead hears that it needs a next step, without the user
// writing first.
func TestExpectRestartRechecksOpenWork(t *testing.T) {
	m, store := newTestManager(t, 0)
	if err := store.AddWorker("alice", yesmen.Worker{Name: "w1", Title: "Interrupted", Task: "synthetic", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: str("Interrupted"), Status: str("doing"), Owner: str("w1")}}}); err != nil {
		t.Fatal(err)
	}
	m.StopAll()
	again := New(m.cfg)
	t.Cleanup(again.StopAll)
	prompts := promptsTo(again)
	appStarted(t, again)
	waitNoticeListing(t, prompts, ID("alice", yesmen.LeadName), "t1")
}
