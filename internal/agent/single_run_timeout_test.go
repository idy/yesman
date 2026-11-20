package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
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
	finished.proc, finished.reports, finished.handoff.state = nil, 1, restart.ReportAccepted
	report := add("handoff", Idle)
	report.reports, report.handoff.state = 1, restart.ReportAccepted
	report.handoff.digest = "should never be copied"
	failed := add("failed-notice", Failed)
	failed.proc, failed.reports = nil, 1
	unknown := add("retry", Idle)
	unknown.handoff.state = restart.ReportNotSent // a prior attempt could have arrived
	oldHire := add("rehired", Waiting)
	oldHire.retiring = true
	rehired := add("rehired", Working)
	rehired.info.Since = since.Add(time.Minute)
	overlap := add("overlap", Idle)
	overlap.reports, overlap.handoff.state = 2, restart.ReportAccepted
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
		{ID: "team/retry", Role: RoleWorker, Since: since, Phase: "reporting", ReportStatus: "unknown"},
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
