package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// stopWorkerWritesFail persists the user's Stop of alice/w1, then makes
// writes of workers.json fail while it can still be read. It returns the
// function that makes them work again.
func stopWorkerWritesFail(t *testing.T, m *Manager, s *yesmen.Store) func() {
	t.Helper()
	y, _ := s.Get("alice")
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	addTasks(t, s, yesmen.TaskEdit{Title: str("Goal still outstanding"), Owner: str("w1"), Status: str("doing")})
	if _, _, err := m.ensure("alice/w1"); err != nil {
		t.Fatal(err)
	}
	m.StopByUser("alice/w1")
	if workerRecord(t, s, "w1").PausedAt.IsZero() {
		t.Fatal("Stop did not persist")
	}
	fail := filepath.Join(y.MetaDir(), "workers.json.tmp")
	if err := os.Mkdir(fail, 0o700); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Remove(fail); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

func workerInfo(t *testing.T, m *Manager, id string) Info {
	t.Helper()
	i, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func waitWorkerSettled(t *testing.T, m *Manager, id string) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		m.mu.Lock()
		a := m.agents[id]
		done := a != nil && a.reports == 0 && a.prompts == 0 && a.info.Status == Idle
		m.mu.Unlock()
		if done {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s did not settle", id)
}

// The R4-F1 probe's trigger, with its oracle kept except where it required
// the bug: explicit work given to a worker the user stopped lifts the pause
// even while that cannot be written. The lead is told of the outstanding
// goal at once (classification follows the work given, not the old record),
// Info says the clear is not saved, it is written once storage works, and
// a restart then sees no pause: a new state of the task reaches the lead.
func TestR5ResumeWriteFailureRecovers(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	repair := stopWorkerWritesFail(t, m, s)
	if err := m.Prompt(context.Background(), "alice/w1", "explicit new work"); err != nil {
		t.Fatal(err)
	}
	// The report, and the notice of the task its owner no longer carries.
	waitCount(t, prompts, "alice/lead", 2)
	if noticesListing(prompts("alice/lead"), "t1") != 1 {
		t.Fatalf("the goal was not told while the resume was unsaved: %q", prompts("alice/lead"))
	}
	waitWorkerSettled(t, m, "alice/w1")
	if d := workerInfo(t, m, "alice/w1").Detail; !strings.HasPrefix(d, resumeUnsaved) {
		t.Fatalf("Info does not say the resume is unsaved: %q", d)
	}
	repair()
	m.continueTasks("alice")
	reviewR5Drain(t, m) // the evaluation it joined, if one ran, has ended
	w := workerRecord(t, s, "w1")
	if !w.PausedAt.IsZero() {
		t.Error("accepted explicit new work lost its pause-clear after I/O failure; retry leaves old durable pause")
	}
	if d := workerInfo(t, m, "alice/w1").Detail; strings.HasPrefix(d, resumeUnsaved) {
		t.Fatalf("Info still says the resume is unsaved once written: %q", d)
	}
	m.StopAll()
	again := New(m.cfg)
	t.Cleanup(again.StopAll)
	after := promptsTo(again)
	// Told once already, and deduped across the restart; a new state of the
	// task is told, which a pause the user set would hold back.
	if _, err := s.ChangeBoard("alice", yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: str("todo")}}}); err != nil {
		t.Fatal(err)
	}
	again.continueTasks("alice")
	waitNoticeListing(t, after, "alice/lead", "t1")
}

// A Stop after the worker was given work wins, whichever write lands first:
// the reopen held before it writes does not clear the newer pause, and an
// unsaved resume tried again does not either.
func TestR5StopAfterResumeWins(t *testing.T) {
	t.Run("held reopen", func(t *testing.T) {
		m, s := newTestManager(t, 0)
		repair := stopWorkerWritesFail(t, m, s)
		repair() // storage works; only the order is under test
		stopped := make(chan struct{})
		var once bool
		m.reviewBeforeReopen = func() {
			if once {
				return
			}
			once = true
			go func() {
				m.StopByUser("alice/w1")
				close(stopped)
			}()
			select {
			case <-stopped:
			case <-time.After(2 * time.Second):
			}
		}
		_ = m.Prompt(context.Background(), "alice/w1", "explicit new work")
		<-stopped
		if workerRecord(t, s, "w1").PausedAt.IsZero() {
			t.Fatal("the reopen given before the user's Stop cleared it")
		}
		m.continueTasks("alice")
		reviewR5Drain(t, m) // the evaluation it joined, if one ran, has ended
		if workerRecord(t, s, "w1").PausedAt.IsZero() {
			t.Fatal("a retry cleared the user's later Stop")
		}
	})
	t.Run("unsaved resume", func(t *testing.T) {
		m, s := newTestManager(t, 0)
		repair := stopWorkerWritesFail(t, m, s)
		if err := m.Prompt(context.Background(), "alice/w1", "explicit new work"); err != nil {
			t.Fatal(err)
		}
		waitWorkerSettled(t, m, "alice/w1")
		first := workerRecord(t, s, "w1").PausedAt
		m.StopByUser("alice/w1") // unsaved too
		repair()
		m.continueTasks("alice")
		reviewR5Drain(t, m) // the evaluation it joined, if one ran, has ended
		w := workerRecord(t, s, "w1")
		if w.PausedAt.IsZero() || w.PausedAt.Equal(first) {
			t.Fatalf("the later Stop was not what was written: %v (first %v)", w.PausedAt, first)
		}
	})
}

