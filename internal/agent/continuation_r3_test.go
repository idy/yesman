package agent

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/idy/yesman/internal/yesmen"
)

// The user's settings of a lead's pause commit in the order they were
// made, whichever write reaches the workspace first, and a restart reads
// the newest one.
func TestR3PauseSettingsCommitInOrder(t *testing.T) {
	t.Run("newer Stop survives a late resume, across a restart", func(t *testing.T) {
		m, s := newTestManager(t, 0)
		lead := "alice/lead"
		addTasks(t, s, yesmen.TaskEdit{Title: str("Orphan")})
		m.StopByUser(lead)
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var once sync.Once
		m.reviewBeforePersistPause = func(paused bool) {
			if !paused {
				once.Do(func() { close(entered); <-release })
			}
		}
		go func() { m.resumeByUser(lead); close(done) }()
		<-entered
		m.StopByUser(lead)
		close(release)
		<-done
		m.StopAll()
		again := New(m.cfg)
		t.Cleanup(again.StopAll)
		again.continueTasks("alice")
		if again.running(lead) != nil {
			t.Fatal("a restart woke the lead the user stopped last")
		}
	})
	t.Run("newer resume survives a late Stop write", func(t *testing.T) {
		m, s := newTestManager(t, 0)
		lead := "alice/lead"
		y, _ := s.Get("alice")
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var once sync.Once
		m.reviewBeforePersistPause = func(paused bool) {
			if paused {
				once.Do(func() { close(entered); <-release })
			}
		}
		go func() { m.StopByUser(lead); close(done) }()
		<-entered
		m.resumeByUser(lead)() // the user wrote to it meanwhile
		close(release)
		<-done
		c, err := s.Continuation("alice")
		if err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		paused := m.leadPausedLocked(y)
		m.mu.Unlock()
		if !c.LeadPausedAt.IsZero() || paused {
			t.Fatalf("an older Stop written late undid the user's newer message: disk=%v memory=%v", c.LeadPausedAt, paused)
		}
	})
}

// A Stop that could not be saved is reported, never taken as saved, and
// still holds in memory.
func TestR3UnsavedAbortIsReported(t *testing.T) {
	m, s := newTestManager(t, 0)
	lead := "alice/lead"
	y, _ := s.Get("alice")
	if _, _, err := m.ensure(lead); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(y.ContinuationFile(), 0o700); err != nil {
		t.Fatal(err)
	}
	err := m.AbortByUser(context.Background(), lead)
	if err == nil || !strings.Contains(err.Error(), "could not be saved") {
		t.Fatalf("an Abort whose pause was not saved reported %v", err)
	}
	info, _ := m.Get(lead)
	if !strings.HasPrefix(info.Detail, pauseUnsaved) {
		t.Fatalf("the lead does not show its unsaved Stop: %q", info.Detail)
	}
	m.mu.Lock()
	lp := m.pauseOf(y)
	paused, persisted := lp.paused, lp.persisted
	m.mu.Unlock()
	if !paused || persisted {
		t.Fatalf("memory paused=%v persisted=%v", paused, persisted)
	}
	// Once storage works again, the next look writes it and clears the note.
	if err := os.Remove(y.ContinuationFile()); err != nil {
		t.Fatal(err)
	}
	if !m.loadPause(y) {
		t.Fatal("the pause did not hold")
	}
	c, err := s.Continuation("alice")
	if err != nil || c.LeadPausedAt.IsZero() {
		t.Fatalf("retried pause not written: %v %v", c.LeadPausedAt, err)
	}
	if info, _ := m.Get(lead); strings.HasPrefix(info.Detail, pauseUnsaved) {
		t.Fatal("the note stayed after the pause was saved")
	}
}

// A notice refused as it was to be written was never one: it takes nothing
// of the hourly budget nor of its task's count.
func TestR3RefusedNoticeCostsNothing(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	lead := "alice/lead"
	if _, _, err := m.ensure(lead); err != nil {
		t.Fatal(err)
	}
	addTasks(t, s, yesmen.TaskEdit{Title: str("Orphan")})
	recorded := 0
	m.reviewBeforeTaskSend = func() {
		c, _ := s.Continuation("alice")
		recorded = len(c.Attempts)
		if err := m.AbortByUser(context.Background(), lead); err != nil {
			t.Error(err)
		}
	}
	m.continueTasks("alice")
	if recorded != 1 {
		t.Fatalf("the fixture did not reach a recorded attempt: %d", recorded)
	}
	if got := noticesOf(prompts(lead)); len(got) > 0 {
		t.Fatalf("notice sent to a lead the user stopped: %q", got)
	}
	c, err := s.Continuation("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Attempts) != 0 || len(c.Tasks) != 0 {
		t.Fatalf("a notice never written was charged: attempts=%v tasks=%v", c.Attempts, c.Tasks)
	}
	// The user writes to it: the notice now goes, once.
	m.reviewBeforeTaskSend = nil
	if err := m.Prompt(context.Background(), lead, "go on"); err != nil {
		t.Fatal(err)
	}
	waitNoticeListing(t, prompts, lead, "t1")
}
