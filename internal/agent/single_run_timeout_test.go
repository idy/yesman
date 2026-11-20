package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/restart"
	"github.com/idy/yesman/internal/yesmen"
)

func TestRunOnceTimeoutSnapshotsTeam(t *testing.T) {
	for _, phase := range []string{"working", "reporting"} {
		t.Run(phase, func(t *testing.T) {
			m, store := newTestManager(t, 0, "FAKE_PI_HOLD_REPORTS=1")
			y, err := store.Get("alice")
			if err != nil {
				t.Fatal(err)
			}
			leadSettled, reportWaiting := make(chan struct{}, 1), make(chan struct{}, 1)
			m.cfg.OnEvent = func(e Event) {
				var signal chan struct{}
				switch {
				case e.Agent == "alice/lead" && strings.Contains(string(e.Data), `"type":"agent_settled"`):
					signal = leadSettled
				case e.Agent == "alice/helper" && strings.Contains(string(e.Data), `"type":"test_report_waiting"`):
					signal = reportWaiting
				default:
					return
				}
				select {
				case signal <- struct{}{}:
				default:
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			done := make(chan RunResult, 1)
			go func() { done <- m.RunOnce(ctx, y, "initial message") }()
			wait := func(signal <-chan struct{}, event string) {
				t.Helper()
				select {
				case <-signal:
				case <-ctx.Done():
					t.Fatalf("deadline before %s", event)
				}
			}
			wait(leadSettled, "Lead settlement")
			task, reportStatus := "test-run:retry", ""
			if phase == "reporting" {
				task, reportStatus = "write a draft", "unknown"
			}
			w, err := m.StartWorker(ctx, y.Name, "helper", "Task", task)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "reporting" {
				// Hold the report read while the Lead stays idle. Shutdown
				// stops the read before the run returns its result.
				wait(reportWaiting, "worker report read")
			}
			out := <-done
			want := []RunInterruptedAgent{{ID: w.ID, Role: RoleWorker, Since: w.Since.UTC(), Phase: phase, ReportStatus: reportStatus}}
			if out.Status != "timed_out" || !slices.Equal(out.InterruptedAgents, want) {
				t.Fatalf("result = %+v, want timed_out and %+v", out, want)
			}
		})
	}
}

func TestRunOnceTimeoutOmitsExhaustedReport(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_FAIL_REPORTS=1")
	y, err := store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	backoff := reportBackoff
	reportBackoff = time.Millisecond
	defer func() { reportBackoff = backoff }()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan RunResult, 1)
	go func() { done <- m.RunOnce(ctx, y, "initial message") }()
	waitRunLeadIdle(t, m)
	live, err := m.StartWorker(ctx, y.Name, "live", "Still running", "test-run:retry")
	if err != nil {
		t.Fatal(err)
	}
	exhausted, err := m.StartWorker(ctx, y.Name, "exhausted", "Report rejected", "finish the task")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "all attempts to be rejected and the reservation to end", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		a := m.agents[exhausted.ID]
		return a.info.Status == Idle && a.reports == 0 && a.handoff.state == restart.ReportNotSent &&
			m.run.handoffErr != nil && strings.Contains(m.run.handoffErr.Error(), "rejected")
	})
	out := <-done
	want := []RunInterruptedAgent{{ID: live.ID, Role: RoleWorker, Since: live.Since.UTC(), Phase: "working"}}
	if out.Status != "timed_out" || !slices.Equal(out.InterruptedAgents, want) {
		t.Fatalf("result = %+v, want timed_out and %+v", out, want)
	}
}

