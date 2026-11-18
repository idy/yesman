package agent

import (
	"context"
	"github.com/idy/yesman/internal/yesmen"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReviewR5OldReopenCannotResetReplacement(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ensure("alice/w1"); err != nil {
		t.Fatal(err)
	}
	reached, release := make(chan struct{}), make(chan struct{})
	// Only schedules the real operation after its active-scope check, before
	// the Store writer lock. No data, guards or results are changed by this hook.
	reviewR5BeforeStoreReopen = func() { close(reached); <-release }
	t.Cleanup(func() { reviewR5BeforeStoreReopen = nil })
	done := make(chan error, 1)
	go func() { done <- m.Prompt(context.Background(), "alice/w1", "old registered input") }()
	<-reached
	// The old hook has already been invoked. Future, independent work is not held.
	reviewR5BeforeStoreReopen = nil
	if err := m.RemoveWorker("alice", "w1"); err != nil {
		t.Fatal(err)
	}
	since := time.Now().UTC()
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: since}); err != nil {
		t.Fatal(err)
	}
	if err := m.Prompt(context.Background(), "alice/w1", "new worker actual work"); err != nil {
		t.Fatal(err)
	}
	waitCount(t, prompts, "alice/lead", 1)
	w := reviewR4WorkerRecord(t, s, "w1")
	if w.FinishedAt.IsZero() {
		t.Fatal("replacement actual report did not finish")
	}
	m.StopByUser("alice/w1")
	w = reviewR4WorkerRecord(t, s, "w1")
	finished, failed, paused := w.FinishedAt, w.FailedAt, w.PausedAt
	if paused.IsZero() {
		t.Fatal("replacement actual Stop did not persist")
	}
	y, _ := s.Get("alice")
	path := filepath.Join(y.MetaDir(), "workers.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	err = <-done
	if err == nil {
		t.Error("retired old prompt unexpectedly succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w = reviewR4WorkerRecord(t, s, "w1")
	if string(after) != string(before) || !w.FinishedAt.Equal(finished) || !w.FailedAt.Equal(failed) || !w.PausedAt.Equal(paused) {
		t.Errorf("old reopen changed replacement metadata: finished=%v want=%v failed=%v want=%v paused=%v want=%v", w.FinishedAt, finished, w.FailedAt, failed, w.PausedAt, paused)
	}
}
