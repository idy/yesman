package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

const testPublication = "https://github.com/acme/demo/releases/tag/v-fixture"

func returnFixture(t *testing.T, purpose, alias string) (*worktreeFixture, pool.Worktree, yesmen.WorktreeReturn) {
	t.Helper()
	x := newWorktreeFixture(t)
	s := x.f.srv
	s.ReadPullMerge = x.cf.s.ReadPullMerge
	s.HasOpenPull = func(context.Context, string, string, string) (bool, error) { return false, nil }
	s.ReadPublishedTag = func(ctx context.Context, target, tag string) (string, error) {
		if target != "acme/demo" || tag != "v-fixture" {
			return "", fmt.Errorf("unexpected publication %s/%s", target, tag)
		}
		return x.cf.merge, nil
	}
	var wt pool.Worktree
	if alias == "" {
		wt = managedCheckout(t, x, purpose)
	} else {
		res := managementPost(t, x, x.w.Token, "/v1/checkout", CheckoutRequest{Repo: x.cf.repo.Name, Branch: alias, Base: x.cf.head, Task: "t1", Purpose: purpose, Dir: "managed"})
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &wt) != nil || wt.CleanupID == "" {
			t.Fatalf("checkout: %s", res.Body)
		}
	}
	result := yesmen.WorktreeReturn{Checkout: wt.CleanupID, WorktreeCompletion: *x.cf.completion("done")}
	if purpose == "review" {
		result.Outcome = "pass"
	}
	if purpose == "release" {
		result.Publication = testPublication
	}
	if alias != "" {
		result.SourceBranch = "feature"
	}
	return x, wt, result
}

func submitReturn(t *testing.T, x *worktreeFixture, v ...yesmen.WorktreeReturn) {
	t.Helper()
	res := managementPost(t, x, x.w.Token, "/v1/board", map[string]any{"reportWorktrees": true, "update": []any{map[string]any{"id": "t1", "status": "done", "note": "Delivered tested source. The push to feature and publication receipt are saved in the task evidence.", "worktrees": v}}})
	if res.Code != 200 {
		t.Fatalf("board return: %d %s", res.Code, res.Body)
	}
}

func assertRetained(t *testing.T, x *worktreeFixture, wt pool.Worktree) {
	t.Helper()
	if rec := recordOf(t, x, wt.Dir); rec.Removed {
		t.Fatalf("retired: %+v", rec)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatal("lost directory", err)
	}
}

func TestReturnedCheckoutServiceLifecycle(t *testing.T) {
	for _, kind := range []string{"review", "author", "release", "published-alias"} {
		t.Run(kind, func(t *testing.T) {
			purpose, alias := kind, ""
			if kind == "published-alias" {
				purpose, alias = "author", "integration/local"
			}
			x, wt, result := returnFixture(t, purpose, alias)
			s := x.f.srv
			refs := cleanupGit(t, wt.Path, "for-each-ref", "--format=%(refname) %(objectname)")
			var reads atomic.Int32
			read := s.ReadPullMerge
			s.ReadPullMerge = func(ctx context.Context, target string, n int) (github.PullMerge, error) {
				reads.Add(1)
				return read(ctx, target, n)
			}
			removed := make(chan struct{}, 1)
			s.Changed = func() {
				select {
				case removed <- struct{}{}:
				default:
				}
			}
			r := NewWorktreeReclaimer(s)
			s.Reclaimer = r
			r.Start()
			defer r.Close()
			submitReturn(t, x, result)
			// The authenticated Board commit has recorded a proposed result. A
			// still-running owner cannot be retired, even by its own return.
			if retry := s.reconcileCheckout(context.Background(), x.y, wt.CleanupID); retry != waitForOwner || reads.Load() != 0 {
				t.Fatal("active owner reached verification")
			}
			assertRetained(t, x, wt)
			x.f.m.Stop(x.me.agent.ID)
			r.OwnerRetired(x.y.Name, x.me.agent.Name)
			select {
			case <-removed:
			case <-time.After(15 * time.Second):
				t.Fatal("owner retired but its checkout did not")
			}
			if _, err := os.Lstat(wt.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("worktree still on disk", err)
			}
			rec := recordOf(t, x, wt.Dir)
			if !rec.Removed || rec.Completion == nil || rec.Completion.Head != wt.Head || rec.Completion.SourceBranch != result.SourceBranch || rec.Completion.CertifiedBy != x.me.agent.Name || !rec.Completion.CertifiedSince.Equal(x.me.agent.Since) || rec.BoardRound.IsZero() || !strings.Contains(rec.Completion.Evidence, "Reconstruct") {
				t.Fatalf("incomplete provenance: %+v", rec)
			}
			if purpose == "review" && reads.Load() != 0 {
				t.Fatal("review made a PR lookup")
			}
			assertCleanupRefsPreserved(t, x.cf, refs, wt)
		})
	}
}

