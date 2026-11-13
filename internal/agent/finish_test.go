package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func waitFinished(t *testing.T, m *Manager, id string, after time.Time) Info {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if i, err := m.Get(id); err == nil && i.Status == Stopped && i.FinishedAt.After(after) {
			return i
		}
		time.Sleep(10 * time.Millisecond)
	}
	i, _ := m.Get(id)
	t.Fatalf("%s did not finish and close: %+v", id, i)
	return i
}

// A worker whose report reached its lead is closed and shown as done; a
// message starts it again, with work.
func TestFinishedWorkerIsClosedAndReopened(t *testing.T) {
	m, store := newTestManager(t, 0)
	prompts := promptsTo(m)
	ctx := context.Background()
	start := time.Now()
	if _, err := m.StartWorker(ctx, "alice", "w1", "  Fix the   login timeout ", "Run task t5.\nMore detail."); err != nil {
		t.Fatal(err)
	}
	id := ID("alice", "w1")
	i := waitFinished(t, m, id, start)
	if i.Title != "Fix the login timeout" {
		t.Errorf("title = %q", i.Title)
	}
	if p := m.running(id); p != nil {
		t.Error("a finished worker's process still runs")
	}

	again := time.Now()
	if err := m.Prompt(ctx, id, "[from your lead] one more thing"); err != nil {
		t.Fatalf("a finished worker did not start again: %v", err)
	}
	waitFinished(t, m, id, again)
	lead := ID("alice", yesmen.LeadName)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(prompts(lead)) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(prompts(lead)); n != 2 {
		t.Errorf("the lead received %d reports: %q", n, prompts(lead))
	}
	if ws, _ := store.Workers("alice"); len(ws) != 1 || ws[0].FinishedAt.Before(again) {
		t.Errorf("worker record = %+v", ws)
	}
}

// Reading a finished worker's conversation starts it; it is closed again.
func TestReadingAFinishedWorkerClosesItAgain(t *testing.T) {
	m, _ := newTestManager(t, 0)
	ctx := context.Background()
	if _, err := m.StartWorker(ctx, "alice", "w1", "Docs", "a task"); err != nil {
		t.Fatal(err)
	}
	id := ID("alice", "w1")
	waitFinished(t, m, id, time.Time{})
	if _, err := m.Messages(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, id, Stopped)
	if i, _ := m.Get(id); i.FinishedAt.IsZero() {
		t.Error("reading a worker reopened it")
	}
}

// A worker that has not finished stays open when its conversation is read.
func TestReadingAnOpenWorkerKeepsItRunning(t *testing.T) {
	m, store := newTestManager(t, 0)
	if err := store.AddWorker("alice", yesmen.Worker{Name: "w1", Task: "t", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	id := ID("alice", "w1")
	if _, err := m.Messages(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if i, _ := m.Get(id); i.Status != Idle {
		t.Errorf("status = %s", i.Status)
	}
}

func TestWorkerTitleLimit(t *testing.T) {
	m, store := newTestManager(t, 0)
	if _, err := m.StartWorker(context.Background(), "alice", "w1", strings.Repeat("修", titleLimit+1), "a task"); err == nil {
		t.Fatal("a long title was accepted")
	}
	if ws, _ := store.Workers("alice"); len(ws) != 0 {
		t.Errorf("workers = %+v", ws)
	}
}
