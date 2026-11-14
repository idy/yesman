package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

func TestWorktreeCheckpointAndInterruptedCleanup(t *testing.T) {
	f := newCleanupFixture(t)
	wt := f.checkout(t, "review", "review")
	if out := f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir, Completion: f.completion("pass")}); !out.Removed {
		t.Fatalf("cleanup: %+v", out)
	}
	// Remote branch deletion and garbage collection after a squash merge
	// must not destroy the source commit needed by a resumed review.
	cleanupGit(t, f.repo.URL, "branch", "-D", "feature")
	if err := f.s.Pool.Fetch(context.Background(), f.repo.Name, true); err != nil {
		t.Fatal(err)
	}
	cleanupGit(t, f.repo.Path, "gc", "--prune=now")
	if got := cleanupGit(t, f.repo.Path, "rev-parse", "refs/yesman/worktrees/"+wt.CleanupID); got != f.head {
		t.Fatal("checkpoint lost")
	}
	// Simulate an exit after git removal and before the final metadata save.
	records, err := f.s.Yesmen.WorktreeRecords(f.y.Name)
	if err != nil {
		t.Fatal(err)
	}
	records[0].Removed = false
	if err := f.s.Yesmen.SaveWorktreeRecords(f.y.Name, records); err != nil {
		t.Fatal(err)
	}
	if out := f.remove(t, f.lead, removeWorktreeRequest{Dir: wt.Dir}); !out.Removed {
		t.Fatalf("interrupted retry: %+v", out)
	}
	// The ordinary checkout API reconstructs the exact HEAD, not main's
	// squash commit, and allocates a fresh cleanup ID for the new round.
	rebuilt := f.checkout(t, "review", wt.Dir)
	if rebuilt.Head != f.head || rebuilt.CleanupID == wt.CleanupID {
		t.Fatalf("rebuild: %+v", rebuilt)
	}
}

func TestMissingButRegisteredTreeIsRetained(t *testing.T) {
	f := newCleanupFixture(t)
	wt := f.checkout(t, "review", "review")
	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatal(err)
	}
	out := f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir, Completion: f.completion("pass")})
	if out.Removed || !strings.Contains(out.Reason, "still registered") {
		t.Fatalf("missing checkout: %+v", out)
	}
	// Prunable Git records are hidden in normal workspace snapshots, but
	// must still block cleanup's interrupted-success recovery path.
	if exists, err := f.s.Pool.HasWorktreeRegistration(context.Background(), wt.Repo, wt.Path); err != nil || !exists {
		t.Fatalf("registration: %v %v", exists, err)
	}
}

func TestMainAndUserCheckoutsStay(t *testing.T) {
	f := newCleanupFixture(t)
	req := CheckoutRequest{Repo: f.repo.Name, Dir: "primary", Task: "t1", Purpose: "author", Branch: "main"}
	body, _ := json.Marshal(req)
	out, err := f.s.checkout(context.Background(), f.owner, httptest.NewRequest("POST", "/v1/checkout", bytes.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	wt := out.(pool.Worktree)
	f.finishTask(t)
	proof := f.completion("done")
	proof.Head = wt.Head
	read := f.s.ReadPullMerge
	f.s.ReadPullMerge = func(ctx context.Context, repo string, n int) (github.PullMerge, error) {
		pr, err := read(ctx, repo, n)
		pr.Head.Ref, pr.Head.SHA = "main", wt.Head
		return pr, err
	}
	if result := f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir, Completion: proof}); result.Removed || !strings.Contains(result.Reason, "primary branch") {
		t.Fatalf("main: %+v", result)
	}
	// A UI/user checkout has no agent ownership record, even in this workspace.
	user, err := Checkout(context.Background(), f.s.Pool, f.y, CheckoutRequest{Repo: f.repo.Name, Dir: "user", Ref: f.head})
	if err != nil {
		t.Fatal(err)
	}
	if result := f.remove(t, f.lead, removeWorktreeRequest{Dir: user.Dir, Completion: f.completion("pass")}); result.Removed || !strings.Contains(result.Reason, "unknown ownership") {
		t.Fatalf("user tree: %+v", result)
	}
}

func TestCompletionValidationAndOwnerIdentity(t *testing.T) {
	f := newCleanupFixture(t)
	wt := f.checkout(t, "review", "review")
	for _, kind := range []string{"head", "ref", "evidence", "unused", "outcome", "checkout"} {
		t.Run(kind, func(t *testing.T) {
			proof := f.completion("pass")
			req := removeWorktreeRequest{Dir: wt.Dir, Completion: proof}
			switch kind {
			case "head":
				proof.Head = f.head[:10]
			case "ref":
				proof.Ref = ""
			case "evidence":
				proof.Evidence = ""
			case "unused":
				proof.Unused = false
			case "outcome":
				proof.Outcome = "stopped"
			case "checkout":
				req.Checkout = "old-generation"
			}
			if out := f.remove(t, f.owner, req); out.Removed || out.Reason == "" {
				t.Fatalf("invalid completion: %+v", out)
			}
		})
	}
	// Reusing a worker name after removal does not inherit cleanup authority.
	if err := f.s.Yesmen.RemoveWorker(f.y.Name, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Yesmen.AddWorker(f.y.Name, yesmen.Worker{Name: "owner", StartedAt: f.owner.agent.Since.AddDate(0, 0, 1)}); err != nil {
		t.Fatal(err)
	}
	newOwner := f.caller(t, "owner")
	if out := f.remove(t, newOwner, removeWorktreeRequest{Dir: wt.Dir, Completion: f.completion("pass")}); out.Removed || !strings.Contains(out.Reason, "another agent") {
		t.Fatalf("reused owner: %+v", out)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatal("original tree lost")
	}
}

func TestCleanupContextBothRolesAndRecoveryIndex(t *testing.T) {
	f := newCleanupFixture(t)
	wt := f.checkout(t, "review", "review")
	// Persist proof but retain dirty work, so both context and fallback can
	// locate its report without treating process state as completion.
	os.WriteFile(filepath.Join(wt.Path, "untracked"), []byte("keep"), 0644)
	f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir, Completion: f.completion("waiting-release")})
	for _, who := range []caller{f.owner, f.lead} {
		text := BuildContext(context.Background(), f.s.Pool, f.s.Agents, f.y, who.agent, yesmen.Board{})
		for _, want := range []string{"purpose (review, author or release)", "before your final report", "findings handed", "open PR", "squash", "publication and verification finish", "first check whether the old path exists", "exact HEAD", "never force", wt.CleanupID, f.head, "waiting-release", "refs/yesman/worktrees/"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q", who.agent.Role, want)
			}
		}
		if who.agent.Role == agent.RoleLead {
			for _, want := range []string{"one batch cleanup worker", "handoff", "Resume unfinished reviews first", "Stopped, failed, FinishedAt"} {
				if !strings.Contains(text, want) {
					t.Errorf("lead lacks %q", want)
				}
			}
		}
	}
}