func TestRunOnceTimeoutReportReceiptAssociation(t *testing.T) {
	for _, tt := range []struct {
		name        string
		overlap     bool
		drainOlder  bool
		wantReports int
		wantStatus  string
	}{
		{name: "one accepted", wantReports: 1, wantStatus: "accepted"},
		{name: "newer accepted while older still pending", overlap: true, wantReports: 2, wantStatus: "unknown"},
		{name: "older finishes before newer", overlap: true, drainOlder: true, wantReports: 1, wantStatus: "accepted"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, store := newTestManager(t, 0, "FAKE_PI_HOLD_REPORTS=1")
			y, err := store.Get("alice")
			if err != nil {
				t.Fatal(err)
			}
			const id = "alice/helper"
			reads := make(chan struct{}, 2)
			var prompts atomic.Int32
			m.cfg.OnEvent = func(e Event) {
				switch {
				case e.Agent == id && strings.Contains(string(e.Data), `"type":"test_report_waiting"`):
					reads <- struct{}{}
				case e.Agent == "alice/lead" && strings.Contains(string(e.Data), `"type":"test_prompt_received"`) && strings.Contains(string(e.Data), "finished a run"):
					prompts.Add(1)
				}
			}
			var holdNext atomic.Bool
			m.beforePromptDone = func(a *agent) {
				if a.info.ID == "alice/lead" && holdNext.CompareAndSwap(true, false) {
					<-m.shutdown // keep the acknowledged handoff in flight at the deadline
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			done := make(chan RunResult, 1)
			go func() { done <- m.RunOnce(ctx, y, "initial message") }()
			waitRunLeadIdle(t, m)
			w, err := m.StartWorker(ctx, y.Name, "helper", "Task", "first task")
			if err != nil {
				t.Fatal(err)
			}
			waitRead := func() {
				t.Helper()
				select {
				case <-reads:
				case <-ctx.Done():
					t.Fatal("Pi did not reach the held report read")
				}
			}
			waitRead()
			if tt.overlap {
				if err := m.PromptFromLead(ctx, id, "second task"); err != nil {
					t.Fatal(err)
				}
				waitRead()
			}
			worker, lead := m.running(id), m.running("alice/lead")
			call := func(p *proc, typ string) {
				t.Helper()
				if _, err := p.call(ctx, map[string]any{"type": typ}); err != nil {
					t.Fatal(err)
				}
			}
			call(worker, "test_release_latest_report")
			eventually(t, "the latest report to reach Pi", func() bool { return prompts.Load() == 1 })
			holdNext.Store(true)
			call(lead, "test_release_prompts")
			if tt.drainOlder {
				eventually(t, "the newest report to reach its gate", func() bool { return !holdNext.Load() })
				call(worker, "test_release_one_report")
				eventually(t, "the older report to reach Pi", func() bool { return prompts.Load() == 2 })
				call(lead, "test_release_prompts")
			}
			eventually(t, "Pi's acknowledgement with the expected outstanding report count", func() bool {
				m.mu.Lock()
				defer m.mu.Unlock()
				a := m.agents[id]
				return !holdNext.Load() && a.info.Status == Idle && a.reports == tt.wantReports && a.handoff.state == restart.ReportAccepted
			})
			out := <-done
			want := RunInterruptedAgent{ID: id, Role: RoleWorker, Since: w.Since.UTC(), Phase: "reporting", ReportStatus: tt.wantStatus}
			if out.Status != "timed_out" || !slices.Contains(out.InterruptedAgents, want) {
				t.Fatalf("result = %+v, want timed_out and worker %+v", out, want)
			}
		})
	}
}

func waitRunLeadIdle(t *testing.T, m *Manager) {
	t.Helper()
	eventually(t, "the initial Lead turn to settle", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		a := m.agents["alice/lead"]
		return a != nil && a.info.Status == Idle && a.settled > 0 && a.prompts == 0
	})
}