func TestReturnedAuthorWaitsAcrossRestart(t *testing.T) {
	x, wt, v := returnFixture(t, "author", "")
	submitReturn(t, x, v)
	x.f.m.Stop(x.me.agent.ID)
	s := x.f.srv
	read := s.ReadPullMerge
	s.ReadPullMerge = func(context.Context, string, int) (github.PullMerge, error) {
		return github.PullMerge{State: "open"}, nil
	}
	if s.reconcileCheckout(context.Background(), x.y, wt.CleanupID) != retryLater {
		t.Fatal("open PR lost its retry")
	}
	if recordOf(t, x, wt.Dir).Completion != nil {
		t.Fatal("open PR froze completion")
	}
	assertRetained(t, x, wt)
	// Fresh manager, registry and event loop. Startup alone collects the
	// result already on the board, even though no worker is running.
	store := yesmen.New(filepath.Dir(x.y.Path), filepath.Join(filepath.Dir(filepath.Dir(x.y.Path)), "archive"))
	m := agent.New(agent.Config{Yesmen: store})
	defer m.StopAll()
	removed := make(chan struct{}, 1)
	cold := &Server{Pool: s.Pool, Yesmen: store, Agents: m, ReadPullMerge: read, HasOpenPull: s.HasOpenPull, Changed: func() { removed <- struct{}{} }}
	r := NewWorktreeReclaimer(cold)
	r.Start()
	defer r.Close()
	for range 50 {
		r.BoardResult(x.y)
		r.OwnerRetired(x.y.Name, "unrelated")
	}
	select {
	case <-removed:
	case <-time.After(10 * time.Second):
		t.Fatal("startup did not recover the open-then-merged return")
	}
	rec := recordOf(t, x, wt.Dir)
	if !rec.Removed || rec.Completion.Head != wt.Head {
		t.Fatalf("cold result: %+v", rec)
	}
	if registered, err := s.Pool.HasWorktreeRegistration(context.Background(), wt.Repo, wt.Path); err != nil || registered {
		t.Fatal("registration survived", err)
	}
}

