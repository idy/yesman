package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// An old worker's reopen, held until a worker of the same name replaced it,
// leaves every field the replacement records alone: finished, failed and
// paused alike (the reviewer's metadata control, renamed).
func TestR6OldReopenLeavesReplacementFields(t *testing.T) {
	m, s := newTestManager(t, 0)
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ensure("alice/w1"); err != nil {
		t.Fatal(err)
	}
	reached, release := make(chan struct{}), make(chan struct{})
	reviewR5BeforeStoreReopen = func() { close(reached); <-release }
	t.Cleanup(func() { reviewR5BeforeStoreReopen = nil })
	done := make(chan error, 1)
	go func() { done <- m.Prompt(context.Background(), "alice/w1", "old registered input") }()
	<-reached
	reviewR5BeforeStoreReopen = nil
	if err := m.RemoveWorker("alice", "w1"); err != nil {
		t.Fatal(err)
	}
	since := time.Now().UTC()
	finished, failed, paused := since.Add(-time.Second), since.Add(-2*time.Second), since.Add(-3*time.Second)
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: since, FinishedAt: finished, FailedAt: failed, PausedAt: paused}); err != nil {
		t.Fatal(err)
	}
	y, _ := s.Get("alice")
	path := filepath.Join(y.MetaDir(), "workers.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Error("the retired old prompt succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("the old reopen changed the replacement:\n%s\nwas\n%s", after, before)
	}
}

// The Store's own fence: a reopen or finish for one incarnation changes
// nothing of another of the same name, and does what it did for its own.
func TestR6StoreWritesOnlyTheirIncarnation(t *testing.T) {
	_, s := newTestManager(t, 0)
	old := time.Now().UTC()
	now := old.Add(time.Second)
	at := now.Add(time.Second)
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: now, FinishedAt: at, FailedAt: at, PausedAt: at}); err != nil {
		t.Fatal(err)
	}
	y, _ := s.Get("alice")
	path := filepath.Join(y.MetaDir(), "workers.json")
	before, _ := os.ReadFile(path)
	if reopened, err := s.ReopenWorkerIf("alice", "w1", old, func(yesmen.Worker) bool { return true }); err != nil || reopened {
		t.Fatalf("reopen of another incarnation: %v %v", reopened, err)
	}
	if err := s.FinishWorkerOf("alice", "w1", old, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("a write for another incarnation changed the worker")
	}
	// Its own: finished and failed clear; the pause only as unpause says.
	if reopened, err := s.ReopenWorkerIf("alice", "w1", now, func(yesmen.Worker) bool { return false }); err != nil || !reopened {
		t.Fatalf("reopen of its own: %v %v", reopened, err)
	}
	if w := workerRecord(t, s, "w1"); !w.FinishedAt.IsZero() || !w.FailedAt.IsZero() || !w.PausedAt.Equal(at) {
		t.Fatalf("own reopen: %+v", w)
	}
	if err := s.FinishWorkerOf("alice", "w1", now, at); err != nil {
		t.Fatal(err)
	}
	if w := workerRecord(t, s, "w1"); !w.FinishedAt.Equal(at) {
		t.Fatalf("own finish not written: %+v", w)
	}
}

// The user's Stop of a live lead of a hire, while the record kept belongs
// to an earlier hire not forgotten yet and an Archive holds the workspace,
// owns the pause at once (no lease), holds it in memory, and a late
// ForgetYesman of the earlier hire does not take it away; a call about the
// earlier hire stays refused.
func TestR6LiveLeadOwnsPauseWithoutLease(t *testing.T) {
	m, s := newTestManager(t, 0)
	m.StopByUser("alice/lead")
	old, _ := s.Get("alice")
	if _, err := s.Archive("alice", func(y yesmen.Yesman) error { return m.StopYesman(y.Name) }); err != nil {
		t.Fatal(err)
	}
	now, err := s.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ensure("alice/lead"); err != nil {
		t.Fatal(err)
	}
	entered, release, archived := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := s.Archive("alice", func(y yesmen.Yesman) error { close(entered); <-release; return m.StopYesman(y.Name) })
		archived <- err
	}()
	<-entered
	owned := make(chan bool, 1)
	go func() { owned <- m.ownPause(now) }()
	select {
	case ok := <-owned:
		if !ok {
			t.Error("the live hire did not own its pause")
		}
	case <-time.After(250 * time.Millisecond):
		t.Error("owning the live hire's pause waited for the Archive lease")
	}
	m.mu.Lock()
	lp := m.leadPauses["alice"]
	mine := lp != nil && lp.hiredAt.Equal(now.HiredAt)
	m.mu.Unlock()
	if !mine {
		t.Fatal("the record is not the live hire's")
	}
	m.ForgetYesman("alice", old.HiredAt)
	m.mu.Lock()
	kept := m.leadPauses["alice"] == lp
	staleRefused := m.leadPausedLocked(old)
	m.mu.Unlock()
	if !kept || !staleRefused {
		t.Fatalf("late Forget/stale hire: kept=%v refused=%v", kept, staleRefused)
	}
	close(release)
	<-archived // its move may fail (same-second archive name): either way it has stopped the lead
	// The earlier hire stays refused: it has no live lead, and the workspace
	// does not have it.
	if m.ownPause(old) {
		t.Fatal("an archived hire owned the pause")
	}
}
