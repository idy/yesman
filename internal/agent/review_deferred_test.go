package agent

import (
	"context"
	"github.com/idy/yesman/internal/yesmen"
	"strings"
	"testing"
	"time"
)

func reviewWaitDeferred(t *testing.T, m *Manager, n int) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		m.mu.Lock()
		p := m.leadPauses["alice"]
		ok := p != nil && len(p.deferred) == n
		m.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("deferred count not reached")
}

func TestReviewR2DeferredReportUserOrderAndGoal(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	lead := "alice/lead"
	addTasks(t, s, yesmen.TaskEdit{Title: str("Unfinished goal"), Status: str("doing"), Owner: str("w1")})
	m.StopByUser(lead)
	if _, err := m.StartWorker(context.Background(), "alice", "w1", "Work", "a task"); err != nil {
		t.Fatal(err)
	}
	reviewWaitDeferred(t, m, 1)
	ws, _ := s.Workers("alice")
	if !ws[0].FinishedAt.IsZero() || m.running(lead) != nil {
		t.Fatal("undelivered report completed worker/woke lead")
	}
	y, _ := s.Get("alice")
	if err := m.TellLeadAboutUser(context.Background(), "alice", y.HiredAt, "[yesman] user action needs verification"); err != nil {
		t.Fatal(err)
	}
	reviewWaitDeferred(t, m, 2)
	if err := m.Prompt(context.Background(), lead, "explicit resume first"); err != nil {
		t.Fatal(err)
	}
	waitFinished(t, m, "alice/w1", time.Time{})
	waitPrompt(t, prompts, lead, "[yesman] user action needs verification")
	got := prompts(lead)
	if len(got) < 3 || got[0] != "explicit resume first" {
		t.Fatalf("deferred got ahead of user: %q", got)
	}
	b, _ := s.Board("alice")
	if b.Tasks[0].Status != yesmen.TaskDoing {
		t.Fatal("report completed user goal")
	}
	t.Log("paused actual worker report retained; user message first; report delivered then worker closed; goal still doing")
}

func TestReviewR2DeferredResponsibilityAcrossRestart(t *testing.T) {
	m, s := newTestManager(t, 0)
	lead := "alice/lead"
	addTasks(t, s, yesmen.TaskEdit{Title: str("Unfinished goal"), Status: str("doing"), Owner: str("w1")})
	m.StopByUser(lead)
	if _, err := m.StartWorker(context.Background(), "alice", "w1", "Work", "a task"); err != nil {
		t.Fatal(err)
	}
	reviewWaitDeferred(t, m, 1)
	m.StopAll()
	again := New(m.cfg)
	t.Cleanup(again.StopAll)
	prompts := promptsTo(again)
	again.Resume()
	time.Sleep(50 * time.Millisecond)
	if again.running(lead) != nil {
		t.Fatal("restart resumed user pause")
	}
	ws, _ := s.Workers("alice")
	if !ws[0].FinishedAt.IsZero() {
		t.Fatal("lost deferred report marked delivered")
	}
	if err := again.Prompt(context.Background(), lead, "resume after restart"); err != nil {
		t.Fatal(err)
	}
	waitNoticeListing(t, prompts, lead, "t1")
	t.Log("restart retains paused state and open responsibility; full deferred report text is memory-only, not recovered by this path")
}

func TestReviewR2DeferredLimit(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	lead := "alice/lead"
	y, _ := s.Get("alice")
	m.StopByUser(lead)
	for i := 0; i < 65; i++ {
		msg := "[yesman] kept"
		if i == 0 {
			msg = "[yesman] oldest"
		}
		if err := m.TellLeadAboutUser(context.Background(), "alice", y.HiredAt, msg); err != nil {
			t.Fatal(err)
		}
	}
	reviewWaitDeferred(t, m, 64)
	if m.running(lead) != nil {
		t.Fatal("bounded deferral woke paused lead")
	}
	if err := m.Prompt(context.Background(), lead, "user before flush"); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(3 * time.Second)
	for len(prompts(lead)) < 65 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	got := prompts(lead)
	if len(got) != 65 || got[0] != "user before flush" {
		t.Fatalf("got %d inputs, expected user +64", len(got))
	}
	for _, x := range got {
		if strings.Contains(x, "oldest") {
			t.Fatal("oldest not evicted")
		}
	}
}

func TestReviewR2OldDeferredReportCannotCloseNewUnreportedRun(t *testing.T) {
	m, s := newTestManager(t, 0, "FAKE_PI_HOLD_REPORTS=1")
	prompts := promptsTo(m)
	waiting := eventsOf(m, "test_report_waiting")
	lead := "alice/lead"
	wid := "alice/w1"
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", Task: "first task", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	a, _, err := m.ensure(wid)
	if err != nil {
		t.Fatal(err)
	}
	m.StopByUser(lead)
	oldmsg := "[yesman] Worker w1 finished its OLD run"
	if err := m.tellLeadWithin(context.Background(), a, "report", oldmsg); err != errLeadPaused {
		t.Fatalf("old report did not defer: %v", err)
	}
	// New user/lead work reaches the real worker, settles, and has its final text
	// read held in the fake transport. That newer report has NOT reached lead.
	if err := m.Prompt(context.Background(), wid, "new work after old report"); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(3 * time.Second)
	for len(waiting(wid)) == 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if len(waiting(wid)) == 0 {
		t.Fatal("new run never reached actual held get_last_assistant_text")
	}
	if err := m.Prompt(context.Background(), lead, "resume lead"); err != nil {
		t.Fatal(err)
	}
	waitPrompt(t, prompts, lead, oldmsg)
	lp := m.running(lead)
	_, _ = lp.call(context.Background(), map[string]any{"type": "test_release_prompts"})
	time.Sleep(150 * time.Millisecond)
	info, _ := m.Get(wid)
	if !info.FinishedAt.IsZero() || m.running(wid) == nil {
		t.Fatalf("old deferred report finished/closed newer unreported run: status=%s finishedAt=%s", info.Status, info.FinishedAt)
	}
	wp := m.running(wid)
	_, _ = wp.call(context.Background(), map[string]any{"type": "test_release_reports"})
}
