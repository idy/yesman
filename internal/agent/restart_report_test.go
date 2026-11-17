package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/restart"
	"github.com/idy/yesman/internal/yesmen"
)

// restartInstanceEnv is restartInstance with the fake pi given env too.
func restartInstanceEnv(t *testing.T, store *yesmen.Store, tmp string, env ...string) (*Manager, *received) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := &received{}
	m := New(Config{
		Pi: exe, Extension: filepath.Join(tmp, "none.ts"),
		Env:    append([]string{"YESMAN_FAKE_PI=1", "FAKE_PI_DELAY=0s"}, env...),
		Yesmen: store, OnEvent: r.on,
	})
	t.Cleanup(m.StopAll)
	return m, r
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// reports are the worker reports among the prompts to the lead.
func (r *received) reports(lead, worker string) []string {
	var out []string
	for _, p := range r.to(lead) {
		if strings.HasPrefix(p, `[yesman] Worker "`+worker+`" finished a run.`) {
			out = append(out, p)
		}
	}
	return out
}

func finishedAt(t *testing.T, store *yesmen.Store, name string) time.Time {
	t.Helper()
	ws, err := store.Workers("alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.Name == name {
			return w.FinishedAt
		}
	}
	t.Fatalf("no worker %s", name)
	return time.Time{}
}

var (
	runStart   = json.RawMessage(`{"type":"agent_start"}`)
	runSettled = json.RawMessage(`{"type":"agent_settled","aborted":false}`)
)

// The model lock is held past the resume's time, after its start: the
// expired resume is admitted nowhere and sends nothing, then or later.
func TestResumeExpiredBeforeAdmissionSendsNothing(t *testing.T) {
	store, tmp, y := restartStore(t)
	m, got := restartInstance(t, store, tmp)
	id := ID("alice", "lead")
	m.mu.Lock()
	a, err := m.workspaceAgentLocked(y, Info{ID: id, Yesman: y.Name, Name: "lead", Role: RoleLead, Since: y.HiredAt})
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	a.modelMu.Lock() // an ordinary model operation, still under way
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan string, 1)
	lead := restart.Agent{Yesman: y.Name, Name: "lead", Role: RoleLead, WorkspaceHiredAt: y.HiredAt, AgentSince: y.HiredAt, Reason: restart.ReasonWorking}
	go func() { o, _ := m.Resume(ctx, lead, "expired note"); done <- o }()
	eventually(t, "the start", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return a.proc != nil && a.info.Status == Idle })
	var outcome string
	select {
	case outcome = <-done: // Resume gives up with its time, not with the lock
	case <-time.After(3 * time.Second):
		t.Fatal("Resume waited for the lock past its time")
	}
	a.modelMu.Unlock()
	if outcome != restart.Failed {
		t.Fatalf("Resume = %s", outcome)
	}
	// A late callback would dispatch a moment after the lock is free.
	time.Sleep(300 * time.Millisecond)
	m.mu.Lock()
	prompts := a.prompts
	m.mu.Unlock()
	if n := got.notes(id, "expired note"); n != 0 || prompts != 0 {
		t.Fatalf("expired resume dispatched %d note(s), %d registered", n, prompts)
	}
	// The agent is left as any idle agent: a message still goes to it.
	if err := m.Prompt(context.Background(), id, "user later"); err != nil {
		t.Fatal(err)
	}
	if p := got.to(id); len(p) != 1 || p[0] != "user later" {
		t.Fatalf("lead got %q", p)
	}
}

// A start that outlasts the time is cut off and joined before Resume
// returns: its pi is stopped (within the 2 s a failed start allows it to
// exit), not left starting for the 10 s it would take.
func TestResumeCutsOffItsOwnStart(t *testing.T) {
	store, tmp, y := restartStore(t)
	m, got := restartInstanceEnv(t, store, tmp, "FAKE_PI_DELAY=10s")
	id := ID("alice", "lead")
	lead := restart.Agent{Yesman: y.Name, Name: "lead", Role: RoleLead, WorkspaceHiredAt: y.HiredAt, AgentSince: y.HiredAt, Reason: restart.ReasonWorking}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	began := time.Now()
	outcome, err := m.Resume(ctx, lead, "note")
	if outcome != restart.Failed || err == nil {
		t.Fatalf("Resume = %s %v", outcome, err)
	}
	if d := time.Since(began); d > 4*time.Second {
		t.Fatalf("Resume returned after %s, after the start it should have cut off", d)
	}
	m.mu.Lock()
	a := m.agents[id]
	status, proc, starting := a.info.Status, a.proc, a.starting
	m.mu.Unlock()
	if proc != nil || starting != nil || status != Stopped {
		t.Fatalf("left %v running=%v starting=%v", status, proc != nil, starting != nil)
	}
	if n := got.notes(id, "note"); n != 0 {
		t.Fatal("a cut-off resume sent its note")
	}
}

