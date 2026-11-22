package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/yesmen"
)

func ordinaryTaskEdit(t *testing.T, x *worktreeFixture, token, status string, keep bool) {
	t.Helper()
	e := map[string]any{"id": "t1", "status": status}
	if keep {
		e["worktrees"] = []any{}
	}
	// Old tool versions sent this compatibility field. It must no longer
	// force a per-checkout return as a condition of marking the task done.
	r := managementPost(t, x, token, "/v1/board", map[string]any{"reportWorktrees": true, "update": []any{e}})
	if r.Code != 200 {
		t.Fatalf("board update: %d %s", r.Code, r.Body)
	}
}

func TestCreatingTaskDoneWithoutReturn(t *testing.T) {
	for _, purpose := range []string{"review", "author", "release"} {
		t.Run(purpose, func(t *testing.T) {
			x, w, _ := returnFixture(t, purpose, "")
			s := x.f.srv
			s.ReadPullMerge = func(context.Context, string, int) (github.PullMerge, error) {
				t.Error("ordinary cleanup queried a merge")
				return github.PullMerge{}, errors.New("unexpected merge lookup")
			}
			s.ReadPublishedTag = func(context.Context, string, string) (string, error) {
				t.Error("ordinary cleanup queried a publication")
				return "", errors.New("unexpected publication lookup")
			}
			if purpose != "review" {
				cleanupGit(t, w.Path, "commit", "--allow-empty", "-qm", "Only local source")
				w.Head = cleanupGit(t, w.Path, "rev-parse", "HEAD")
			}
			lead := x.f.start("alice/lead")
			x.f.m.Stop(x.me.agent.ID)
			if got := s.reconcileCheckout(t.Context(), x.y, w.CleanupID); got != collectedOrIneligible {
				t.Fatal("open task scheduled cleanup", got)
			}
			assertRetained(t, x, w)
			refs := cleanupGit(t, w.Path, "for-each-ref", "--format=%(refname) %(objectname)")
			r := NewWorktreeReclaimer(s)
			s.Reclaimer = r
			removed := make(chan struct{}, 1)
			s.Changed = func() { removed <- struct{}{} }
			r.Start()
			defer r.Close()
			ordinaryTaskEdit(t, x, lead.Token, yesmen.TaskDone, false)
			select {
			case <-removed:
			case <-time.After(15 * time.Second):
				t.Fatal("ordinary done did not wake reclamation")
			}
			rec := recordOf(t, x, w.Dir)
			if !rec.Removed || rec.Completion != nil || !rec.BoardRound.IsZero() || rec.CleanupHead != w.Head || rec.CreatedFor == nil || !rec.CreatedFor.HiredAt.Equal(x.y.HiredAt) {
				t.Fatalf("registry: %+v", rec)
			}
			assertCleanupRefsPreserved(t, x.cf, refs, w)
		})
	}
}

func TestCreatingTaskReopensDuringOpenPullCheck(t *testing.T) {
	x, w, _ := returnFixture(t, "author", "")
	s := x.f.srv
	ordinaryTaskEdit(t, x, x.w.Token, yesmen.TaskDone, false)
	if got := s.reconcileCheckout(t.Context(), x.y, w.CleanupID); got != waitForOwner {
		t.Fatal("running owner did not hold checkout", got)
	}
	x.f.m.Stop(x.me.agent.ID)
	read := s.HasOpenPull
	s.HasOpenPull = func(context.Context, string, string, string) (bool, error) {
		doing, done := yesmen.TaskDoing, yesmen.TaskDone
		changeDesktopTask(t, s.Yesmen, x.y, yesmen.TaskEdit{ID: "t1", Status: &doing}, yesmen.TaskEdit{ID: "t1", Status: &done})
		return false, nil
	}
	if got := s.reconcileCheckout(t.Context(), x.y, w.CleanupID); got != retryLater {
		t.Fatal("stale task close was not retried", got)
	}
	assertRetained(t, x, w)
	s.HasOpenPull = read
	if got := s.reconcileCheckout(t.Context(), x.y, w.CleanupID); got != collectedOrIneligible || !recordOf(t, x, w.Dir).Removed {
		t.Fatal("new close did not authorize cleanup", got)
	}
}

