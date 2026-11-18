package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/idy/yesman/internal/restart"
)

// A valid but unfinished record must also block the independent task-startup
// path. Holding its exact generations is distinct from resuming any agent.
func TestRestartCompositionUnfinishedHoldsWithoutResume(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "restart")
	old, _ := testRestarts(t, dir, "0.1.48")
	_ = old.begin(restart.KindRestart, UpdateView{})
	entries := []restart.Agent{working("lead"), working("worker")}
	old.beforeStop(func() []restart.Agent { return entries })
	next, _ := testRestarts(t, dir, "0.1.48")
	held := []restart.Agent{}
	next.hold = func(a restart.Agent) error { held = append(held, a); return nil }
	next.resume(func(context.Context, restart.Agent, string) (string, error) {
		t.Fatal("unfinished resumed")
		return "", nil
	})
	if len(held) != 2 || held[0] != entries[0] || held[1] != entries[1] {
		t.Fatalf("wrong generations held: %+v", held)
	}
	if !next.view().Last.Unfinished {
		t.Fatal("unfinished result lost")
	}
	later, _ := testRestarts(t, dir, "0.1.48")
	later.hold = func(restart.Agent) error { t.Fatal("repeated hold after claim"); return nil }
	later.resume(func(context.Context, restart.Agent, string) (string, error) {
		t.Fatal("replayed claim")
		return "", nil
	})
}

func TestRestartCompositionFailedAdmissionIsHeld(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "restart")
	old, _ := testRestarts(t, dir, "0.1.48")
	_ = old.begin(restart.KindRestart, UpdateView{})
	old.beforeStop(func() []restart.Agent { return []restart.Agent{working("lead"), working("worker")} })
	old.afterStop(unchanged)
	next, _ := testRestarts(t, dir, "0.1.48")
	var mu sync.Mutex
	held := []restart.Agent{}
	next.hold = func(a restart.Agent) error { mu.Lock(); defer mu.Unlock(); held = append(held, a); return nil }
	next.resume(func(context.Context, restart.Agent, string) (string, error) {
		return restart.Failed, context.DeadlineExceeded
	})
	if len(held) != 2 {
		t.Fatalf("failed recoveries not held: %+v", held)
	}
}
