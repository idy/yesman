package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// eventsOf records the events of the given type each agent's fake pi wrote.
func eventsOf(m *Manager, kind string) func(id string) []json.RawMessage {
	var mu sync.Mutex
	got := map[string][]json.RawMessage{}
	prev := m.cfg.OnEvent
	m.cfg.OnEvent = func(e Event) {
		prev(e)
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(e.Data, &head) == nil && head.Type == kind {
			mu.Lock()
			got[e.Agent] = append(got[e.Agent], e.Data)
			mu.Unlock()
		}
	}
	return func(id string) []json.RawMessage {
		mu.Lock()
		defer mu.Unlock()
		return append([]json.RawMessage(nil), got[id]...)
	}
}

func str(s string) *string { return &s }

// A worker's question goes to its lead, which answers it.
func TestWorkerQuestionGoesToLead(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_ASK_WORKERS=1")
	prompts := promptsTo(m)
	answers := eventsOf(m, "test_ui_answer")
	ctx := context.Background()
	if _, err := m.StartWorker(ctx, "alice", "w1", "Clean the build", "a task"); err != nil {
		t.Fatal(err)
	}
	lead, worker := ID("alice", yesmen.LeadName), ID("alice", "w1")
	waitPrompt(t, prompts, lead, `[yesman] Worker "w1" asks:`)
	question := prompts(lead)[0]
	for _, want := range []string{"Allow rm -rf build?", "- Allow\n- Block\n", "after 1m0s", "yesman_worker_answer", `requestId "q1"`} {
		if !strings.Contains(question, want) {
			t.Errorf("the question lacks %q:\n%s", want, question)
		}
	}
	waitStatus(t, m, worker, Waiting)

	yes := true
	for _, bad := range []DialogReply{{RequestID: "q1", Value: str("Maybe")}, {RequestID: "q1", Confirmed: &yes}, {RequestID: "q1"}, {Value: str("Block")}} {
		if err := m.AnswerDialog(worker, bad); err == nil {
			t.Errorf("reply %+v was accepted", bad)
		}
	}
	if err := m.AnswerDialog(worker, DialogReply{RequestID: "q1", Value: str(" block ")}); err != nil {
		t.Fatal(err)
	}
	if err := m.AnswerDialog(worker, DialogReply{RequestID: "q1", Value: str("Allow")}); err == nil {
		t.Error("a question was answered twice")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(answers(worker)) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	var got struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	if a := answers(worker); len(a) != 1 || json.Unmarshal(a[0], &got) != nil || got.ID != "q1" || got.Value != "Block" {
		t.Fatalf("the worker received %s", answers(worker))
	}
	if err := m.AnswerDialog(lead, DialogReply{RequestID: "q1", Value: str("x")}); err == nil {
		t.Error("answered an agent that asked nothing")
	}
}

// An answer to a question the user already handled must never approve a
// newer dialog, even when both questions accept the same answer type.
func TestWorkerAnswerCannotConsumeANewerQuestion(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_ASK_WORKERS=1")
	if _, err := m.StartWorker(context.Background(), "alice", "w1", "Build", "a task"); err != nil {
		t.Fatal(err)
	}
	id := ID("alice", "w1")
	waitStatus(t, m, id, Waiting)
	m.mu.Lock()
	a, p := m.agents[id], m.agents[id].proc
	m.mu.Unlock()
	m.record(a, p, "extension_ui_request", json.RawMessage(`{"type":"extension_ui_request","id":"q2","method":"confirm","title":"Approve a different operation?"}`))
	yes := true
	if err := m.AnswerDialog(id, DialogReply{RequestID: "q1", Confirmed: &yes}); err == nil {
		t.Fatal("a stale lead answer approved a newer question")
	}
	if err := m.Answer(id, "q1", map[string]any{"confirmed": true}); err == nil {
		t.Fatal("a stale UI answer approved a newer question")
	}
	info, _ := m.Get(id)
	var current dialog
	_ = json.Unmarshal(info.Dialog, &current)
	if info.Status != Waiting || current.ID != "q2" {
		t.Fatalf("stale answer consumed the current dialog: %+v", info)
	}
	if err := m.AnswerDialog(id, DialogReply{RequestID: "q2", Cancelled: true}); err != nil {
		t.Fatal(err)
	}
}

func TestDialogReply(t *testing.T) {
	no := false
	for _, c := range []struct {
		d    dialog
		r    DialogReply
		want string
	}{
		{dialog{Method: "confirm"}, DialogReply{Confirmed: &no}, `{"confirmed":false}`},
		{dialog{Method: "input"}, DialogReply{Value: str("v2")}, `{"value":"v2"}`},
		{dialog{Method: "editor"}, DialogReply{Value: str("")}, `{"value":""}`},
		{dialog{Method: "select", Options: []string{"A"}}, DialogReply{Cancelled: true}, `{"cancelled":true}`},
	} {
		got, err := c.d.reply(c.r)
		data, _ := json.Marshal(got)
		if err != nil || string(data) != c.want {
			t.Errorf("%s reply %+v = %s, %v; want %s", c.d.Method, c.r, data, err, c.want)
		}
	}
	if _, err := (dialog{Method: "input"}).reply(DialogReply{}); err == nil {
		t.Error("an empty reply to a text question was accepted")
	}
}

const reminder = "[yesman] No worker is running"

func countPrefix(list []string, prefix string) int {
	n := 0
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

func waitCount(t *testing.T, prompts func(string) []string, id string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(prompts(id)) < n {
		time.Sleep(10 * time.Millisecond)
	}
	// Then make sure no more arrive.
	time.Sleep(300 * time.Millisecond)
	if got := prompts(id); len(got) != n {
		t.Fatalf("%s received %d messages, want %d: %q", id, len(got), n, got)
	}
}

// A lead that ends a run with tasks open and nothing running is reminded
// once, again after someone writes to it, and not at all once the board
// has nothing left to do.
func TestFollowThroughRemindsOnce(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_SLOW_RUNS=1")
	prompts := promptsTo(m)
	s := func(v string) *string { return &v }
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{
		{Title: s("Windows build")},
		{Title: s("Lint"), Status: s("done")},
		{Title: s("Migrate the database"), Status: s("blocked"), Note: s("Which schema?")},
	}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lead := ID("alice", yesmen.LeadName)
	if err := m.Prompt(ctx, lead, "hi"); err != nil {
		t.Fatal(err)
	}
	waitCount(t, prompts, lead, 2)
	got := prompts(lead)[1]
	if !strings.HasPrefix(got, reminder) || !strings.Contains(got, "- t1 [todo] Windows build\n") || strings.Contains(got, "Lint") || strings.Contains(got, "Migrate") {
		t.Errorf("reminder:\n%s", got)
	}

	if err := m.Prompt(ctx, lead, "again"); err != nil {
		t.Fatal(err)
	}
	waitCount(t, prompts, lead, 4)
	if n := countPrefix(prompts(lead), reminder); n != 2 {
		t.Errorf("%d reminders: %q", n, prompts(lead))
	}

	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: s("done")}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Prompt(ctx, lead, "done?"); err != nil {
		t.Fatal(err)
	}
	waitCount(t, prompts, lead, 5)
}

// While a worker works, the lead is not reminded: the worker's report
// starts its next run.
func TestNoReminderWhileAWorkerWorks(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_SLOW_RUNS=1", "FAKE_PI_ASK_WORKERS=1")
	prompts := promptsTo(m)
	s := func(v string) *string { return &v }
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: s("Clean the build"), Status: s("doing"), Owner: s("w1")}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorker(context.Background(), "alice", "w1", "", "a task"); err != nil {
		t.Fatal(err)
	}
	lead := ID("alice", yesmen.LeadName)
	// The worker's question starts a run of the lead, which ends while the
	// worker waits for the answer.
	waitCount(t, prompts, lead, 1)
	if n := countPrefix(prompts(lead), reminder); n != 0 {
		t.Errorf("reminded while a worker waits: %q", prompts(lead))
	}
}