func TestCreatingTaskRegistrationAfterTaskChanged(t *testing.T) {
	f := newCleanupFixture(t)
	b, err := f.s.Yesmen.Board(f.y.Name)
	if err != nil {
		t.Fatal(err)
	}
	admitted := b.Tasks[0]
	w, err := Checkout(t.Context(), f.s.Pool, f.y, CheckoutRequest{Repo: f.repo.Name, Dir: "unregistered", Ref: f.head})
	if err != nil {
		t.Fatal(err)
	}
	// Deliver the creating task between Git creation and lifecycle publication.
	f.finishTask(t)
	rec := yesmen.WorktreeRecord{ID: w.CleanupID, Dir: w.Dir, Repo: w.Repo, Owner: admitted.Owner,
		OwnerSince: admitted.OwnerSince, Purpose: "review", Task: admitted.ID, Head: w.Head,
		CreatedFor: &yesmen.WorktreeCreation{Workspace: f.y.Name, HiredAt: f.y.HiredAt, TaskAt: admitted.CreatedAt}}
	if err := f.s.Yesmen.SaveCheckout(f.y, []yesmen.WorktreeRecord{rec}, &admitted); err == nil {
		t.Fatal("published stale creation provenance")
	}
	d := &DesktopWorktrees{Server: f.s}
	if p := previewTree(t, d, f.y, nil, false); p.Rows[0].Reason != unknownOwner {
		t.Fatal(p)
	}
	if registered, err := f.s.Pool.HasWorktreeRegistration(t.Context(), w.Repo, w.Path); err != nil || !registered {
		t.Fatal("registration was not retained", err)
	}
}

func TestCreatingTaskCleanupRecovery(t *testing.T) {
	for _, state := range []string{"complete", "partial", "moved"} {
		t.Run(state, func(t *testing.T) {
			x, w, _ := returnFixture(t, "review", "")
			s := x.f.srv
			ordinaryTaskEdit(t, x, x.w.Token, yesmen.TaskDone, false)
			x.f.m.Stop(x.me.agent.ID)
			if state == "complete" {
				s.reconcileCheckout(t.Context(), x.y, w.CleanupID)
				if err := s.Pool.CheckWorktreeCheckpoint(t.Context(), w); err != nil {
					t.Fatal(err)
				}
			} else {
				// Simulate interrupted cleanup after the exact receipt and
				// checkpoint became durable but before Git removed the tree.
				cleanupGit(t, x.cf.repo.Path, "update-ref", "refs/yesman/worktrees/"+w.CleanupID, w.Head)
				if state == "moved" {
					cleanupGit(t, x.cf.repo.Path, "worktree", "move", w.Path, filepath.Join(x.y.Path, "relocated"))
				} else if err := os.Rename(w.Path, filepath.Join(x.y.Path, "partial")); err != nil {
					t.Fatal(err)
				}
			}
			recs, err := s.Yesmen.WorktreeRecords(x.y.Name)
			if err != nil || len(recs) != 1 || recs[0].Completion != nil {
				t.Fatal(recs, err)
			}
			// A final registry replacement did not complete. The Board may
			// now have reopened. Recovery can only fix past bookkeeping.
			recs[0].Removed, recs[0].CleanupHead = false, w.Head
			if err := s.Yesmen.SaveWorktreeRecords(x.y.Name, recs); err != nil {
				t.Fatal(err)
			}
			doing := yesmen.TaskDoing
			changeDesktopTask(t, s.Yesmen, x.y, yesmen.TaskEdit{ID: "t1", Status: &doing})
			s.reconcileCheckout(t.Context(), x.y, w.CleanupID)
			rec := recordOf(t, x, w.Dir)
			if rec.Removed != (state == "complete") || rec.Completion != nil {
				t.Fatalf("recovery state %s: %+v", state, rec)
			}
		})
	}
}