// An unsaved resume belongs to the worker that was given work: a worker of
// the same name hired after it keeps the pause the user set on it.
func TestR5ResumeStaysWithItsWorker(t *testing.T) {
	m, s := newTestManager(t, 0)
	repair := stopWorkerWritesFail(t, m, s)
	if err := m.Prompt(context.Background(), "alice/w1", "explicit new work"); err != nil {
		t.Fatal(err)
	}
	waitWorkerSettled(t, m, "alice/w1")
	m.Stop("alice/w1")
	// Another w1 replaces it in the workspace, paused by the user.
	y, _ := s.Get("alice")
	ws, err := s.Workers("alice")
	if err != nil {
		t.Fatal(err)
	}
	newer := ws[0].StartedAt.Add(time.Second)
	ws[0].StartedAt, ws[0].PausedAt = newer, newer
	data, _ := json.Marshal(ws)
	if err := os.WriteFile(filepath.Join(y.MetaDir(), "workers.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	repair()
	m.continueTasks("alice")
	reviewR5Drain(t, m) // the evaluation it joined, if one ran, has ended
	if w := workerRecord(t, s, "w1"); !w.PausedAt.Equal(newer) {
		t.Fatalf("the old worker's resume cleared the new worker's pause: %v", w.PausedAt)
	}
	if d := workerInfo(t, m, "alice/w1").Detail; strings.HasPrefix(d, resumeUnsaved) {
		t.Fatalf("the new worker shows the old one's unsaved resume: %q", d)
	}
}

// A hire whose time is earlier than an archived one's owns its pause all
// the same, also when the Manager still keeps the archived hire's record
// (not forgotten); a late call about the archived hire changes nothing.
func TestR5HireIdentityNotClockOrder(t *testing.T) {
	m, s := newTestManager(t, 0)
	y, _ := s.Get("alice")
	path := filepath.Join(y.MetaDir(), "yesman.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	stamp, _ := json.Marshal(time.Now().UTC().Add(time.Hour))
	obj["hiredAt"] = stamp
	data, _ = json.Marshal(obj)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	old, _ := s.Get("alice")
	m.StopByUser("alice/lead")
	if _, err := s.Archive("alice", func(y yesmen.Yesman) error { return m.StopYesman(y.Name) }); err != nil {
		t.Fatal(err)
	}
	// No ForgetYesman: the archived hire's record is still kept here.
	now, err := s.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	if !now.HiredAt.Before(old.HiredAt) {
		t.Fatal("fixture did not reverse the hire times")
	}
	addTasks(t, s, yesmen.TaskEdit{Title: str("New current goal")})
	m.StopByUser("alice/lead")
	if c, err := s.Continuation("alice"); err != nil || c.LeadPausedAt.IsZero() {
		t.Fatalf("the current hire's Stop was not written: %v %v", c.LeadPausedAt, err)
	}
	// Late calls about the archived hire.
	if !m.loadPause(old) {
		t.Fatal("the archived hire read as not paused")
	}
	m.resumeByUser("alice/lead") // the user's resume of the current hire...
	m.StopByUser("alice/lead")   // ...and Stop again
	_, persist := m.setPause(old, false)
	if err := persist(); err != nil {
		t.Fatal(err)
	}
	refused := m.admitAuto(old, nil)() != nil
	m.mu.Lock()
	lp := m.leadPauses["alice"]
	kept := lp != nil && lp.hiredAt.Equal(now.HiredAt) && lp.known && lp.paused
	m.mu.Unlock()
	if !kept || !refused {
		t.Fatalf("a late call about the archived hire took over: kept=%v refused=%v", kept, refused)
	}
	if c, _ := s.Continuation("alice"); c.LeadPausedAt.IsZero() {
		t.Fatal("a late call about the archived hire cleared the current Stop")
	}
	m.StopAll()
	again := New(m.cfg)
	t.Cleanup(again.StopAll)
	again.continueTasks("alice")
	if again.running("alice/lead") != nil {
		t.Fatal("restart woke the current hire despite the user's Stop")
	}
}
