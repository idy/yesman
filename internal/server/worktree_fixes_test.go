package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

func retainReviewCompletion(t *testing.T, f *cleanupFixture, wt pool.Worktree) {
	t.Helper()
	err := f.s.Agents.WithWorktreeOwner(f.owner.agent.ID, f.lead.agent.ID, func() error {
		out := f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir, Completion: f.completion("pass")})
		if out.Removed || !strings.Contains(out.Reason, "in progress") {
			return fmt.Errorf("completion not retained: %+v", out)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCleanupRejectsReplacementGitInstances(t *testing.T) {
	for _, scenario := range []string{"retained-then-manually-replaced", "git-removed-before-metadata-save"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCleanupFixture(t)
			wt := f.checkout(t, "review", "review")
			if scenario == "retained-then-manually-replaced" {
				retainReviewCompletion(t, f, wt)
				if err := f.s.Pool.RemoveWorktree(context.Background(), wt.Path, false); err != nil {
					t.Fatal(err)
				}
			} else {
				if out := f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir, Completion: f.completion("pass")}); !out.Removed {
					t.Fatal(out.Reason)
				}
				records, err := f.s.Yesmen.WorktreeRecords(f.y.Name)
				if err != nil {
					t.Fatal(err)
				}
				records[0].Removed = false // app exits after git removal, before metadata commit
				if err := f.s.Yesmen.SaveWorktreeRecords(f.y.Name, records); err != nil {
					t.Fatal(err)
				}
			}
			// The real UI/user entry point creates the same path, repo and HEAD
			// without adopting the prior owner's workspace lifecycle record.
			replacement, err := Checkout(context.Background(), f.s.Pool, f.y, CheckoutRequest{Repo: f.repo.Name, Ref: f.head, Dir: wt.Dir})
			if err != nil {
				t.Fatal(err)
			}
			if replacement.CleanupID == "" || replacement.CleanupID == wt.CleanupID {
				t.Fatal("UI reused the original Git generation")
			}
			for _, who := range []caller{f.owner, f.lead} {
				out := f.remove(t, who, removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID})
				if out.Removed || !strings.Contains(out.Reason, "instance changed") {
					t.Fatalf("replacement removed: %+v", out)
				}
			}
			if got := cleanupGit(t, replacement.Path, "status", "--porcelain", "--untracked-files=all", "--ignored"); got != "" {
				t.Fatalf("private marker dirtied the tree: %s", got)
			}
			records, _ := f.s.Yesmen.WorktreeRecords(f.y.Name)
			if len(records) != 1 || records[0].ID != wt.CleanupID || records[0].Removed {
				t.Fatalf("replacement adopted: %+v", records)
			}
			if registered, err := f.s.Pool.HasWorktreeRegistration(context.Background(), replacement.Repo, replacement.Path); err != nil || !registered {
				t.Fatalf("UI registration lost: %v %v", registered, err)
			}
		})
	}
}

func TestStaleCleanupRacesOwnerlessCheckout(t *testing.T) {
	f := newCleanupFixture(t)
	wt := f.checkout(t, "review", "review")
	retainReviewCompletion(t, f, wt)
	if err := f.s.Pool.RemoveWorktree(context.Background(), wt.Path, false); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 9)
	body, _ := json.Marshal(removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID})
	for range 8 {
		go func() {
			<-start
			_, err := f.s.removeWorktree(context.Background(), f.lead, httptest.NewRequest("POST", "/v1/worktrees/remove", bytes.NewReader(body)))
			errs <- err
		}()
	}
	replacements := make(chan pool.Worktree, 1)
	go func() {
		<-start
		wt, err := Checkout(context.Background(), f.s.Pool, f.y, CheckoutRequest{Repo: f.repo.Name, Ref: f.head, Dir: wt.Dir})
		replacements <- wt
		errs <- err
	}()
	close(start)
	for range 9 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	replacement := <-replacements
	if replacement.CleanupID == "" || replacement.CleanupID == wt.CleanupID {
		t.Fatal("instance was reused")
	}
	if _, err := os.Stat(filepath.Join(replacement.Path, "README")); err != nil {
		t.Fatal("stale request deleted UI replacement", err)
	}
	if out := f.remove(t, f.lead, removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID}); out.Removed {
		t.Fatal("later replay deleted UI replacement")
	}
}

