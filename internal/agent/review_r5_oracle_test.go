package agent

import (
	"context"
	"github.com/idy/yesman/internal/yesmen"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This is the original R4-F1 trigger. The observation waits for the worker's
// report, not a total count that would forbid the requested task reminder.
// The original persistence assertion remains; cold dedupe and a real changed
// task are separate assertions, so dedupe cannot masquerade as a saved pause.
func TestReviewR5ResumePersistenceAndColdResponsibility(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	y, _ := s.Get("alice")
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	addTasks(t, s, yesmen.TaskEdit{Title: str("Goal still outstanding"), Owner: str("w1"), Status: str("doing")})
	if _, _, err := m.ensure("alice/w1"); err != nil {
		t.Fatal(err)
	}
	m.StopByUser("alice/w1")
	w := reviewR4WorkerRecord(t, s, "w1")
	if w.PausedAt.IsZero() {
		t.Fatal("Stop did not persist")
	}
	fail := filepath.Join(y.MetaDir(), "workers.json.tmp")
	if err := os.Mkdir(fail, 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.Prompt(context.Background(), "alice/w1", "explicit new work"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && countPrefix(prompts("alice/lead"), "[yesman] Worker \"w1\" finished a run.") == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if countPrefix(prompts("alice/lead"), "[yesman] Worker \"w1\" finished a run.") != 1 {
		t.Fatalf("expected one actual report: %q", prompts("alice/lead"))
	}
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		m.mu.Lock()
		a := m.agents["alice/w1"]
		done := a != nil && a.reports == 0
		m.mu.Unlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	m.continueTasks("alice")
	// The report can be acknowledged before the lead settles. Wait for
	// the task notice while the old pause still remains on disk.
	waitNoticeListing(t, prompts, "alice/lead", "t1")
	reviewR5Drain(t, m)
	t.Logf("during failure lead messages: %q", prompts("alice/lead"))
	if noticesListing(prompts("alice/lead"), "t1") != 1 {
		t.Error("current explicit resume must expose the orphan despite stale disk pause")
	}
	i, err := m.Get("alice/w1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(i.Detail, "Given work, but its Stop could not be cleared") {
		t.Errorf("resume error absent from actual Info: %q", i.Detail)
	}
	if err := os.Remove(fail); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice")
	reviewR5Drain(t, m)
	w = reviewR4WorkerRecord(t, s, "w1")
	t.Logf("after repair: paused=%v finished=%v", w.PausedAt, w.FinishedAt)
	if !w.PausedAt.IsZero() {
		t.Error("accepted explicit new work lost its pause-clear after I/O failure; retry leaves old durable pause")
	}
	i, err = m.Get("alice/w1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(i.Detail, "Given work, but its Stop could not be cleared") {
		t.Errorf("resume error stayed after durable success: %q", i.Detail)
	}
	c, err := s.Continuation("alice")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("before cold: task=%+v attempts=%d", c.Tasks["t1"], len(c.Attempts))
	if got := prompts("alice/w1"); len(got) != 1 || got[0] != "explicit new work" {
		t.Errorf("resume retry dispatched input again: %q", got)
	}
	b, _ := s.Board("alice")
	if len(b.Tasks) != 1 || b.Tasks[0].Status != "doing" {
		t.Fatalf("reminder altered responsibility: %+v", b)
	}
	m.StopAll()
	again := New(m.cfg)
	t.Cleanup(again.StopAll)
	after := promptsTo(again)
	again.continueTasks("alice")
	quiet(t, after, "alice/lead", 0)
	if again.running("alice/w1") != nil {
		t.Error("cold retry started a worker")
	}
	if c.Tasks["t1"].Delivered && again.running("alice/lead") != nil {
		t.Error("cold restart replayed an already-delivered task state")
	}
	c2, _ := s.Continuation("alice")
	if c2.Tasks["t1"].Count != c.Tasks["t1"].Count || len(c2.Attempts) != len(c.Attempts) {
		t.Error("cold same-state check charged another attempt")
	}
	if _, err := s.ChangeBoard("alice", yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: str("todo")}}}); err != nil {
		t.Fatal(err)
	}
	again.continueTasks("alice")
	reviewR5Drain(t, again)
	if n := noticesListing(after("alice/lead"), "t1"); n != 1 {
		t.Errorf("cold unfinished changed task is still suppressed after explicit resume: notices=%d messages=%q", n, after("alice/lead"))
	}
	if c3, _ := s.Continuation("alice"); c3.Tasks["t1"].Delivered && c3.Tasks["t1"].Sig == c.Tasks["t1"].Sig {
		t.Error("new task status reused prior identity")
	}
}

func reviewR5Drain(t *testing.T, m *Manager) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		m.mu.Lock()
		c := m.continuations["alice"]
		busy := c != nil && (c.running || c.again)
		for _, a := range m.agents {
			busy = busy || a.prompts != 0 || a.reports != 0
		}
		m.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("continuation/report chain did not drain within original one-second observation bound")
}

func TestReviewR5NormalReportAndNotice(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	addTasks(t, s, yesmen.TaskEdit{Title: str("Goal still outstanding"), Owner: str("w1"), Status: str("doing")})
	if err := m.Prompt(context.Background(), "alice/w1", "explicit new work"); err != nil {
		t.Fatal(err)
	}
	waitCount(t, prompts, "alice/lead", 2)
	if countPrefix(prompts("alice/lead"), "[yesman] Worker \"w1\" finished a run.") != 1 || noticesListing(prompts("alice/lead"), "t1") != 1 {
		t.Fatalf("unexpected messages: %q", prompts("alice/lead"))
	}
	t.Logf("normal actual report and task notice: %q", prompts("alice/lead"))
}