func TestCreatingTaskBulkReopenKeepAndReassign(t *testing.T) {
	for _, which := range []string{"reclose", "keep", "reassign", "legacy-proven", "legacy-rootless", "stale-task", "stale-hire", "moved", "draft", "open-pr", "failed"} {
		t.Run(which, func(t *testing.T) {
			x, w, _ := returnFixture(t, "author", "")
			s := x.f.srv
			d := &DesktopWorktrees{Server: s}
			ordinaryTaskEdit(t, x, x.w.Token, yesmen.TaskDone, which == "keep")
			x.f.m.Stop(x.me.agent.ID)
			switch which {
			case "legacy-proven", "legacy-rootless", "stale-task", "stale-hire":
				recs, err := s.Yesmen.WorktreeRecords(x.y.Name)
				if err != nil {
					t.Fatal(err)
				}
				switch which {
				case "legacy-proven":
					recs[0].CreatedFor = nil
				case "legacy-rootless":
					recs[0].CreatedFor, recs[0].Root = nil, nil
				case "stale-task":
					recs[0].CreatedFor.TaskAt = recs[0].CreatedFor.TaskAt.Add(-time.Second)
				case "stale-hire":
					recs[0].CreatedFor.HiredAt = recs[0].CreatedFor.HiredAt.Add(-time.Second)
				}
				if err := s.Yesmen.SaveWorktreeRecords(x.y.Name, recs); err != nil {
					t.Fatal(err)
				}
			case "reclose", "keep", "reassign":
				var stale WorktreeCleanupPreview
				if which == "reclose" {
					stale = previewTree(t, d, x.y, nil, true)
				}
				doing, done := yesmen.TaskDoing, yesmen.TaskDone
				edits := []yesmen.TaskEdit{{ID: "t1", Status: &doing}, {ID: "t1", Status: &done}}
				if which == "reassign" {
					emptyOwner := ""
					edits[0].Owner = &emptyOwner
				}
				changeDesktopTask(t, s.Yesmen, x.y, edits...)
				if stale.Token != "" {
					if row := confirmTree(t, d, x.y, stale.Token, false); row.Reason != staleDecision {
						t.Fatal(row)
					}
				}
			case "draft":
				p := previewTree(t, d, x.y, nil, true)
				release, err := s.Pool.HoldWorktreeEditor(w.Path, w.Repo, w.CleanupID)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				if row := confirmTree(t, d, x.y, p.Token, false); !strings.Contains(row.Reason, "editor is open") {
					t.Fatal(row)
				}
				s.reconcileCheckout(t.Context(), x.y, w.CleanupID)
				assertRetained(t, x, w)
				release()
			case "open-pr":
				cleanupGit(t, w.Path, "branch", "--set-upstream-to=origin/main", w.Branch)
				s.HasOpenPull = func(_ context.Context, _, _, ref string) (bool, error) { return ref == "main", nil }
			case "moved":
				cleanupGit(t, x.cf.repo.Path, "worktree", "move", w.Path, filepath.Join(x.y.Path, "elsewhere"))
			case "failed":
				if err := s.Yesmen.FailWorker(x.y.Name, x.me.agent.Name, x.me.agent.Since, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			switch which {
			case "legacy-rootless", "stale-task", "stale-hire", "open-pr", "failed", "moved", "keep":
				p := previewTree(t, d, x.y, nil, false)
				s.reconcileCheckout(t.Context(), x.y, w.CleanupID)
				if which == "moved" {
					if _, err := os.Stat(filepath.Join(x.y.Path, "elsewhere", "README")); err != nil {
						t.Fatal(err)
					}
					return
				}
				assertRetained(t, x, w)
				if which == "keep" {
					if p.Rows[0].Reason != boardKeep {
						t.Fatal(p.Rows[0])
					}
					p = previewTree(t, d, x.y, &w, true)
					if p.Rows[0].KeepWarning != keepWarning {
						t.Fatal(p.Rows[0])
					}
					confirmTree(t, d, x.y, p.Token, true)
				}
			default:
				p := previewTree(t, d, x.y, nil, true)
				confirmTree(t, d, x.y, p.Token, true)
				if rec := recordOf(t, x, w.Dir); rec.Completion != nil || !rec.Removed || rec.CleanupHead != w.Head {
					t.Fatalf("removal provenance: %+v", rec)
				}
			}
		})
	}
}