func TestBranchCompletionCanCorrectRejectedProof(t *testing.T) {
	for _, purpose := range []string{"author", "release"} {
		for _, wrong := range []string{"head", "ref"} {
			t.Run(purpose+"/"+wrong, func(t *testing.T) {
				f := newCleanupFixture(t)
				wt := f.checkout(t, purpose, "work")
				f.finishTask(t)
				proof := f.completion("done")
				if wrong == "head" {
					proof.Head = f.merge
				} else {
					proof.Ref = "https://github.com/acme/demo/pull/8"
				}
				for range 2 {
					if out := f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir, Completion: proof}); out.Removed {
						t.Fatal("bad proof removed source")
					}
				}
				records, _ := f.s.Yesmen.WorktreeRecords(f.y.Name)
				if records[0].Completion != nil || len(records[0].Attempts) != 1 || records[0].Attempts[0].Completion.Head != proof.Head || records[0].Attempts[0].Completion.Ref != proof.Ref || records[0].Attempts[0].Reason == "" {
					t.Fatalf("failed evidence lost or frozen: %+v", records[0])
				}
				if out := f.remove(t, f.delegate, removeWorktreeRequest{Dir: wt.Dir, Completion: f.completion("done")}); out.Removed {
					t.Fatal("non-owner corrected another actor's proof")
				}
				if out := f.remove(t, f.lead, removeWorktreeRequest{Dir: wt.Dir, Checkout: strings.Repeat("0", 32), Completion: f.completion("done")}); out.Removed {
					t.Fatal("old instance corrected proof")
				}
				certifier := f.owner
				if wrong == "ref" {
					certifier = f.lead
				}
				if out := f.remove(t, certifier, removeWorktreeRequest{Dir: wt.Dir, Completion: f.completion("done")}); !out.Removed {
					t.Fatal("corrected merged proof blocked", out.Reason)
				}
				records, _ = f.s.Yesmen.WorktreeRecords(f.y.Name)
				if !records[0].Removed || records[0].Completion.Head != f.head || len(records[0].Attempts) != 1 || records[0].Attempts[0].Completion.Evidence != proof.Evidence {
					t.Fatalf("correction discarded history: %+v", records[0])
				}
			})
		}
	}
}

func TestLegacyAndCurrentCheckoutReuseAbsentRecords(t *testing.T) {
	for _, role := range []string{"worker", "lead"} {
		for _, next := range []string{"legacy-omitted", "current-review"} {
			for _, naming := range []string{"explicit-dir", "default-dir"} {
				t.Run(role+"/"+next+"/"+naming, func(t *testing.T) {
					f := newCleanupFixture(t)
					who, other := f.owner, f.lead
					if role == "lead" {
						who, other = f.lead, f.owner
						owner := who.agent.Name
						if _, err := f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Owner: &owner}}}); err != nil {
							t.Fatal(err)
						}
					}
					call := func(actor caller, req CheckoutRequest) (pool.Worktree, error) {
						body, _ := json.Marshal(req)
						out, err := f.s.checkout(context.Background(), actor, httptest.NewRequest("POST", "/v1/checkout", bytes.NewReader(body)))
						if err != nil {
							return pool.Worktree{}, err
						}
						return out.(pool.Worktree), nil
					}
					req := CheckoutRequest{Repo: f.repo.Name, Ref: f.head}
					if naming == "explicit-dir" {
						req.Dir = "legacy"
					}
					wt, err := call(who, req)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := call(who, req); err == nil {
						t.Fatal("adopted existing unknown tree")
					}
					if err := f.s.Pool.RemoveWorktree(context.Background(), wt.Path, false); err != nil {
						t.Fatal(err)
					}
					if _, err := call(other, req); err == nil {
						t.Fatal("other actor retired owner's record")
					}
					err = f.s.Agents.WithWorktreeOwner(who.agent.ID, other.agent.ID, func() error {
						if _, err := call(who, req); err == nil {
							return fmt.Errorf("retired an actively reserved owner")
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					if next == "current-review" {
						req.Dir, req.Purpose, req.Task = wt.Dir, "review", "t1"
					}
					rebuilt, err := call(who, req)
					if err != nil {
						t.Fatal("missing legacy record pinned dir", err)
					}
					if rebuilt.CleanupID == "" || rebuilt.CleanupID == wt.CleanupID {
						t.Fatal("legacy instance was reused")
					}
					records, _ := f.s.Yesmen.WorktreeRecords(f.y.Name)
					if len(records) != 2 || !records[0].Removed || records[0].RetiredReason == "" || records[0].Completion != nil || records[1].Completion != nil || records[1].Removed {
						t.Fatalf("not a fresh lifecycle: %+v", records)
					}
				})
			}
		}
	}
}

func TestMissingLegacyRegistrationCannotBeRetired(t *testing.T) {
	f := newCleanupFixture(t)
	req := CheckoutRequest{Repo: f.repo.Name, Ref: f.head, Dir: "legacy"}
	body, _ := json.Marshal(req)
	out, err := f.s.checkout(context.Background(), f.owner, httptest.NewRequest("POST", "/v1/checkout", bytes.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	wt := out.(pool.Worktree)
	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.checkout(context.Background(), f.owner, httptest.NewRequest("POST", "/v1/checkout", bytes.NewReader(body))); err == nil {
		t.Fatal("reused missing but registered tree")
	}
	records, _ := f.s.Yesmen.WorktreeRecords(f.y.Name)
	if len(records) != 1 || records[0].Removed || records[0].Completion != nil {
		t.Fatal("unfinished legacy record altered", records)
	}
}
