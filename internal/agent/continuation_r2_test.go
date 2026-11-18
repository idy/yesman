package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// The t159 independent review of 9ccd5d9 found these six with real Manager,
// fake pi and Store probes; assertions are kept as the reviewer wrote them.

// Every fixture uses newTestManager's disposable Store and fake-pi test binary.
func TestR2AsleepRefusalRetries(t *testing.T) {
	old := noticeRetries
	noticeRetries = []time.Duration{20 * time.Millisecond, 20 * time.Millisecond}
	defer func() { noticeRetries = old }()
	m, s := newTestManager(t, 0, "FAKE_PI_FAIL_PROMPTS=1")
	addTasks(t, s, yesmen.TaskEdit{Title: str("Orphan")})
	m.continueTasks("alice") // deliberately do NOT pre-start the lead
	time.Sleep(250 * time.Millisecond)
	c, err := s.Continuation("alice")
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	a := m.agents["alice/lead"]
	t.Logf("inbox=%d settled=%d prompts=%d status=%s", a.inbox, a.settledInbox, a.prompts, a.info.Status)
	m.mu.Unlock()
	if c.Tasks["t1"].Count != 3 || c.Tasks["t1"].Delivered {
		t.Fatalf("refused startup notice must exhaust two retries: got %+v", c)
	}
}

func TestR2StopAndAbortDuringNotice(t *testing.T) {
	for _, kind := range []string{"stop-lead", "abort-lead", "stop-worker"} {
		t.Run(kind, func(t *testing.T) {
			m, s := newTestManager(t, 0, "FAKE_PI_HOLD_PROMPTS=1")
			prompts := promptsTo(m)
			lead := "alice/lead"
			_, p, err := m.ensure(lead)
			if err != nil {
				t.Fatal(err)
			}
			target := lead
			if kind == "stop-worker" {
				target = "alice/w1"
				if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", Task: "fixture", StartedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
				if _, _, err := m.ensure(target); err != nil {
					t.Fatal(err)
				}
			}
			addTasks(t, s, yesmen.TaskEdit{Title: str("Orphan")})
			noticeDone := make(chan struct{})
			go func() { m.continueTasks("alice"); close(noticeDone) }()
			waitNoticeListing(t, prompts, lead, "t1") // real prompt-received event, acknowledgement held
			done := make(chan struct{})
			go func() {
				if kind == "abort-lead" {
					_ = m.AbortByUser(context.Background(), target)
				} else {
					m.StopByUser(target)
				}
				close(done)
			}()
			blocked := false
			select {
			case <-done:
			case <-time.After(250 * time.Millisecond):
				blocked = true
			}
			// Release this synthetic RPC to leave no hung process or goroutine.
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, _ = p.call(ctx, map[string]any{"type": "test_release_prompts"})
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("cleanup stop did not return")
			}
			select {
			case <-noticeDone:
			case <-time.After(3 * time.Second):
				t.Fatal("cleanup notice did not return")
			}
			if blocked {
				t.Fatal("user action blocked behind unrelated notice acknowledgement for >250ms; released only by accepting notice")
			}
		})
	}
}

func TestR2WorkerReportRespectsLeadPause(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	lead := "alice/lead"
	m.StopByUser(lead)
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", Task: "fixture", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	a, _, err := m.ensure("alice/w1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = m.tellLeadWithin(ctx, a, "report", "[yesman] Worker report fixture")
	c, _ := s.Continuation("alice")
	if c.LeadPausedAt.IsZero() {
		t.Fatal("fixture pause lost")
	}
	if m.running(lead) != nil || len(prompts(lead)) > 0 {
		t.Fatalf("automatic report woke persistently paused lead: err=%v prompts=%q pause=%s", err, prompts(lead), c.LeadPausedAt)
	}
}

func TestR2SnapshotMustNotOverrideNewWait(t *testing.T) {
	for _, kind := range []string{yesmen.WaitPaused, yesmen.WaitTasks, yesmen.WaitUser} {
		t.Run(kind, func(t *testing.T) {
			m, s := newTestManager(t, 0)
			prompts := promptsTo(m)
			addTasks(t, s, yesmen.TaskEdit{Title: str("Target")}, yesmen.TaskEdit{Title: str("Unfinished predecessor"), Wait: &yesmen.WaitEdit{Kind: yesmen.WaitExternal, Reason: "CI"}})
			changed := false
			m.beforeNotice = func() {
				if changed {
					return
				}
				changed = true
				w := &yesmen.WaitEdit{Kind: kind, Reason: "User said stop"}
				if kind == yesmen.WaitTasks {
					w.Tasks = []string{"t2"}
				}
				if kind == yesmen.WaitUser {
					h, _, e := s.AddHandoff("alice", yesmen.Handoff{Type: yesmen.HandoffInstall, Title: "User must install", Task: "t1"})
					if e != nil {
						t.Error(e)
					}
					w.Handoff = h.ID
				}
				if _, err := s.ChangeBoard("alice", yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Wait: w}}}); err != nil {
					t.Error(err)
				}
			}
			m.continueTasks("alice")
			if got := noticesOf(prompts("alice/lead")); len(got) > 0 {
				t.Fatalf("outdated actionable notice sent after %s wait committed: %q", kind, got)
			}
		})
	}
}

func TestR2PauseWriteFailureMustRemainStopped(t *testing.T) {
	m, s := newTestManager(t, 0)
	lead := "alice/lead"
	y, _ := s.Get("alice")
	addTasks(t, s, yesmen.TaskEdit{Title: str("Orphan")})
	// A directory at the file path deterministically rejects persistence.
	if err := os.Mkdir(y.ContinuationFile(), 0700); err != nil {
		t.Fatal(err)
	}
	m.StopByUser(lead)
	if err := os.Remove(y.ContinuationFile()); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice")
	if m.running(lead) != nil {
		t.Fatal("Stop silently lost its pause on I/O failure; automatic continuation restarted lead once storage recovered")
	}
}

func TestR2FailedHandoffReadIsNotReady(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	addTasks(t, s, yesmen.TaskEdit{Title: str("Install"), Wait: &yesmen.WaitEdit{Kind: yesmen.WaitUser}})
	y, _ := s.Get("alice")
	if err := os.WriteFile(y.MetaDir()+"/needs-you.json", []byte("{invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice")
	if got := noticesOf(prompts("alice/lead")); len(got) > 0 && strings.Contains(got[0], "no Needs you request") {
		t.Fatalf("unreadable request storage classified as no open request: %q", got)
	}
}
