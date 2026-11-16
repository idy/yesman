package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func workerRecord(t *testing.T, s *yesmen.Store, name string) yesmen.Worker {
	t.Helper()
	ws, err := s.Workers("alice")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(ws, func(w yesmen.Worker) bool { return w.Name == name })
	if i < 0 {
		t.Fatalf("no worker %s", name)
	}
	return ws[i]
}

// A worker the user stopped stays stopped while its pause cannot be
// written, is written once storage works, and is lifted by giving it work.
func TestR4WorkerPauseHeldWrittenAndLifted(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	y, _ := s.Get("alice")
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	addTasks(t, s, yesmen.TaskEdit{Title: str("Goal"), Owner: str("w1"), Status: str("doing")})
	if _, _, err := m.ensure("alice/w1"); err != nil {
		t.Fatal(err)
	}
	fail := filepath.Join(y.MetaDir(), "workers.json.tmp")
	if err := os.Mkdir(fail, 0o700); err != nil {
		t.Fatal(err)
	}
	err := m.AbortByUser(context.Background(), "alice/w1")
	if err == nil || !strings.Contains(err.Error(), "could not be saved") {
		t.Fatalf("an Abort whose pause was not saved reported %v", err)
	}
	if info, _ := m.Get("alice/w1"); !strings.HasPrefix(info.Detail, pauseUnsaved) {
		t.Fatalf("the worker does not show its unsaved Stop: %q", info.Detail)
	}
	m.continueTasks("alice") // the retry fails again: still held in memory
	reviewR5Drain(t, m)      // the evaluation it joined, if one ran, has ended
	if got := noticesOf(prompts("alice/lead")); len(got) > 0 {
		t.Fatalf("notice about a worker the user stopped: %q", got)
	}
	if err := os.Remove(fail); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice")
	reviewR5Drain(t, m) // the evaluation it joined, if one ran, has ended
	if w := workerRecord(t, s, "w1"); w.PausedAt.IsZero() {
		t.Fatal("the pause was not written once storage worked")
	}
	if info, _ := m.Get("alice/w1"); strings.HasPrefix(info.Detail, pauseUnsaved) {
		t.Fatal("the note stayed after the pause was saved")
	}
	if got := noticesOf(prompts("alice/lead")); len(got) > 0 {
		t.Fatalf("notice about a worker the user stopped: %q", got)
	}
	if err := m.Prompt(context.Background(), "alice/w1", "go on"); err != nil {
		t.Fatal(err)
	}
	if w := workerRecord(t, s, "w1"); !w.PausedAt.IsZero() {
		t.Fatalf("work given to the worker did not lift its pause: %v", w.PausedAt)
	}
}

// A pause decided before the worker was given work is never written after.
func TestR4LateWorkerPauseNeverRepausesWork(t *testing.T) {
	m, s := newTestManager(t, 0)
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ensure("alice/w1"); err != nil {
		t.Fatal(err)
	}
	persist := m.pauseByUser("alice/w1")
	if err := m.Prompt(context.Background(), "alice/w1", "new work"); err != nil {
		t.Fatal(err)
	}
	if err := persist(); err != nil {
		t.Fatal(err)
	}
	if w := workerRecord(t, s, "w1"); !w.PausedAt.IsZero() {
		t.Fatal("a pause from before the worker was given work was written after it")
	}
}

// A pause of a worker holds for that incarnation only: a new worker of the
// same name is not paused by it.
func TestR4WorkerPauseIsPerIncarnation(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	y, _ := s.Get("alice")
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ensure("alice/w1"); err != nil {
		t.Fatal(err)
	}
	fail := filepath.Join(y.MetaDir(), "workers.json.tmp")
	if err := os.Mkdir(fail, 0o700); err != nil {
		t.Fatal(err)
	}
	m.StopByUser("alice/w1") // held in memory only
	if err := os.Remove(fail); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveWorker("alice", "w1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	addTasks(t, s, yesmen.TaskEdit{Title: str("Given to the new w1"), Owner: str("w1"), Status: str("doing")})
	m.continueTasks("alice")
	waitNoticeListing(t, prompts, "alice/lead", "t1")
	if w := workerRecord(t, s, "w1"); !w.PausedAt.IsZero() {
		t.Fatal("the old worker's pause was written on the new one")
	}
}

// A late read of an earlier hire's pause neither replaces nor clears the
// pause the user set on the workspace hired again.
func TestR4StaleLoadPauseKeepsNewHire(t *testing.T) {
	m, s := newTestManager(t, 0)
	old, _ := s.Get("alice")
	if _, err := s.Archive("alice", func(y yesmen.Yesman) error { return m.StopYesman(y.Name) }); err != nil {
		t.Fatal(err)
	}
	m.ForgetYesman("alice", old.HiredAt)
	if _, err := s.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	now, _ := s.Get("alice")
	if err := os.Mkdir(now.ContinuationFile(), 0o700); err != nil {
		t.Fatal(err)
	}
	m.StopByUser("alice/lead") // unsaved: memory only
	if err := os.Remove(now.ContinuationFile()); err != nil {
		t.Fatal(err)
	}
	if !m.loadPause(old) {
		t.Fatal("a stale hire read as not paused")
	}
	m.mu.Lock()
	lp := m.leadPauses["alice"]
	kept := lp != nil && lp.hiredAt.Equal(now.HiredAt) && lp.paused && !lp.persisted
	m.mu.Unlock()
	if !kept {
		t.Fatal("a late read of the earlier hire replaced the new hire's pause")
	}
	addTasks(t, s, yesmen.TaskEdit{Title: str("Goal")})
	m.continueTasks("alice")
	reviewR5Drain(t, m) // the evaluation it joined, if one ran, has ended
	if m.running("alice/lead") != nil {
		t.Fatal("the new hire's lead the user stopped was started")
	}
	if c, err := s.Continuation("alice"); err != nil || c.LeadPausedAt.IsZero() {
		t.Fatalf("the new hire's pause was not written on retry: %v %v", c.LeadPausedAt, err)
	}
}