func TestRunOnceTimeoutWhileStarting(t *testing.T) {
	m, store := newTestManager(t, 0)
	y, err := store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	m.cfg.Command = func(context.Context) (string, []string, error) {
		<-m.shutdown // hold startup through the instant RunOnce closes admission
		return "", nil, errStopped
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	out := m.RunOnce(ctx, y, "start")
	want := []RunInterruptedAgent{{ID: "alice/lead", Role: RoleLead, Since: y.HiredAt.UTC(), Phase: "starting"}}
	if out.Status != "timed_out" || !slices.Equal(out.InterruptedAgents, want) {
		t.Fatalf("result = %+v, want timed_out and %+v", out, want)
	}
}

func TestInterruptedRunAgentsObservesCurrentGenerationAndReportReceipts(t *testing.T) {
	since := time.Now().UTC()
	y := yesmen.Yesman{Name: "team", HiredAt: since}
	m := &Manager{agents: map[string]*agent{}}
	add := func(name string, status Status) *agent {
		id := ID(y.Name, name)
		a := &agent{info: Info{ID: id, Yesman: y.Name, Since: since, Role: RoleWorker, Status: status}, workspaceSince: since, proc: &proc{}}
		m.agents[id] = a
		return a
	}
	add("lead", Idle).info.Role = RoleLead
	add("old-hire", Working).workspaceSince = since.Add(-time.Hour)
	add("retired", Working).retiring = true
	add("removing", Working).removing = true
	add("elsewhere", Working).info.Yesman = "elsewhere"
	add("queued", Idle).inbox = 1 // input admitted before agent_start
	add("already-stopping", Working).stopping = true
	finished := add("finished", Stopped)
	finished.proc, finished.reports = nil, 1
	finished.handoff = reportHandoff{run: 1, state: restart.ReportAccepted, active: true}
	report := add("handoff", Idle)
	report.reports = 1
	report.handoff = reportHandoff{run: 1, state: restart.ReportAccepted, active: true, digest: "should never be copied"}
	failed := add("failed-notice", Failed)
	failed.proc, failed.reports = nil, 1
	exhausted := add("exhausted", Idle)
	exhausted.handoff.state = restart.ReportNotSent
	stale := add("stale-receipt", Idle)
	stale.reports = 1 // an older report or a notice is the only reservation left
	stale.handoff = reportHandoff{run: 2, state: restart.ReportAccepted}
	oldHire := add("rehired", Waiting)
	oldHire.retiring = true
	rehired := add("rehired", Working)
	rehired.info.Since = since.Add(time.Minute)
	overlap := add("overlap", Idle)
	overlap.reports = 2
	overlap.handoff = reportHandoff{run: 2, state: restart.ReportAccepted, active: true}
	waiting := add("waiting", Waiting)
	waiting.info.Dialog = json.RawMessage(`{"id":"q","method":"confirm","message":"should never be copied"}`)
	user := add("user", Waiting)
	user.info.Dialog, user.handedOver = waiting.info.Dialog, "q"
	want := []RunInterruptedAgent{
		{ID: "team/failed-notice", Role: RoleWorker, Since: since, Phase: "reporting", ReportStatus: "unknown"},
		{ID: "team/handoff", Role: RoleWorker, Since: since, Phase: "reporting", ReportStatus: "accepted"},
		{ID: "team/overlap", Role: RoleWorker, Since: since, Phase: "reporting", ReportStatus: "unknown"},
		{ID: "team/queued", Role: RoleWorker, Since: since, Phase: "input_pending"},
		{ID: "team/rehired", Role: RoleWorker, Since: rehired.info.Since, Phase: "working"},
		{ID: "team/stale-receipt", Role: RoleWorker, Since: since, Phase: "reporting", ReportStatus: "unknown"},
		{ID: "team/user", Role: RoleWorker, Since: since, Phase: "waiting", WaitingOn: "user"},
		{ID: "team/waiting", Role: RoleWorker, Since: since, Phase: "waiting", WaitingOn: "lead"},
	}
	m.mu.Lock()
	got := m.interruptedRunAgentsLocked(y)
	report.handoff = reportHandoff{}
	waiting.info.Dialog = nil
	m.mu.Unlock()
	if !slices.Equal(got, want) {
		t.Fatalf("snapshot = %+v, want %+v", got, want)
	}
	b, err := json.Marshal(got)
	if err != nil || strings.Contains(string(b), "should never be copied") {
		t.Fatalf("snapshot JSON: %s (%v)", b, err)
	}
}
