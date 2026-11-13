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
	for _, want := range []string{"Allow rm -rf build?", "- Allow\n- Block\n", "after 1m0s", "yesman_worker_answer"} {
		if !strings.Contains(question, want) {
			t.Errorf("the question lacks %q:\n%s", want, question)
		}
	}
	waitStatus(t, m, worker, Waiting)

	yes := true
	for _, bad := range []DialogReply{{Value: str("Maybe")}, {Confirmed: &yes}, {}} {
		if err := m.AnswerDialog(worker, bad); err == nil {
			t.Errorf("reply %+v was accepted", bad)
		}
	}
	if err := m.AnswerDialog(worker, DialogReply{Value: str(" block ")}); err != nil {
		t.Fatal(err)
	}
	if err := m.AnswerDialog(worker, DialogReply{Value: str("Allow")}); err == nil {
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
	if err := m.AnswerDialog(lead, DialogReply{Value: str("x")}); err == nil {
		t.Error("answered an agent that asked nothing")
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
