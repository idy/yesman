package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/yesmen"
)

func TestReturnIsPartOfAuthenticatedTaskCommit(t *testing.T) {
	x, wt, proof := returnFixture(t, "release", "")
	before := string(x.f.boardBytes(x.y.Name))
	for _, invalid := range []string{"forgot", "missing-unused", "incomplete-release", "wrong-instance", "foreign-certifier", "duplicate"} {
		p := proof
		token := x.w.Token
		e := map[string]any{"id": "t1", "note": "Handoff saved", "status": "done"}
		switch invalid {
		case "missing-unused":
			p.Unused = false
		case "incomplete-release":
			p.Publication = ""
		case "wrong-instance":
			p.Checkout = strings.Repeat("0", 32)
		case "foreign-certifier":
			if err := x.f.store.AddWorker("alice", yesmen.Worker{Name: "other", StartedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			token = x.f.start("alice/other").Token
		}
		if invalid != "forgot" {
			e["worktrees"] = []yesmen.WorktreeReturn{p}
		}
		if invalid == "duplicate" {
			e["worktrees"] = []yesmen.WorktreeReturn{p, p}
		}
		res := managementPost(t, x, token, "/v1/board", map[string]any{"reportWorktrees": true, "update": []any{e}})
		if res.Code == 200 || string(x.f.boardBytes(x.y.Name)) != before {
			t.Fatalf("%s was not refused atomically: %d %s", invalid, res.Code, res.Body)
		}
	}
	// A worker may always deliver its task while retaining its directory.
	submitReturn(t, x, []yesmen.WorktreeReturn{}...)
	x.f.m.Stop(x.me.agent.ID)
	if x.f.srv.reconcileCheckout(context.Background(), x.y, wt.CleanupID) != collectedOrIneligible {
		t.Fatal("keep scheduled cleanup")
	}
	assertRetained(t, x, wt)
}

func TestReclaimerRetriesOnlyPendingProof(t *testing.T) {
	x, wt, v := returnFixture(t, "author", "")
	submitReturn(t, x, v)
	x.f.m.Stop(x.me.agent.ID)
	s := x.f.srv
	var calls atomic.Int32
	read := s.ReadPullMerge
	observe := make(chan time.Time, 2)
	s.ReadPullMerge = func(ctx context.Context, target string, n int) (github.PullMerge, error) {
		if calls.Add(1) <= 2 {
			observe <- time.Now()
			return github.PullMerge{}, errors.New("temporarily unavailable")
		}
		return read(ctx, target, n)
	}
	r := NewWorktreeReclaimer(s)
	r.minRetry = 150 * time.Millisecond
	removed := make(chan struct{}, 1)
	s.Changed = func() { removed <- struct{}{} }
	r.Start()
	defer r.Close()
	first := <-observe
	for range 40 {
		r.BoardResult(x.y)
		r.OwnerRetired(x.y.Name, "unrelated")
	}
	second := <-observe
	if second.Sub(first) < r.minRetry {
		t.Fatal("unrelated events bypassed retry interval")
	}
	select {
	case <-removed:
	case <-time.After(10 * time.Second):
		t.Fatal("timed retry lost completion")
	}
	if calls.Load() != 3 || !recordOf(t, x, wt.Dir).Removed {
		t.Fatal("duplicate or missing retirement", calls.Load())
	}
}

func TestAutoRecoveryNeedsExactCheckpoint(t *testing.T) {
	for _, kind := range []string{"crash-after-remove", "missing-checkpoint", "still-registered"} {
		t.Run(kind, func(t *testing.T) {
			x, wt, v := returnFixture(t, "author", "")
			submitReturn(t, x, v)
			x.f.m.Stop(x.me.agent.ID)
			s := x.f.srv
			if kind == "crash-after-remove" {
				s.reconcileCheckout(context.Background(), x.y, wt.CleanupID)
				if !recordOf(t, x, wt.Dir).Removed {
					t.Fatal("initial remove failed")
				}
				rr, err := s.Yesmen.WorktreeRecords(x.y.Name)
				if err != nil {
					t.Fatal(err)
				}
				rr[0].Removed = false // final registry write was lost
				if err := s.Yesmen.SaveWorktreeRecords(x.y.Name, rr); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(wt.Path, "ignored"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				s.reconcileCheckout(context.Background(), x.y, wt.CleanupID)
				if recordOf(t, x, wt.Dir).Completion == nil {
					t.Fatal("expected saved proof")
				}
				if err := os.Remove(filepath.Join(wt.Path, "ignored")); err != nil {
					t.Fatal(err)
				}
				if kind == "still-registered" {
					if err := os.Rename(wt.Path, wt.Path+".saved"); err != nil {
						t.Fatal(err)
					}
				} else if err := s.Pool.RemoveWorktree(context.Background(), wt.Path, false); err != nil {
					t.Fatal(err)
				}
			}
			s.ReadPullMerge = func(context.Context, string, int) (github.PullMerge, error) {
				t.Fatal("missing path queried GitHub")
				return github.PullMerge{}, nil
			}
			s.reconcileCheckout(context.Background(), x.y, wt.CleanupID)
			if got := recordOf(t, x, wt.Dir); got.Removed != (kind == "crash-after-remove") || got.Completion.Head != wt.Head {
				t.Fatalf("incorrect recovery: %+v", got)
			}
		})
	}
}

func TestLegacyAliasRequiresExplicitPublishedSource(t *testing.T) {
	x, wt, v := returnFixture(t, "author", "integration/local")
	s := x.f.srv
	rr, err := s.Yesmen.WorktreeRecords(x.y.Name)
	if err != nil {
		t.Fatal(err)
	}
	rr[0].Root = nil // pre-directory-identity registration, with its actual Git nonce
	if err := s.Yesmen.SaveWorktreeRecords(x.y.Name, rr); err != nil {
		t.Fatal(err)
	}
	status := "done"
	if _, err := s.Yesmen.ChangeBoard(x.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: &status}}}); err != nil {
		t.Fatal(err)
	}
	manual := func(source string) cleanupResult {
		v.SourceBranch = source
		res := managementPost(t, x, x.w.Token, "/v1/worktrees/remove", removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID, Completion: &v.WorktreeCompletion})
		var out cleanupResult
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &out) != nil {
			t.Fatalf("manual completion: %d %s", res.Code, res.Body)
		}
		return out
	}
	if out := manual(""); out.Removed || !strings.Contains(out.Reason, "exact source") {
		t.Fatalf("used same SHA as provenance: %+v", out)
	}
	if s.reconcileCheckout(context.Background(), x.y, wt.CleanupID) != collectedOrIneligible {
		t.Fatal("replayed rejected legacy proof")
	}
	v.Evidence = "Owner's saved push receipt for integration/local:feature is in the task report. Restore using the recorded source SHA."
	if out := manual("feature"); !out.Removed || out.Record.Root != nil || out.Record.Completion.SourceBranch != "feature" || out.Record.Branch != "integration/local" || len(out.Record.Attempts) != 1 {
		t.Fatalf("published source not preserved: %+v", out)
	}
}

func TestReclaimYieldsToFileAndWorkspaceLeases(t *testing.T) {
	x, wt, v := returnFixture(t, "review", "")
	submitReturn(t, x, v)
	x.f.m.Stop(x.me.agent.ID)
	s := x.f.srv
	try := func() error {
		if s.reconcileCheckout(context.Background(), x.y, wt.CleanupID) != retryLater {
			t.Fatal("did not retain while resource was leased")
		}
		assertRetained(t, x, wt)
		return nil
	}
	if err := s.Pool.WithWorktree(wt.Path, try); err != nil {
		t.Fatal(err)
	}
	if err := s.Yesmen.Use(x.y.Name, func(yesmen.Yesman) error { return try() }); err != nil {
		t.Fatal(err)
	}
	s.reconcileCheckout(context.Background(), x.y, wt.CleanupID)
	if !recordOf(t, x, wt.Dir).Removed {
		t.Fatal("released lease still held tree")
	}
}
