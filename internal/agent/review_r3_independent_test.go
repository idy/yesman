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

// The storage failure is real: workers.json.tmp is an unwritable namespace.
// No manager field or classification result is changed by this fixture.
func TestReviewR3WorkerStopFailureMustHold(t *testing.T) {
	for _, abort := range []bool{false, true} {
		t.Run(map[bool]string{false: "Stop", true: "Abort"}[abort], func(t *testing.T) {
			m, s := newTestManager(t, 0)
			prompts := promptsTo(m)
			y, _ := s.Get("alice")
			if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			addTasks(t, s, yesmen.TaskEdit{Title: str("Goal user paused"), Owner: str("w1"), Status: str("doing")})
			if _, _, err := m.ensure("alice/w1"); err != nil {
				t.Fatal(err)
			}
			fail := filepath.Join(y.MetaDir(), "workers.json.tmp")
			if err := os.Mkdir(fail, 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			if abort {
				err = m.AbortByUser(context.Background(), "alice/w1")
			} else {
				m.StopByUser("alice/w1")
			}
			info, _ := m.Get("alice/w1")
			t.Logf("abort=%v return=%v detail=%q", abort, err, info.Detail)
			if err := os.Remove(fail); err != nil {
				t.Fatal(err)
			}
			m.continueTasks("alice")
			got := noticesOf(prompts("alice/lead"))
			if len(got) > 0 {
				t.Fatalf("user-paused worker responsibility automatically advertised for resumption after failed pause write: %q", got)
			}
		})
	}
}

func TestReviewR3OldHireAdmissionCannotEraseNewPause(t *testing.T) {
	m, s := newTestManager(t, 0)
	old, _ := s.Get("alice")
	m.loadPause(old)
	// Retain the actual automatic-admission closure, as a send waiting on wmu does.
	stale := m.admitAuto(old, nil)
	if _, err := s.Archive("alice", func(y yesmen.Yesman) error { return m.StopYesman(y.Name) }); err != nil {
		t.Fatal(err)
	}
	m.ForgetYesman("alice", old.HiredAt)
	if _, err := s.HireAs("alice", "alpha/shared"); err != nil {
		t.Fatal(err)
	}
	now, _ := s.Get("alice")
	if now.HiredAt.Equal(old.HiredAt) {
		t.Fatal("hire did not change")
	}
	addTasks(t, s, yesmen.TaskEdit{Title: str("New paused goal")})
	// Actual unsaved Stop must still fence automatic work in this process.
	if err := os.Mkdir(now.ContinuationFile(), 0700); err != nil {
		t.Fatal(err)
	}
	m.StopByUser("alice/lead")
	if err := os.Remove(now.ContinuationFile()); err != nil {
		t.Fatal(err)
	}
	_ = stale()
	m.mu.Lock()
	lp := m.leadPauses["alice"]
	same := lp.hiredAt.Equal(now.HiredAt) && lp.paused
	m.mu.Unlock()
	if !same {
		t.Error("stale automatic admission replaced the newer hire's in-memory user pause")
	}
	m.continueTasks("alice")
	if m.running("alice/lead") != nil {
		t.Fatal("new hire's unsaved user Stop was erased and automatic task notice started it")
	}
}

func TestReviewR3PauseErrorDTOAndRetry(t *testing.T) {
	m, s := newTestManager(t, 0)
	y, _ := s.Get("alice")
	if _, _, err := m.ensure("alice/lead"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(y.ContinuationFile(), 0700); err != nil {
		t.Fatal(err)
	}
	err := m.AbortByUser(context.Background(), "alice/lead")
	info, _ := m.Get("alice/lead")
	if err == nil || !strings.Contains(err.Error(), "could not be saved") || !strings.HasPrefix(info.Detail, pauseUnsaved) {
		t.Fatalf("not visible: %v %+v", err, info)
	}
	if err := os.Remove(y.ContinuationFile()); err != nil {
		t.Fatal(err)
	}
	if !m.loadPause(y) {
		t.Fatal("retry did not hold pause")
	}
	info, _ = m.Get("alice/lead")
	if strings.HasPrefix(info.Detail, pauseUnsaved) {
		t.Fatal("error not cleared")
	}
}
