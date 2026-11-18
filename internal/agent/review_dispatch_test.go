package agent

import (
	"context"
	"github.com/idy/yesman/internal/yesmen"
	"testing"
)

func TestReviewR2IdleAbortAfterReservationBeforeSend(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	lead := "alice/lead"
	if _, _, err := m.ensure(lead); err != nil {
		t.Fatal(err)
	}
	addTasks(t, s, yesmen.TaskEdit{Title: str("Orphan")})
	m.reviewBeforeTaskSend = func() {
		if err := m.AbortByUser(context.Background(), lead); err != nil {
			t.Error(err)
		}
	}
	m.continueTasks("alice")
	c, _ := s.Continuation("alice")
	if c.LeadPausedAt.IsZero() {
		t.Fatal("fixture Abort not durable")
	}
	if got := noticesOf(prompts(lead)); len(got) > 0 {
		t.Fatalf("idle-path notice dispatched after AbortByUser returned with persistent pause: %q", got)
	}
}
func TestReviewR2StopCancelsAutomaticStartup(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	lead := "alice/lead"
	addTasks(t, s, yesmen.TaskEdit{Title: str("Orphan")})
	entered, done := make(chan struct{}), make(chan struct{})
	m.cfg.Command = func(ctx context.Context) (string, []string, error) {
		close(entered)
		<-ctx.Done()
		return "", nil, ctx.Err()
	}
	go func() { m.continueTasks("alice"); close(done) }()
	<-entered
	m.StopByUser(lead)
	<-done
	if got := noticesOf(prompts(lead)); len(got) > 0 || m.running(lead) != nil {
		t.Fatalf("startup Stop admitted a prompt: %q", got)
	}
}
