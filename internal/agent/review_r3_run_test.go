package agent

import (
	"context"
	"github.com/idy/yesman/internal/yesmen"
	"testing"
	"time"
)

func TestReviewR3RegisteredNextInputCannotBeOldRun(t *testing.T) {
	m, s := newTestManager(t, 0, "FAKE_PI_SLOW_RUNS=1", "FAKE_PI_HOLD_REPORTS=1")
	wid := "alice/w1"
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	a, p, err := m.ensure(wid)
	if err != nil {
		t.Fatal(err)
	}
	prompts := promptsTo(m)
	waiting := eventsOf(m, "test_report_waiting")
	entered, release := make(chan struct{}), make(chan struct{})
	second := make(chan error, 1)
	p.reviewBeforeWrite = func(v any) {
		if cmd, ok := v.(map[string]any); ok && cmd["message"] == "next input" {
			close(entered)
			<-release
		}
	}
	if err := m.Prompt(context.Background(), wid, "first input"); err != nil {
		t.Fatal(err)
	}
	go func() { second <- m.Prompt(context.Background(), wid, "next input") }()
	<-entered
	until := time.Now().Add(time.Second)
	for len(waiting(wid)) < 1 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if len(waiting(wid)) < 1 {
		close(release)
		t.Fatal("first report not held")
	}
	m.mu.Lock()
	t.Logf("old run settledInbox=%d registered inbox=%d prompts=%d", a.settledInbox, a.inbox, a.prompts)
	m.mu.Unlock()
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	until = time.Now().Add(time.Second)
	for len(waiting(wid)) < 2 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if len(waiting(wid)) < 2 {
		t.Fatal("new report not held")
	}
	if _, err := p.call(context.Background(), map[string]any{"type": "test_release_one_report"}); err != nil {
		t.Fatal(err)
	}
	waitCount(t, prompts, "alice/lead", 1)
	// Lead held the first report's ACK; accepting just that report must not finish
	// the second run whose get_last_assistant_text is still held in worker pi.
	lp := m.running("alice/lead")
	if _, err := lp.call(context.Background(), map[string]any{"type": "test_release_prompts"}); err != nil {
		t.Fatal(err)
	}
	until = time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(until) {
		i, _ := m.Get(wid)
		if !i.FinishedAt.IsZero() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	i, _ := m.Get(wid)
	if !i.FinishedAt.IsZero() || m.running(wid) == nil {
		t.Fatalf("first report closed next run: status=%s finishedAt=%v; second report still unread", i.Status, i.FinishedAt)
	}
}
