package main

import (
	"context"
	"os"
	"path/filepath"
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
