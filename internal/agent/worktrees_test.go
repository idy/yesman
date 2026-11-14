package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/idy/yesman/internal/yesmen"
)

func TestWorktreeOwnerGuard(t *testing.T) {
	for _, tc := range []struct {
		name   string
		a      *agent
		caller string
		allow  bool
	}{
		{"not running after restart", nil, "alice/cleanup", true},
		{"stopped", &agent{info: Info{Status: Stopped}}, "alice/cleanup", true},
		{"active", &agent{info: Info{Status: Working}}, "alice/cleanup", false},
		{"waiting", &agent{info: Info{Status: Waiting}}, "alice/cleanup", false},
		{"idle process", &agent{proc: &proc{}}, "alice/cleanup", false},
		{"starting", &agent{starting: make(chan struct{})}, "alice/cleanup", false},
		{"pending prompt", &agent{prompts: 1}, "alice/cleanup", false},
		{"pending report", &agent{reports: 1}, "alice/cleanup", false},
		{"self cleanup", &agent{info: Info{Status: Working}, proc: &proc{}}, "alice/owner", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{agents: map[string]*agent{"alice/owner": tc.a}}
			called := false
			err := m.WithWorktreeOwner("alice/owner", tc.caller, func() error { called = true; return nil })
			if called != tc.allow || (err == nil) != tc.allow {
				t.Fatalf("called=%v err=%v", called, err)
			}
		})
	}
}

func TestWorktreeCleanupExcludesOwnerStartupAndInput(t *testing.T) {
	m, store := newTestManager(t, 0)
	if err := store.AddWorker("alice", yesmen.Worker{Name: "owner"}); err != nil {
		t.Fatal(err)
	}
	err := m.WithWorktreeOwner("alice/owner", "alice/cleanup", func() error {
		// Manager reads remain available while git runs. Startup/input for
		// this owner are refused before any process/model can be invoked.
		if _, err := m.Get("alice/owner"); err != nil {
			t.Fatal(err)
		}
		if err := m.Prompt(context.Background(), "alice/owner", "resume"); !errors.Is(err, errWorktreeCleanup) {
			t.Fatalf("owner startup during cleanup: %v", err)
		}
		if err := m.WithWorktreeOwner("alice/owner", "alice/cleanup", func() error { t.Fatal("duplicate cleanup ran"); return nil }); !errors.Is(err, errWorktreeCleanup) {
			t.Fatalf("duplicate cleanup: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.WithWorktreeOwner("alice/owner", "alice/cleanup", func() error { return nil }); err != nil {
		t.Fatalf("reservation did not clear: %v", err)
	}
}