// A worker has settled, but its report is deliberately held by fake pi.
// Running followThrough synchronously must not race the report goroutine.
func TestNoReminderWhileAReportIsPending(t *testing.T) {
	for _, afterReport := range []string{yesmen.TaskTodo, yesmen.TaskDone} {
		t.Run(afterReport, func(t *testing.T) {
			m, store := newTestManager(t, 0, "FAKE_PI_HOLD_REPORTS=1")
			prompts := promptsTo(m)
			reportWaiting := make(chan struct{}, 1)
			prev := m.cfg.OnEvent
			m.cfg.OnEvent = func(e Event) {
				prev(e)
				var record struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(e.Data, &record) == nil && record.Type == "test_report_waiting" {
					reportWaiting <- struct{}{}
				}
			}
			board, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: str("Collect the result"), Owner: str("w1")}}})
			if err != nil {
				t.Fatal(err)
			}
			id := ID("alice", yesmen.LeadName)
			lead, p, err := m.ensure(id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.StartWorker(context.Background(), "alice", "w1", "Collect the result", "a task"); err != nil {
				t.Fatal(err)
			}
			worker := ID("alice", "w1")
			wp := m.running(worker)
			// Release even if an assertion fails, before StopAll runs.
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_, _ = wp.call(ctx, map[string]any{"type": "test_release_reports"})
				_, _ = p.call(ctx, map[string]any{"type": "test_release_prompts"})
			}()
			select {
			case <-reportWaiting:
			case <-time.After(10 * time.Second):
				t.Fatal("the worker never tried to read its report")
			}
			m.mu.Lock()
			w := m.agents[worker]
			status, pending := w.info.Status, w.reports
			m.mu.Unlock()
			if status != Idle || pending != 1 {
				t.Fatalf("settled worker: status %s, pending reports %d", status, pending)
			}
			m.followThrough(lead, p)
			if got := prompts(id); len(got) != 0 {
				t.Fatalf("reminded before the report was read: %q", got)
			}
			if m.SleepIfIdle(worker, time.Now().Add(time.Hour)) {
				t.Fatal("put a worker with a pending report to sleep")
			}
			if _, err := wp.call(context.Background(), map[string]any{"type": "test_release_reports"}); err != nil {
				t.Fatal(err)
			}
			waitPrompt(t, prompts, id, `[yesman] Worker "w1" finished a run`)
			// The text has been fetched, but its delivery is still held by the lead.
			m.mu.Lock()
			pending = w.reports
			m.mu.Unlock()
			if pending != 1 {
				t.Fatalf("released the report reservation before delivery: %d", pending)
			}
			m.followThrough(lead, p)
			if n := countPrefix(prompts(id), reminder); n != 0 {
				t.Fatalf("reminded before report delivery finished: %q", prompts(id))
			}
			if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: board.Tasks[0].ID, Status: str(afterReport)}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := p.call(context.Background(), map[string]any{"type": "test_release_prompts"}); err != nil {
				t.Fatal(err)
			}
			waitFinished(t, m, worker, time.Time{})
			if afterReport == yesmen.TaskTodo {
				waitPrompt(t, prompts, id, reminder)
			} else if n := countPrefix(prompts(id), reminder); n != 0 {
				t.Fatalf("reminded about finished work: %q", prompts(id))
			}
		})
	}
}