// A worker whose run settles after the snapshot, while the intent is
// written: the shutdown stops its report, the reconciled record lists the
// report as not sent with the digest of its final message, and the next
// instance hands that report on with no new worker run.
func TestLateSettleHandedOnWithoutNewRun(t *testing.T) {
	store, tmp, _ := restartStore(t)
	addWorker(t, store, "worker")
	old, gotOld := restartInstance(t, store, tmp)
	id, lead := ID("alice", "worker"), ID("alice", "lead")
	a, p, err := old.ensure(id)
	if err != nil {
		t.Fatal(err)
	}
	old.record(a, p, "agent_start", runStart)
	entries := old.CloseForRestart()
	if len(entries) != 1 || entries[0].Reason != restart.ReasonWorking {
		t.Fatalf("snapshot %+v", entries)
	}
	old.record(a, p, "agent_settled", runSettled) // during the write
	eventually(t, "its final message read", func() bool { old.mu.Lock(); defer old.mu.Unlock(); return a.handoff.digest != "" })
	old.StopAll()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entries = old.ReconcileRestart(ctx, entries)
	if e := entries[0]; e.Reason != restart.ReasonReport || e.Report == nil ||
		*e.Report != (restart.Report{State: restart.ReportNotSent, Digest: digest("done")}) {
		t.Fatalf("reconciled %+v %+v", e, e.Report)
	}
	if n := len(gotOld.reports(lead, "worker")); n != 0 {
		t.Fatalf("the old instance sent the report %d times after closing", n)
	}
	in := restart.Intent{Version: restart.Version, ID: restart.NewID(), Kind: restart.KindRestart, CreatedAt: time.Now(), Agents: entries}
	if err := in.Validate(time.Now()); err != nil {
		t.Fatal(err)
	}

	next, got := restartInstance(t, store, tmp)
	outcome, err := next.Resume(context.Background(), entries[0], "must not run")
	if outcome != restart.Reported || err != nil {
		t.Fatalf("Resume = %s %v", outcome, err)
	}
	if p := got.to(id); len(p) != 0 {
		t.Fatalf("the worker was given a new run: %q", p)
	}
	r := got.reports(lead, "worker")
	if len(r) != 1 || !strings.Contains(r[0], "Handed on after a restart") || !strings.HasSuffix(r[0], "done") {
		t.Fatalf("lead got %q", r)
	}
	eventually(t, "the worker finished", func() bool { return !finishedAt(t, store, "worker").IsZero() })
}

// The lead's pi took the report before the restart, but the worker was
// not yet recorded finished: the report is not sent again, and the worker
// is recorded finished, as it would have been.
func TestAcceptedReportNotSentAgain(t *testing.T) {
	store, tmp, _ := restartStore(t)
	addWorker(t, store, "worker")
	old, gotOld := restartInstance(t, store, tmp)
	id, lead := ID("alice", "worker"), ID("alice", "lead")
	a, p, err := old.ensure(id)
	if err != nil {
		t.Fatal(err)
	}
	a.modelMu.Lock() // closing it as finished waits behind this
	old.record(a, p, "agent_start", runStart)
	old.record(a, p, "agent_settled", runSettled)
	eventually(t, "the lead's pi taking the report", func() bool {
		old.mu.Lock()
		defer old.mu.Unlock()
		return a.handoff.state == restart.ReportAccepted
	})
	entries := old.CloseForRestart()
	old.StopAll()
	a.modelMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entries = old.ReconcileRestart(ctx, entries)
	var entry restart.Agent
	for _, e := range entries {
		if e.Name == "worker" {
			entry = e
		}
	}
	if entry.Reason != restart.ReasonReport || entry.Report.State != restart.ReportAccepted {
		t.Fatalf("listed %+v", entries)
	}
	if !finishedAt(t, store, "worker").IsZero() {
		t.Fatal("fixture: the old instance recorded it finished")
	}

	next, got := restartInstance(t, store, tmp)
	outcome, err := next.Resume(context.Background(), entry, "must not run")
	if outcome != restart.Accepted || err == nil || !strings.Contains(err.Error(), "not sent again") {
		t.Fatalf("Resume = %s %v", outcome, err)
	}
	if n := len(gotOld.reports(lead, "worker")) + len(got.reports(lead, "worker")); n != 1 {
		t.Fatalf("the report was sent %d times across the restart", n)
	}
	if len(got.to(id)) != 0 || next.running(id) != nil {
		t.Fatal("the worker was started or given a run")
	}
	if finishedAt(t, store, "worker").IsZero() {
		t.Fatal("the worker was not recorded finished")
	}
}