func TestReturnedCheckoutRefusals(t *testing.T) {
	for _, kind := range []string{"tracked", "untracked", "ignored", "hidden-index", "git-in-progress", "post-merge", "open-followup", "alias-open-local", "wrong-alias", "source-unavailable", "release-unpublished", "release-unrelated", "more-owner-work", "paused-task", "worker-paused", "owner-replaced", "keep-frozen", "unmarked-root", "path-replaced"} {
		t.Run(kind, func(t *testing.T) {
			purpose, alias := "author", ""
			if strings.HasPrefix(kind, "release-") {
				purpose = "release"
			}
			if strings.Contains(kind, "alias") {
				alias = "local/integration"
			}
			x, wt, result := returnFixture(t, purpose, alias)
			s := x.f.srv
			if kind == "wrong-alias" {
				result.SourceBranch = ""
			}
			submitReturn(t, x, result)
			x.f.m.Stop(x.me.agent.ID)
			switch kind {
			case "tracked", "untracked", "ignored", "hidden-index":
				path := kind
				if kind == "tracked" || kind == "hidden-index" {
					path = "README"
				}
				if kind == "hidden-index" {
					cleanupGit(t, wt.Path, "update-index", "--assume-unchanged", path)
				}
				if err := os.WriteFile(filepath.Join(wt.Path, path), []byte("keep\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "git-in-progress":
				path := cleanupGit(t, wt.Path, "rev-parse", "--absolute-git-dir")
				if err := os.WriteFile(filepath.Join(path, "index.lock"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "post-merge":
				cleanupGit(t, wt.Path, "commit", "--allow-empty", "-qm", "more")
			case "open-followup", "alias-open-local":
				s.HasOpenPull = func(_ context.Context, target, owner, ref string) (bool, error) {
					return target == "acme/demo" && owner == "acme" && ref == wt.Branch, nil
				}
			case "source-unavailable":
				s.HasOpenPull = func(context.Context, string, string, string) (bool, error) { return false, errors.New("offline") }
			case "release-unpublished", "release-unrelated":
				s.ReadPublishedTag = func(context.Context, string, string) (string, error) {
					if kind == "release-unpublished" {
						return "", errors.New("draft")
					}
					return strings.Repeat("0", 40), nil
				}
			case "more-owner-work":
				title, owner := "active assignment", x.me.agent.Name
				if _, err := s.Yesmen.ChangeBoard(x.y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: &title, Owner: &owner}}}); err != nil {
					t.Fatal(err)
				}
			case "paused-task":
				if _, err := s.Yesmen.ChangeBoard(x.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Wait: &yesmen.WaitEdit{Kind: "paused", Reason: "user hold"}}}}); err != nil {
					t.Fatal(err)
				}
			case "worker-paused":
				if err := s.Yesmen.PauseWorker(x.y.Name, x.me.agent.Name, x.me.agent.Since, time.Now()); err != nil {
					t.Fatal(err)
				}
			case "owner-replaced":
				if err := s.Yesmen.RemoveWorker(x.y.Name, x.me.agent.Name); err != nil {
					t.Fatal(err)
				}
				if err := s.Yesmen.AddWorker(x.y.Name, yesmen.Worker{Name: x.me.agent.Name, StartedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			case "keep-frozen":
				if err := os.WriteFile(filepath.Join(wt.Path, "ignored"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				s.reconcileCheckout(context.Background(), x.y, wt.CleanupID)
				if recordOf(t, x, wt.Dir).Completion == nil {
					t.Fatal("completion was not saved before retention")
				}
				if err := os.Remove(filepath.Join(wt.Path, "ignored")); err != nil {
					t.Fatal(err)
				}
				x.w = x.f.start(x.me.agent.ID)
				submitReturn(t, x, []yesmen.WorktreeReturn{}...)
				x.f.m.Stop(x.me.agent.ID)
			case "unmarked-root":
				rr, _ := s.Yesmen.WorktreeRecords(x.y.Name)
				rr[0].Root = nil
				if err := s.Yesmen.SaveWorktreeRecords(x.y.Name, rr); err != nil {
					t.Fatal(err)
				}
			case "path-replaced":
				if err := os.Rename(wt.Path, wt.Path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(wt.Path, 0700); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(wt.Path+".saved", ".git"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(wt.Path, ".git"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			s.reconcileCheckout(context.Background(), x.y, wt.CleanupID)
			assertRetained(t, x, wt)
		})
	}
}

func TestReopenedDuringMergeLookupRetainsTree(t *testing.T) {
	x, wt, v := returnFixture(t, "author", "")
	submitReturn(t, x, v)
	x.f.m.Stop(x.me.agent.ID)
	s := x.f.srv
	read := s.ReadPullMerge
	entered, release := make(chan struct{}), make(chan struct{})
	s.ReadPullMerge = func(ctx context.Context, target string, n int) (github.PullMerge, error) {
		close(entered)
		<-release
		return read(ctx, target, n)
	}
	done := make(chan reclaimWait, 1)
	go func() { done <- s.reconcileCheckout(context.Background(), x.y, wt.CleanupID) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("never entered verification")
	}
	// A current board writer (including another worker) can reopen during
	// verification. It does not wait for remote I/O and its new round wins.
	lead := x.f.start("alice/lead")
	res := managementPost(t, x, lead.Token, "/v1/board", map[string]any{"update": []any{map[string]any{"id": "t1", "status": "doing"}}})
	close(release)
	<-done
	if res.Code != 200 {
		t.Fatal(res.Body)
	}
	assertRetained(t, x, wt)
	if recordOf(t, x, wt.Dir).Completion != nil {
		t.Fatal("froze stale verification")
	}
}
