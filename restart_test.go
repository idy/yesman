package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/restart"
)

// testRestarts is one instance's restarts over a shared directory.
func testRestarts(t *testing.T, dir, version string) (*restarts, *int) {
	t.Helper()
	relaunched := 0
	r := newRestarts(restart.Open(dir))
	r.relaunch = func() { relaunched++ }
	r.version = func() string { return version }
	r.changed = func(RestartView) {}
	return r, &relaunched
}

var hired = time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)

func working(name string) restart.Agent {
	role, since := "worker", hired.Add(time.Hour)
	if name == "lead" {
		role, since = "lead", hired
	}
	return restart.Agent{Yesman: "alice", Name: name, Role: role, WorkspaceHiredAt: hired, AgentSince: since, Reason: restart.ReasonWorking}
}

// unchanged is a reconciliation in which nothing settled meanwhile.
func unchanged(_ context.Context, in []restart.Agent) []restart.Agent { return in }

// counting is a resumer that only counts what it was asked, by reason.
func counting(mu *sync.Mutex, by map[string]int) resumer {
	return func(_ context.Context, a restart.Agent, _ string) (string, error) {
		mu.Lock()
		by[a.Reason]++
		mu.Unlock()
		if a.Reason == restart.ReasonReport {
			return restart.Reported, nil
		}
		return restart.Resumed, nil
	}
}

func TestRestartAsksOnce(t *testing.T) {
	r, relaunched := testRestarts(t, filepath.Join(t.TempDir(), "restart"), "0.1.47")
	for range 3 {
		if err := r.begin(restart.KindRestart, UpdateView{State: updateIdle}); err != nil {
			t.Fatal(err)
		}
	}
	if *relaunched != 1 || !r.view().Pending {
		t.Fatalf("relaunched %d times, pending %v", *relaunched, r.view().Pending)
	}
}

func TestRestartToUpdateNeedsReady(t *testing.T) {
	r, relaunched := testRestarts(t, filepath.Join(t.TempDir(), "restart"), "0.1.47")
	if err := r.begin(restart.KindUpdate, UpdateView{State: updateAvailable, Version: "0.1.48"}); err == nil || *relaunched != 0 {
		t.Fatalf("restarted to an update that is not installed: %v", err)
	}
	if r.view().Pending {
		t.Fatal("pending without a restart")
	}
}

func TestPlainQuitRecordsNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "restart")
	r, _ := testRestarts(t, dir, "0.1.47")
	r.beforeStop(func() []restart.Agent { t.Fatal("a plain quit closed the agents for a restart"); return nil })
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("a plain quit wrote %v", err)
	}
	// The next start finds nothing and wakes nobody.
	next, _ := testRestarts(t, dir, "0.1.47")
	next.resume(func(context.Context, restart.Agent, string) (string, error) {
		t.Fatal("woke an agent after a plain quit")
		return "", nil
	})
	if v := next.view(); v.Last != nil {
		t.Fatalf("a result after a plain quit: %+v", v.Last)
	}
}

func TestCanceledRestartLeavesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "restart")
	r, _ := testRestarts(t, dir, "0.1.47")
	_ = r.begin(restart.KindRestart, UpdateView{})
	r.beforeStop(func() []restart.Agent { return []restart.Agent{working("lead")} })
	if _, err := os.Stat(filepath.Join(dir, "intent.json")); err != nil {
		t.Fatal(err)
	}
	r.canceled()
	if _, err := os.Stat(filepath.Join(dir, "intent.json")); !os.IsNotExist(err) {
		t.Fatalf("a canceled restart left its intent: %v", err)
	}
	if r.view().Pending {
		t.Fatal("still pending")
	}
}