func TestFollowThroughLeavesStoppedWorkersAsleep(t *testing.T) {
	for _, c := range []struct {
		name     string
		finished bool
		other    bool
	}{
		{name: "only paused work"},
		{name: "other work continues", other: true},
		{name: "normally completed owner", finished: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, store := newTestManager(t, 0)
			prompts := promptsTo(m)
			if err := store.AddWorker("alice", yesmen.Worker{Name: "w1", Title: "Paused task", Task: "a task", StartedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			worker := ID("alice", "w1")
			if _, _, err := m.ensure(worker); err != nil {
				t.Fatal(err)
			}
			if c.finished {
				if err := store.FinishWorker("alice", "w1", time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			m.Stop(worker)
			tasks := []yesmen.TaskEdit{{Title: str("Paused task"), Status: str("doing"), Owner: str("w1")}}
			if c.other {
				tasks = append(tasks, yesmen.TaskEdit{Title: str("Available task")})
			}
			if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: tasks}); err != nil {
				t.Fatal(err)
			}
			id := ID("alice", yesmen.LeadName)
			lead, p, err := m.ensure(id)
			if err != nil {
				t.Fatal(err)
			}
			m.followThrough(lead, p)
			got := prompts(id)
			if !c.other && !c.finished {
				if len(got) != 0 {
					t.Fatalf("reminded to restart deliberately stopped work: %q", got)
				}
			} else {
				if len(got) != 1 || !strings.HasPrefix(got[0], reminder) {
					t.Fatalf("expected one reminder: %q", got)
				}
				if c.other && (strings.Contains(got[0], "Paused task") || !strings.Contains(got[0], "Available task")) {
					t.Fatalf("reminder included paused work or lost available work: %s", got[0])
				}
				if c.finished && !strings.Contains(got[0], "finished and closed") {
					t.Fatalf("normal completion was treated as a deliberate stop: %s", got[0])
				}
				if !strings.Contains(got[0], "Leave workers deliberately put to sleep by the user asleep") {
					t.Fatalf("reminder does not preserve deliberate sleep: %s", got[0])
				}
			}
			if i, _ := m.Get(worker); i.Status != Stopped || m.running(worker) != nil {
				t.Fatalf("the stopped worker was restarted: %+v", i)
			}
		})
	}
}

// Simulate a state change after followThrough read its board snapshot.
// The final reservation must recheck it instead of queuing a stale reminder.
func TestReminderRechecksAfterBoardRead(t *testing.T) {
	for _, state := range []string{"queued message", "working lead", "pending report", "stopped lead"} {
		t.Run(state, func(t *testing.T) {
			m, _ := newTestManager(t, 0)
			prompts := promptsTo(m)
			id := ID("alice", yesmen.LeadName)
			lead, p, err := m.ensure(id)
			if err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			inbox := lead.inbox
			m.mu.Unlock()
			switch state {
			case "queued message":
				m.unnudge(id)
			case "working lead":
				m.setStatus(lead, p, Working, "")
			case "pending report":
				m.mu.Lock()
				m.agents[ID("alice", "w1")] = &agent{info: Info{Yesman: "alice", Name: "w1", Role: RoleWorker, Status: Idle}, reports: 1}
				m.mu.Unlock()
			case "stopped lead":
				m.Stop(id)
			}
			board := yesmen.Board{Tasks: []yesmen.Task{{ID: "t1", Title: "Stale task", Status: yesmen.TaskTodo}}}
			m.remindOfBoard(lead, p, inbox, board, nil)
			if got := prompts(id); len(got) != 0 {
				t.Fatalf("dispatched stale reminder after %s: %q", state, got)
			}
		})
	}
}