// The report was sent and the lead's pi exited before answering: it may
// or may not have arrived. It is not sent again, nor the worker recorded
// finished; the outcome says so.
func TestUncertainReportNotSentAgain(t *testing.T) {
	store, tmp, _ := restartStore(t)
	addWorker(t, store, "worker")
	old, gotOld := restartInstanceEnv(t, store, tmp, "FAKE_PI_HOLD_REPORTS=1")
	id, lead := ID("alice", "worker"), ID("alice", "lead")
	a, p, err := old.ensure(id)
	if err != nil {
		t.Fatal(err)
	}
	old.record(a, p, "agent_start", runStart)
	old.record(a, p, "agent_settled", runSettled)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The report's read is held; let it go once it has been asked.
	eventually(t, "the report reaching the lead's pi", func() bool {
		if _, err := p.call(ctx, map[string]any{"type": "test_release_reports"}); err != nil {
			t.Fatal(err)
		}
		return len(gotOld.reports(lead, "worker")) == 1
	})
	entries := old.CloseForRestart()
	old.StopAll() // the lead's pi exits with the report unanswered
	entries = old.ReconcileRestart(ctx, entries)
	var entry restart.Agent
	for _, e := range entries {
		if e.Name == "worker" {
			entry = e
		}
	}
	if entry.Reason != restart.ReasonReport || entry.Report.State != restart.ReportUncertain {
		t.Fatalf("listed %+v", entries)
	}

	next, got := restartInstance(t, store, tmp)
	outcome, err := next.Resume(context.Background(), entry, "must not run")
	if outcome != restart.Uncertain || err == nil {
		t.Fatalf("Resume = %s %v", outcome, err)
	}
	if len(got.to(lead)) != 0 || len(got.to(id)) != 0 || next.running(id) != nil {
		t.Fatal("something was sent again after an uncertain report")
	}
	if !finishedAt(t, store, "worker").IsZero() {
		t.Fatal("an uncertain report recorded the worker finished")
	}
}

// A final message that is not the one recorded is not handed on.
func TestReportOfAnotherMessageNotHandedOn(t *testing.T) {
	store, tmp, y := restartStore(t)
	w := addWorker(t, store, "worker")
	m, got := restartInstance(t, store, tmp)
	entry := restart.Agent{Yesman: "alice", Name: "worker", Role: RoleWorker, WorkspaceHiredAt: y.HiredAt, AgentSince: w.StartedAt,
		Reason: restart.ReasonReport, Report: &restart.Report{State: restart.ReportNotSent, Digest: digest("another message")}}
	outcome, err := m.Resume(context.Background(), entry, "must not run")
	if outcome != restart.Uncertain || err == nil {
		t.Fatalf("Resume = %s %v", outcome, err)
	}
	if len(got.to(ID("alice", "lead"))) != 0 || len(got.to(ID("alice", "worker"))) != 0 {
		t.Fatal("sent something")
	}
	if !finishedAt(t, store, "worker").IsZero() {
		t.Fatal("recorded finished")
	}
}

// A report delivered and recorded the usual way before the restart is not
// sent again, whatever the record says.
func TestDeliveredReportStaysDelivered(t *testing.T) {
	store, tmp, y := restartStore(t)
	w := addWorker(t, store, "worker")
	old, gotOld := restartInstance(t, store, tmp)
	if err := old.Prompt(context.Background(), ID("alice", "worker"), "task"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the usual finish", func() bool { return !finishedAt(t, store, "worker").IsZero() })
	for _, e := range old.CloseForRestart() {
		if e.Name == "worker" {
			t.Fatalf("listed %+v", e)
		}
	}
	old.StopAll()
	next, got := restartInstance(t, store, tmp)
	entry := restart.Agent{Yesman: "alice", Name: "worker", Role: RoleWorker, WorkspaceHiredAt: y.HiredAt, AgentSince: w.StartedAt,
		Reason: restart.ReasonReport, Report: &restart.Report{State: restart.ReportNotSent}}
	if outcome, _ := next.Resume(context.Background(), entry, "must not run"); outcome != restart.Finished {
		t.Fatalf("Resume = %s", outcome)
	}
	if n := len(gotOld.reports(ID("alice", "lead"), "worker")) + len(got.reports(ID("alice", "lead"), "worker")); n != 1 {
		t.Fatalf("reported %d times", n)
	}
}

// A run the user stopped tells the lead it was stopped; that notice is not
// a report, and the restart does not list the worker for one.
func TestStoppedRunIsNoReport(t *testing.T) {
	store, tmp, _ := restartStore(t)
	addWorker(t, store, "worker")
	m, _ := restartInstanceEnv(t, store, tmp, "FAKE_PI_HOLD_PROMPTS=1") // the notice stays on its way
	a, p, err := m.ensure(ID("alice", "worker"))
	if err != nil {
		t.Fatal(err)
	}
	m.record(a, p, "agent_start", runStart)
	m.record(a, p, "agent_settled", json.RawMessage(`{"type":"agent_settled","aborted":true}`))
	eventually(t, "the notice under way", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return a.reports > 0 })
	for _, e := range m.CloseForRestart() {
		if e.Name == "worker" {
			t.Fatalf("listed %+v", e)
		}
	}
}

