package agent

import (
	"context"
	"github.com/idy/yesman/internal/yesmen"
	"os"
	"sync"
	"testing"
	"time"
)

func TestReviewR2OlderResumeCannotEraseNewerStop(t *testing.T) {
	m, s := newTestManager(t, 0)
	y, _ := s.Get("alice")
	lead := "alice/lead"
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
	m.StopByUser(lead) // newer user action finishes its actual persistent write first
	newer, _ := s.Continuation("alice")
	if newer.LeadPausedAt.IsZero() {
		t.Fatal("new Stop was not persisted")
	}
	close(release)
	<-done
	current, _ := s.Continuation("alice")
	m.mu.Lock()
	lp := m.pauseOf(y)
	paused, persisted := lp.paused, lp.persisted
	m.mu.Unlock()
	t.Logf("memory paused=%v persisted=%v; disk pause=%v newer=%v", paused, persisted, current.LeadPausedAt, newer.LeadPausedAt)
	if !current.LeadPausedAt.Equal(newer.LeadPausedAt) {
		t.Fatal("older resume overwrote newer durably completed Stop; restart would load unpaused")
	}
}

func TestReviewR2StartupMustRevalidateWaitAtDispatch(t *testing.T) {
	for _, kind := range []string{yesmen.WaitPaused, yesmen.WaitTasks, yesmen.WaitUser} {
		t.Run(kind, func(t *testing.T) {
			m, s := newTestManager(t, 0)
			prompts := promptsTo(m)
			addTasks(t, s, yesmen.TaskEdit{Title: str("Target")}, yesmen.TaskEdit{Title: str("Predecessor"), Wait: &yesmen.WaitEdit{Kind: yesmen.WaitExternal, Reason: "CI"}})
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			m.cfg.Command = func(ctx context.Context) (string, []string, error) {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return "", nil, ctx.Err()
				}
				return m.cfg.Pi, nil, nil
			}
			go func() { m.continueTasks("alice"); close(done) }()
			<-entered
			// The attempt is durable now, but pi has neither started nor received anything.
			c, _ := s.Continuation("alice")
			if len(c.Attempts) != 1 {
				t.Fatal("did not reach post-reservation startup gap")
			}
			w := &yesmen.WaitEdit{Kind: kind, Reason: "User said stop"}
			if kind == yesmen.WaitTasks {
				w.Tasks = []string{"t2"}
			}
			if kind == yesmen.WaitUser {
				h, _, err := s.AddHandoff("alice", yesmen.Handoff{Type: yesmen.HandoffInstall, Title: "Install", Task: "t1"})
				if err != nil {
					t.Fatal(err)
				}
				w.Handoff = h.ID
			}
			if _, err := s.ChangeBoard("alice", yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Wait: w}}}); err != nil {
				t.Fatal(err)
			}
			close(release)
			<-done
			if got := noticesOf(prompts("alice/lead")); len(got) > 0 {
				t.Fatalf("dispatch used obsolete snapshot after committed %s wait during real runtime resolution: %q", kind, got)
			}
		})
	}
}

func TestReviewR2StopCancelsWhilePauseStorageBusy(t *testing.T) {
	m, s := newTestManager(t, 0)
	y, _ := s.Get("alice")
	lead := "alice/lead"
	_, p, err := m.ensure(lead)
	if err != nil {
		t.Fatal(err)
	}
	held, release, writerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = s.UpdateContinuation("alice", y.HiredAt, func(c *yesmen.Continuation) bool { close(held); <-release; return false })
		close(writerDone)
	}()
	<-held
	done := make(chan struct{})
	go func() { m.StopByUser(lead); close(done) }()
	blocked := false
	select {
	case <-p.done:
	case <-time.After(250 * time.Millisecond):
		blocked = true
	}
	close(release)
	<-writerDone
	<-done
	if blocked {
		t.Fatal("user Stop did not even cancel running process while pause persistence waited; held >250ms by file I/O lock")
	}
}

func TestReviewR2CorruptNullPauseFailsClosed(t *testing.T) {
	m, s := newTestManager(t, 0)
	y, _ := s.Get("alice")
	addTasks(t, s, yesmen.TaskEdit{Title: str("Orphan")})
	if err := os.WriteFile(y.ContinuationFile(), []byte("null\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice")
	if m.running("alice/lead") != nil {
		t.Fatal("null continuation record treated as valid unpaused state and started lead")
	}
}
