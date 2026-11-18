package agent

import (
	"context"
	"os"
	"testing"

	"github.com/idy/yesman/internal/yesmen"
)

func TestReviewR3ActualOldSendCannotEraseRehirePause(t *testing.T) {
	m, s := newTestManager(t, 0)
	old, _ := s.Get("alice")
	_, p, err := m.ensure("alice/lead")
	if err != nil {
		t.Fatal(err)
	}
	m.loadPause(old)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	p.reviewBeforeAdmit = func() { close(entered); <-release }
	go func() {
		done <- m.TellLeadAboutUser(context.Background(), "alice", old.HiredAt, "[yesman] old scope verification")
	}()
	<-entered
	if _, err := s.Archive("alice", func(y yesmen.Yesman) error { return m.StopYesman(y.Name) }); err != nil {
		t.Fatal(err)
	}
	m.ForgetYesman("alice", old.HiredAt)
	if _, err := s.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	now, _ := s.Get("alice")
	addTasks(t, s, yesmen.TaskEdit{Title: str("New paused goal")})
	if err := os.Mkdir(now.ContinuationFile(), 0700); err != nil {
		t.Fatal(err)
	}
	m.StopByUser("alice/lead")
	if err := os.Remove(now.ContinuationFile()); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Logf("old send result=%v", err)
	}
	m.mu.Lock()
	lp := m.leadPauses["alice"]
	same := lp.hiredAt.Equal(now.HiredAt) && lp.paused
	m.mu.Unlock()
	if !same {
		t.Error("actual old proc send overwrote replacement hire's pause")
	}
	m.continueTasks("alice")
	if m.running("alice/lead") != nil {
		t.Fatal("new paused hire auto-started after old send callback returned")
	}
}
