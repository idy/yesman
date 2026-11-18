package agent

import (
	"context"
	"github.com/idy/yesman/internal/yesmen"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Gate only scheduling after the real close's active-hire check and before
// its Store finish write. Both reports/runs come from actual fake-pi RPCs.
func TestReviewR6OldFinishLeavesReplacementRunning(t *testing.T) {
	m, s := newTestManager(t, 0, "FAKE_PI_HOLD_REPORTS=1")
	id := "alice/w1"
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	old, op, err := m.ensure(id)
	if err != nil {
		t.Fatal(err)
	}
	prompts := promptsTo(m)
	waiting := eventsOf(m, "test_report_waiting")
	reached, release := make(chan struct{}), make(chan struct{})
	reviewR6BeforeFinish = func(a *agent) {
		if a == old {
			close(reached)
			<-release
		}
	}
	t.Cleanup(func() { reviewR6BeforeFinish = nil })
	if err := m.Prompt(context.Background(), id, "old actual work"); err != nil {
		t.Fatal(err)
	}
	waitEvent := func(n int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for len(waiting(id)) < n && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if len(waiting(id)) < n {
			t.Fatalf("only %d held reports, want %d", len(waiting(id)), n)
		}
	}
	waitEvent(1)
	if _, err := op.call(context.Background(), map[string]any{"type": "test_release_one_report"}); err != nil {
		t.Fatal(err)
	}
	waitCount(t, prompts, "alice/lead", 1)
	lp := m.running("alice/lead")
	if lp == nil {
		t.Fatal("report recipient missing")
	}
	if _, err := lp.call(context.Background(), map[string]any{"type": "test_release_prompts"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reached:
	case <-time.After(time.Second):
		t.Fatal("actual report did not reach finish")
	}
	if err := m.RemoveWorker("alice", "w1"); err != nil {
		close(release)
		t.Fatal(err)
	}
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		close(release)
		t.Fatal(err)
	}
	if err := m.Prompt(context.Background(), id, "new actual work whose report remains unread"); err != nil {
		close(release)
		t.Fatal(err)
	}
	waitEvent(2)
	np := m.running(id)
	if np == nil || np == op {
		close(release)
		t.Fatal("replacement process absent")
	}
	y, _ := s.Get("alice")
	path := filepath.Join(y.MetaDir(), "workers.json")
	before, err := os.ReadFile(path)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		done := old.reports == 0
		m.mu.Unlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	m.mu.Lock()
	done := old.reports == 0
	m.mu.Unlock()
	if !done {
		t.Fatal("old report chain did not finish")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("old finish changed replacement workers.json bytes")
	}
	w := reviewR4WorkerRecord(t, s, "w1")
	t.Logf("replacement FinishedAt=%v", w.FinishedAt)
	select {
	case <-np.done:
		t.Error("old report stopped the replacement process while its own report remains unread")
	default:
	}
	if m.running(id) != np {
		t.Error("current replacement no longer running")
	}
}