// Instance A restarts with agents at work; B lets each go on once, with
// the restart's note; C, a later start, wakes nobody again.
func TestRestartContinuesOnceInTheNextInstance(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "restart")
	a, relaunched := testRestarts(t, dir, "0.1.47")
	if err := a.begin(restart.KindRestart, UpdateView{State: updateReady, Version: "0.1.48"}); err != nil || *relaunched != 1 {
		t.Fatalf("begin: %v", err)
	}
	waiting := working("asker")
	waiting.Reason = restart.ReasonNeedsYou
	a.beforeStop(func() []restart.Agent { return []restart.Agent{working("lead"), working("fixer"), waiting} })
	a.afterStop(unchanged)

	b, _ := testRestarts(t, dir, "0.1.48")
	var mu sync.Mutex
	notes := map[string]string{}
	b.resume(func(_ context.Context, ag restart.Agent, note string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if _, again := notes[ag.Name]; again {
			t.Errorf("%s resumed twice", ag.Name)
		}
		notes[ag.Name] = note
		if ag.Reason == restart.ReasonNeedsYou {
			return restart.Waiting, nil
		}
		return restart.Resumed, nil
	})
	if len(notes) != 3 {
		t.Fatalf("resumed %v", notes)
	}
	if n := notes["lead"]; !strings.HasPrefix(n, "[yesman] Yes Man restarted to update to 0.1.48 while you were working") ||
		!strings.Contains(n, "check whether it already took effect") {
		t.Fatalf("note: %q", n)
	}
	v := b.view()
	if v.Pending || v.Last == nil || v.Last.Kind != restart.KindUpdate || v.Last.FromVersion != "0.1.47" || v.Last.Version != "0.1.48" || len(v.Last.Agents) != 3 {
		t.Fatalf("result: %+v", v.Last)
	}
	// It is on disk for the window of a later start too.
	c, _ := testRestarts(t, dir, "0.1.48")
	c.resume(func(context.Context, restart.Agent, string) (string, error) {
		t.Fatal("a later start woke an agent again")
		return "", nil
	})
	if last := c.view().Last; last == nil || last.ID != v.Last.ID {
		t.Fatalf("a later start lost the result: %+v", last)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "last.json" {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}

func TestRestartNoteSaysWhetherTheUpdateApplied(t *testing.T) {
	in := restart.Intent{Kind: restart.KindUpdate, ToVersion: "0.1.48"}
	if n := restartNote(in, "0.1.47", restart.ReasonWorking); !strings.Contains(n, "the update did not apply: it still runs 0.1.47") {
		t.Fatalf("note: %q", n)
	}
	if n := restartNote(restart.Intent{Kind: restart.KindRestart}, "0.1.47", restart.ReasonQuestion); !strings.HasPrefix(n, "[yesman] Yes Man restarted while you waited for your lead") {
		t.Fatalf("note: %q", n)
	}
}

func TestRejectedRecordWakesNobody(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "restart")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "intent.json"), []byte(`{"version":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _ := testRestarts(t, dir, "0.1.47")
	r.resume(func(context.Context, restart.Agent, string) (string, error) {
		t.Fatal("a rejected record woke an agent")
		return "", nil
	})
	if last := r.view().Last; last == nil || last.Rejected == "" {
		t.Fatalf("result: %+v", last)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "rejected-*.json")); len(m) != 1 {
		t.Fatalf("the rejected record was not kept: %v", m)
	}
}

// Leads go on before workers, whose reports would otherwise reach a lead
// first and make it seem talked to. All share one deadline; a resume keeps
// its place until it has returned, so no more than resumeParallel run at
// once, and the result is written only after every one has returned.
func TestResumeLeadsFirstAndBounded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "restart")
	a, _ := testRestarts(t, dir, "0.1.47")
	_ = a.begin(restart.KindRestart, UpdateView{})
	list := []restart.Agent{working("fixer"), working("stuck"), working("lead")}
	for i := range 6 {
		list = append(list, working(fmt.Sprintf("w%d", i)))
	}
	a.beforeStop(func() []restart.Agent { return list })
	a.afterStop(unchanged)

	b, _ := testRestarts(t, dir, "0.1.47")
	b.timeout = 300 * time.Millisecond
	var mu sync.Mutex
	var order []string
	var running, most int
	returned := map[string]bool{}
	start := time.Now()
	b.resume(func(ctx context.Context, ag restart.Agent, _ string) (string, error) {
		mu.Lock()
		order = append(order, ag.Name)
		running++
		most = max(most, running)
		mu.Unlock()
		defer func() {
			mu.Lock()
			running--
			returned[ag.Name] = true
			mu.Unlock()
		}()
		if ag.Name == "stuck" {
			<-ctx.Done() // a start that does not end in time; Resume gives up with ctx
			return restart.Failed, ctx.Err()
		}
		if ctx.Err() != nil {
			return restart.Failed, ctx.Err()
		}
		return restart.Resumed, nil
	})
	mu.Lock()
	defer mu.Unlock()
	if len(order) == 0 || order[0] != "lead" {
		t.Fatalf("order %v: the lead must go first", order)
	}
	if most > resumeParallel {
		t.Fatalf("%d resumes ran at once", most)
	}
	if len(returned) != len(list) || running != 0 {
		t.Fatalf("the result was written before every resume returned: %v", returned)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("one deadline for all took %s", d)
	}
	got := map[string]string{}
	for _, o := range b.view().Last.Agents {
		got[o.Name] = o.Outcome
	}
	if got["lead"] != restart.Resumed || got["fixer"] != restart.Resumed || got["stuck"] != restart.Failed {
		t.Fatalf("outcomes %v", got)
	}
}

// Once the agents have stopped, where the reports got is recorded over the
// intent; nothing changes when nothing settled, and a plain Quit records
// nothing at all.
func TestAfterStopRecordsWhereReportsGot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "restart")
	q, _ := testRestarts(t, dir, "0.1.47")
	q.afterStop(func(context.Context, []restart.Agent) []restart.Agent {
		t.Fatal("a plain quit reconciled")
		return nil
	})

	a, _ := testRestarts(t, dir, "0.1.47")
	_ = a.begin(restart.KindRestart, UpdateView{})
	a.beforeStop(func() []restart.Agent { return []restart.Agent{working("lead"), working("fixer")} })
	a.afterStop(func(ctx context.Context, in []restart.Agent) []restart.Agent {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the drain is not bounded")
		}
		out := append([]restart.Agent(nil), in...)
		out[1].Reason = restart.ReasonReport
		out[1].Report = &restart.Report{State: restart.ReportNotSent, Digest: strings.Repeat("a", 64)}
		return out
	})
	in, claim, found, rejected, err := restart.Open(dir).Claim()
	if err != nil || !found || rejected != "" {
		t.Fatalf("claim: %v %v %q", err, found, rejected)
	}
	_ = claim
	if in.Phase != restart.PhaseFinal {
		t.Fatalf("phase %q", in.Phase)
	}
	if in.Agents[1].Reason != restart.ReasonReport || in.Agents[1].Report.State != restart.ReportNotSent {
		t.Fatalf("the reconciled report was not recorded: %+v", in.Agents)
	}
}

// many is an ordinary restart of a lead and n workers at work.
func many(n int) []restart.Agent {
	out := []restart.Agent{working("lead")}
	for i := range n {
		w := working(fmt.Sprintf("worker-%03d", i))
		w.Yesman = "alice"
		out = append(out, w)
	}
	return out
}

// toReports is a reconciliation in which every worker's run settled while
// the record was written: each is listed for its report, not sent.
func toReports(_ context.Context, in []restart.Agent) []restart.Agent {
	out := append([]restart.Agent(nil), in...)
	for i := range out {
		if out[i].Role == "worker" {
			out[i].Reason = restart.ReasonReport
			out[i].Report = &restart.Report{State: restart.ReportNotSent, Digest: strings.Repeat("a", 64)}
		}
	}
	return out
}

// The record grows as reports are recorded, and is finished: the next
// instance hands each report on and starts no run for any of them.
func TestOrdinaryGrowthHandsOnReports(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "restart")
	a, _ := testRestarts(t, dir, "0.1.47")
	_ = a.begin(restart.KindRestart, UpdateView{})
	a.beforeStop(func() []restart.Agent { return many(200) })
	a.afterStop(toReports)
	b, _ := testRestarts(t, dir, "0.1.47")
	var mu sync.Mutex
	by := map[string]int{}
	b.resume(counting(&mu, by))
	if by[restart.ReasonReport] != 200 || by[restart.ReasonWorking] != 1 {
		t.Fatalf("asked %v; want 200 reports and the lead working", by)
	}
	if last := b.view().Last; last.Unfinished || len(last.Agents) != 201 {
		t.Fatalf("result: unfinished=%v agents=%d", last.Unfinished, len(last.Agents))
	}
}

// expectUnfinished starts the next instance and checks it woke nobody and
// says so, listing every agent recorded as not continued.
func expectUnfinished(t *testing.T, dir string, n int) {
	t.Helper()
	b, _ := testRestarts(t, dir, "0.1.47")
	var mu sync.Mutex
	by := map[string]int{}
	b.resume(counting(&mu, by))
	if len(by) != 0 {
		t.Fatalf("an unfinished record woke agents: %v", by)
	}
	last := b.view().Last
	if last == nil || !last.Unfinished || len(last.Agents) != n {
		t.Fatalf("result: %+v", last)
	}
	for _, o := range last.Agents {
		if o.Outcome != restart.NotContinued {
			t.Fatalf("outcome %+v", o)
		}
	}
}

// The app ends between the two writes: the provisional record wakes nobody.
func TestInterruptedBeforeFinalWakesNobody(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "restart")
	a, _ := testRestarts(t, dir, "0.1.47")
	_ = a.begin(restart.KindRestart, UpdateView{})
	a.beforeStop(func() []restart.Agent { return many(3) })
	// no afterStop: the old instance was killed while its agents stopped
	expectUnfinished(t, dir, 4)
}

// The final record is larger than a record may be: it is refused whole,
// and the provisional one wakes nobody. Nothing is cut short to fit.
func TestFinalTooLargeWakesNobody(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "restart")
	a, _ := testRestarts(t, dir, "0.1.47")
	_ = a.begin(restart.KindRestart, UpdateView{})
	list := many(0)
	for i := range 255 {
		w := working(fmt.Sprintf("%s%04d", strings.Repeat("w", 28), i))
		w.Yesman = "alice"
		list = append(list, w)
	}
	a.beforeStop(func() []restart.Agent { return list })
	if _, err := os.Stat(filepath.Join(dir, "intent.json")); err != nil {
		t.Fatalf("the provisional record was not written: %v", err)
	}
	a.afterStop(toReports)
	expectUnfinished(t, dir, 256)
}

// Writing the final record fails: the provisional one wakes nobody.
func TestFinalWriteErrorWakesNobody(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory still takes new files on Windows")
	}
	dir := filepath.Join(t.TempDir(), "restart")
	a, _ := testRestarts(t, dir, "0.1.47")
	_ = a.begin(restart.KindRestart, UpdateView{})
	a.beforeStop(func() []restart.Agent { return many(2) })
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	a.afterStop(toReports)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	expectUnfinished(t, dir, 3)
}

// A record too large to write at all leaves nothing: nothing to finish,
// and the next instance neither wakes nor reports anything.
func TestInitialRefusalLeavesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "restart")
	a, _ := testRestarts(t, dir, "0.1.47")
	_ = a.begin(restart.KindRestart, UpdateView{})
	a.beforeStop(func() []restart.Agent { return many(256) }) // 257: more than a record holds
	a.afterStop(func(context.Context, []restart.Agent) []restart.Agent {
		t.Fatal("reconciled a record that was never written")
		return nil
	})
	if _, err := os.Lstat(filepath.Join(dir, "intent.json")); !os.IsNotExist(err) {
		t.Fatalf("a refused record was written: %v", err)
	}
	b, _ := testRestarts(t, dir, "0.1.47")
	b.resume(func(context.Context, restart.Agent, string) (string, error) {
		t.Fatal("woke an agent")
		return "", nil
	})
	if b.view().Last != nil {
		t.Fatal("a result without a record")
	}
}