// An agent waiting on the user is counted waiting only if nothing else
// already runs it.
func TestNeedsYouYieldsToARunningAgent(t *testing.T) {
	store, tmp, y := restartStore(t)
	m, got := restartInstance(t, store, tmp)
	if err := m.Prompt(context.Background(), ID("alice", "lead"), "user first"); err != nil {
		t.Fatal(err)
	}
	lead := restart.Agent{Yesman: "alice", Name: "lead", Role: RoleLead, WorkspaceHiredAt: y.HiredAt, AgentSince: y.HiredAt, Reason: restart.ReasonNeedsYou}
	if outcome, _ := m.Resume(context.Background(), lead, "note"); outcome != restart.Active {
		t.Fatalf("Resume = %s", outcome)
	}
	if p := got.to(ID("alice", "lead")); len(p) != 1 {
		t.Fatalf("lead got %q", p)
	}
}

// A worker that finishes another run while its earlier report waits for
// the lead still has both reports delivered: tracking the latest run for a
// restart does not hold the earlier one back.
func TestEarlierRunReportStillDelivered(t *testing.T) {
	store, tmp, _ := restartStore(t)
	addWorker(t, store, "worker")
	m, got := restartInstance(t, store, tmp)
	id, leadID := ID("alice", "worker"), ID("alice", "lead")
	lead, _, err := m.ensure(leadID)
	if err != nil {
		t.Fatal(err)
	}
	a, p, err := m.ensure(id)
	if err != nil {
		t.Fatal(err)
	}
	lead.modelMu.Lock() // the first report waits for the lead
	m.record(a, p, "agent_start", runStart)
	m.record(a, p, "agent_settled", runSettled)
	eventually(t, "the first report read", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return a.handoff.digest != "" })
	m.record(a, p, "agent_start", runStart)
	m.record(a, p, "agent_settled", runSettled)
	eventually(t, "the second report read", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return a.handoff.run == 2 && a.handoff.digest != ""
	})
	lead.modelMu.Unlock()
	eventually(t, "both reports at the lead", func() bool { return len(got.reports(leadID, "worker")) == 2 })
}

// A report handed on after a restart gives up waiting for the lead's model
// lock when its time ends, and sends nothing after.
func TestHandedOnReportBoundedByLeadLock(t *testing.T) {
	store, tmp, y := restartStore(t)
	w := addWorker(t, store, "worker")
	m, got := restartInstance(t, store, tmp)
	leadID := ID("alice", "lead")
	lead, _, err := m.ensure(leadID)
	if err != nil {
		t.Fatal(err)
	}
	lead.modelMu.Lock()
	entry := restart.Agent{Yesman: "alice", Name: "worker", Role: RoleWorker, WorkspaceHiredAt: y.HiredAt, AgentSince: w.StartedAt,
		Reason: restart.ReasonReport, Report: &restart.Report{State: restart.ReportNotSent, Digest: digest("done")}}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan string, 1)
	go func() { o, _ := m.Resume(ctx, entry, "must not run"); done <- o }()
	var outcome string
	select {
	case outcome = <-done:
	case <-time.After(3 * time.Second):
		lead.modelMu.Unlock()
		t.Fatal("the handed-on report waited for the lead's lock past its time")
	}
	lead.modelMu.Unlock()
	if outcome != restart.Failed {
		t.Fatalf("Resume = %s", outcome)
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(got.reports(leadID, "worker")); n != 0 {
		t.Fatalf("an expired report was sent %d times", n)
	}
	if !finishedAt(t, store, "worker").IsZero() {
		t.Fatal("recorded finished without its report")
	}
}
