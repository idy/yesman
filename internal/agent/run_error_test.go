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

// settledRuns counts each agent's settled runs, from its fake pi's events.
func settledRuns(m *Manager) func(id string) int {
	var mu sync.Mutex
	n := map[string]int{}
	m.cfg.OnEvent = func(e Event) {
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(e.Data, &head) == nil && head.Type == "agent_settled" {
			mu.Lock()
			n[e.Agent]++
			mu.Unlock()
		}
	}
	return func(id string) int {
		mu.Lock()
		defer mu.Unlock()
		return n[id]
	}
}

// RunError says the last run an agent finished ended on a model error, from
// pi's own record of its last assistant message, until a later run ends
// otherwise; it never comes from text, and a new incarnation starts clean.
func TestRunErrorFollowsTheLastFinishedRun(t *testing.T) {
	m, _ := newTestManager(t, 0)
	settled := settledRuns(m)
	ctx := context.Background()
	lead := ID("alice", yesmen.LeadName)
	run := func(text string) Info {
		t.Helper()
		before := settled(lead)
		if err := m.Prompt(ctx, lead, text); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for settled(lead) == before {
			if time.Now().After(deadline) {
				t.Fatalf("%q never settled", text)
			}
			time.Sleep(10 * time.Millisecond)
		}
		waitStatus(t, m, lead, Idle)
		i, _ := m.Get(lead)
		return i
	}

	i := run("test-run:error")
	if i.RunError == nil || !strings.Contains(i.RunError.Message, "usage limit has been reached (429") || i.RunError.Provider != "codex" || i.RunError.Model != "gpt-5" || i.RunError.At.IsZero() {
		t.Fatalf("after a run that ended on an error: %+v", i.RunError)
	}
	// A run that records no assistant message says nothing new; a message
	// whose text mentions an error is not one.
	if i := run("error: the usage limit has been reached (429)"); i.RunError == nil {
		t.Error("a run without an outcome cleared the error")
	}
	// A run the user stopped is no failure.
	if i := run("test-run:aborted"); i.RunError != nil {
		t.Errorf("a stopped run kept the error: %+v", i.RunError)
	}
	run("test-run:error")
	// Putting the agent to sleep does not recover it.
	m.Stop(lead)
	if i, _ := m.Get(lead); i.Status != Stopped || i.RunError == nil {
		t.Errorf("asleep: status %s, error %+v", i.Status, i.RunError)
	}
	// A run that ends on a reply recovers it.
	if i := run("test-run:stop"); i.RunError != nil {
		t.Errorf("a run that ended on a reply kept the error: %+v", i.RunError)
	}

	// A run the user stopped while pi waited to retry its error is no
	// failure: pi settles it aborted, with no message after the error.
	before := settled(lead)
	if err := m.Prompt(ctx, lead, "test-run:retry"); err != nil {
		t.Fatal(err)
	}
	waitActivity(t, m, lead, "Retrying after an error")
	if i, _ := m.Get(lead); i.Status != Working || i.RunError != nil {
		t.Errorf("while waiting to retry: status %s, error %+v", i.Status, i.RunError)
	}
	if err := m.Abort(ctx, lead); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); settled(lead) == before; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the stopped retry never settled")
		}
	}
	waitStatus(t, m, lead, Idle)
	if i, _ := m.Get(lead); i.RunError != nil {
		t.Errorf("a run stopped while waiting to retry kept the error: %+v", i.RunError)
	}
	// An error pi recorded without a message still counts, with no text
	// made up for it here.
	if i := run("test-run:blank"); i.RunError == nil || i.RunError.Message != "" || i.RunError.Provider != "codex" {
		t.Errorf("after an error with no message: %+v", i.RunError)
	}
	run("test-run:stop")

	// A worker that finished on an error shows it, done or not; the same name
	// hired again starts without it.
	w := ID("alice", "w1")
	if _, err := m.StartWorker(ctx, "alice", "w1", "", "test-run:error"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		i, _ := m.Get(w)
		if !i.FinishedAt.IsZero() && i.Status == Stopped {
			if i.RunError == nil {
				t.Fatal("a worker that finished on an error lost it")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worker never finished: %+v", i)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lead, _ := m.Get(lead); lead.RunError != nil {
		t.Errorf("a worker's error marked its lead: %+v", lead.RunError)
	}
	if err := m.RemoveWorker("alice", "w1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorker(ctx, "alice", "w1", "", "a new task"); err != nil {
		t.Fatal(err)
	}
	if i, _ := m.Get(w); i.RunError != nil {
		t.Errorf("a new hire of the same name has the old one's error: %+v", i.RunError)
	}
}

// Starting a run, even one that retries, does not clear the error; only the
// end of a run decides.
func TestRunErrorWaitsForTheRunsEnd(t *testing.T) {
	m, _ := newTestManager(t, 0)
	a := &agent{info: Info{ID: "alice/lead"}}
	step := func(kind, data string) *RunError {
		t.Helper()
		m.follow(a, nil, kind, json.RawMessage(data))
		m.mu.Lock()
		defer m.mu.Unlock()
		return a.info.RunError
	}
	const failed = `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"429 usage limit","provider":"codex","timestamp":1794652000000}}`
	step("agent_start", `{}`)
	step("message_end", failed)
	if e := step("agent_settled", `{"aborted":false}`); e == nil || e.Message != "429 usage limit" || !e.At.Equal(time.UnixMilli(1794652000000).UTC()) {
		t.Fatalf("after the failed run: %+v", e)
	}
	step("agent_start", `{}`)
	if step("message_end", `{"type":"message_end","message":{"role":"user"}}`) == nil {
		t.Error("a new run's prompt cleared the error")
	}
	if step("auto_retry_start", `{}`) == nil {
		t.Error("a retry cleared the error")
	}
	// The run's later message decides, not its first.
	step("message_end", failed)
	step("message_end", `{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}`)
	if step("message_end", `{"type":"message_end","message":{"role":"toolResult"}}`) == nil {
		t.Error("the error cleared before the run ended")
	}
	if e := step("agent_settled", `{"aborted":false}`); e != nil {
		t.Errorf("a run that recovered kept the error: %+v", e)
	}
	// Stopping a run while pi waits to retry its error settles it aborted
	// with no further message: the user's stop, not a failure.
	step("agent_start", `{}`)
	step("message_end", failed)
	step("auto_retry_start", `{"attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"429 usage limit"}`)
	step("auto_retry_end", `{"success":false,"attempt":1,"finalError":"Retry cancelled"}`)
	if e := step("agent_settled", `{"aborted":true}`); e != nil {
		t.Errorf("a run stopped while waiting to retry kept the error: %+v", e)
	}
	// Retries that run out end on the error.
	step("agent_start", `{}`)
	step("message_end", failed)
	step("auto_retry_start", `{"attempt":1}`)
	step("message_end", failed)
	step("auto_retry_end", `{"success":false,"attempt":1,"finalError":"429 usage limit"}`)
	if e := step("agent_settled", `{"aborted":false}`); e == nil || e.Message != "429 usage limit" {
		t.Errorf("after retries ran out: %+v", e)
	}
	// An error without a message is one all the same.
	step("agent_start", `{}`)
	step("message_end", `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"  ","provider":"codex"}}`)
	if e := step("agent_settled", `{"aborted":false}`); e == nil || e.Message != "" || e.Provider != "codex" {
		t.Errorf("after an error with no message: %+v", e)
	}
	// A long error is bounded in every snapshot of the agents.
	step("agent_start", `{}`)
	step("message_end", `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"`+strings.Repeat("x", 5000)+`"}}`)
	if e := step("agent_settled", `{}`); e == nil || len([]rune(e.Message)) != runErrorLimit {
		t.Errorf("long error: %d runes", len([]rune(e.Message)))
	}
}

func waitActivity(t *testing.T, m *Manager, id, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if i, err := m.Get(id); err == nil && i.Activity == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	i, _ := m.Get(id)
	t.Fatalf("%s is doing %q, want %q", id, i.Activity, want)
}
