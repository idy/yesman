package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

func previewTree(t *testing.T, d *DesktopWorktrees, y yesmen.Yesman, w *pool.Worktree, eligible bool) WorktreeCleanupPreview {
	t.Helper()
	p, err := d.Preview(t.Context(), y.Name, y.HiredAt, w, nil)
	if err != nil || (p.Token != "") != eligible || len(p.Rows) == 0 {
		t.Fatalf("preview: %+v, %v", p, err)
	}
	return p
}

func confirmTree(t *testing.T, d *DesktopWorktrees, y yesmen.Yesman, token string, removed bool) WorktreeCleanupRow {
	t.Helper()
	rows, err := d.Confirm(t.Context(), y.Name, y.HiredAt, token, nil)
	if err != nil || len(rows) != 1 || rows[0].Removed != removed || (!removed && rows[0].Reason == "") {
		t.Fatalf("confirmation: %+v, %v", rows, err)
	}
	return rows[0]
}

func TestDesktopWorktreeCanceledTaskWithoutPR(t *testing.T) {
	f := newCleanupFixture(t)
	wt := f.checkout(t, "author", "gizmoth-artifact-rows")
	d := &DesktopWorktrees{Server: f.s}
	// A real older registration: both Git pointers and its original nonce,
	// but no creation-time inode and no task ownerSince. No adoption on read.
	records, _ := f.s.Yesmen.WorktreeRecords(f.y.Name)
	records[0].Root = nil
	if err := f.s.Yesmen.SaveWorktreeRecords(f.y.Name, records); err != nil {
		t.Fatal(err)
	}
	status := yesmen.TaskDropped
	if _, err := f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: &status}}}); err != nil {
		t.Fatal(err)
	}
	file := f.y.BoardFile()
	b, err := f.s.Yesmen.Board(f.y.Name)
	if err != nil {
		t.Fatal(err)
	}
	b.Tasks[0].OwnerSince = time.Time{}
	data, _ := json.Marshal(b)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	f.s.ReadPullMerge = func(context.Context, string, int) (github.PullMerge, error) {
		t.Fatal("single queried a PR")
		return github.PullMerge{}, nil
	}
	// An unpublished commit made before the preview is preserved too.
	cleanupGit(t, wt.Path, "commit", "--allow-empty", "-qm", "Only local")
	wt.Head = cleanupGit(t, wt.Path, "rev-parse", "HEAD")
	refs := cleanupGit(t, f.repo.Path, "for-each-ref", "--format=%(refname) %(objectname)")
	p := previewTree(t, d, f.y, nil, false)
	if !strings.Contains(p.Rows[0].Reason, "No completion") {
		t.Fatal(p)
	}
	p = previewTree(t, d, f.y, &wt, true)
	d.Cancel(p.Token)
	if _, err := d.Confirm(t.Context(), f.y.Name, f.y.HiredAt, p.Token, nil); err == nil {
		t.Fatal("canceled token worked")
	}
	p = previewTree(t, d, f.y, &wt, true)
	for _, who := range []yesmen.Yesman{{Name: "other", HiredAt: f.y.HiredAt}, {Name: f.y.Name, HiredAt: f.y.HiredAt.Add(time.Second)}} {
		if _, err := d.Confirm(t.Context(), who.Name, who.HiredAt, p.Token, nil); err == nil {
			t.Fatal("cross-workspace confirmation")
		}
	}
	if after := cleanupGit(t, f.repo.Path, "for-each-ref", "--format=%(refname) %(objectname)"); after != refs {
		t.Fatal("preview changed refs")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	succeeded := 0
	for range 2 {
		wg.Go(func() {
			rows, err := d.Confirm(t.Context(), f.y.Name, f.y.HiredAt, p.Token, nil)
			if err == nil && len(rows) == 1 && rows[0].Removed && rows[0].Reason == "" {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if succeeded != 1 {
		t.Fatalf("%d confirmations succeeded", succeeded)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("directory: %v", err)
	}
	if ok, err := f.s.Pool.HasWorktreeRegistration(t.Context(), wt.Repo, wt.Path); ok || err != nil {
		t.Fatalf("registration: %t %v", ok, err)
	}
	assertCleanupRefsPreserved(t, f, refs, wt)
	records, _ = f.s.Yesmen.WorktreeRecords(f.y.Name)
	if len(records) != 1 || records[0].Root != nil || records[0].Completion != nil || !records[0].Removed || !strings.Contains(records[0].RetiredReason, wt.Head) {
		t.Fatalf("history: %+v", records)
	}
}

func TestDesktopWorktreeRevalidatesEachProtection(t *testing.T) {
	f := newCleanupFixture(t)
	wt := f.checkout(t, "author", "work")
	f.finishTask(t)
	d := &DesktopWorktrees{Server: f.s}
	private := cleanupGit(t, wt.Path, "rev-parse", "--absolute-git-dir")
	put := func(path string) func(*testing.T) {
		return func(t *testing.T) {
			if err := os.WriteFile(path, []byte("retained\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Remove(path) })
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*testing.T)
	}{
		{"ignored", put(filepath.Join(wt.Path, "ignored"))}, {"untracked", put(filepath.Join(wt.Path, "new"))},
		{"index-lock", put(filepath.Join(private, "index.lock"))}, {"private-notes", put(filepath.Join(private, "local-notes"))}, {"merge", put(filepath.Join(private, "MERGE_HEAD"))},
		{"lock", func(t *testing.T) {
			cleanupGit(t, f.repo.Path, "worktree", "lock", wt.Path)
			t.Cleanup(func() { cleanupGit(t, f.repo.Path, "worktree", "unlock", wt.Path) })
		}},
		{"empty-unknown-directory", func(t *testing.T) {
			path := filepath.Join(wt.Path, "local-only")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Remove(path) })
		}},
		{"hidden-index", func(t *testing.T) {
			cleanupGit(t, wt.Path, "update-index", "--assume-unchanged", "feature")
			t.Cleanup(func() { cleanupGit(t, wt.Path, "update-index", "--no-assume-unchanged", "feature") })
		}},
		{"skip-worktree", func(t *testing.T) {
			cleanupGit(t, wt.Path, "update-index", "--skip-worktree", "feature")
			t.Cleanup(func() { cleanupGit(t, wt.Path, "update-index", "--no-skip-worktree", "feature") })
		}},
		{"primary", func(t *testing.T) {
			cleanupGit(t, wt.Path, "switch", "-c", "main")
			t.Cleanup(func() { cleanupGit(t, wt.Path, "switch", wt.Branch) })
		}},
		{"new-head", func(t *testing.T) {
			cleanupGit(t, wt.Path, "commit", "--allow-empty", "-qm", "Later")
			t.Cleanup(func() { cleanupGit(t, wt.Path, "reset", "--hard", wt.Head) })
		}},
		{"paused-task", func(t *testing.T) {
			_, err := f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Wait: &yesmen.WaitEdit{Kind: yesmen.WaitPaused, Reason: "user paused"}}}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { f.finishTask(t) })
		}},
		{"changed-checkpoint", func(t *testing.T) {
			ref := "refs/yesman/worktrees/" + wt.CleanupID
			cleanupGit(t, f.repo.Path, "update-ref", ref, f.merge)
			t.Cleanup(func() {
				if got := cleanupGit(t, f.repo.Path, "rev-parse", ref); got != f.merge {
					t.Fatal("moved another checkpoint")
				}
				cleanupGit(t, f.repo.Path, "update-ref", "-d", ref)
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := previewTree(t, d, f.y, &wt, true)
			tc.change(t)
			confirmTree(t, d, f.y, p.Token, false)
			previewTree(t, d, f.y, &wt, false)
			if _, err := os.Stat(filepath.Join(wt.Path, "README")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDesktopWorktreeReplacementAndLegacy(t *testing.T) {
	for _, kind := range []string{"new-nonce", "copied-pointer", "wrong-backpointer", "missing-marker"} {
		t.Run(kind, func(t *testing.T) {
			f := newCleanupFixture(t)
			wt := f.checkout(t, "review", "review")
			f.finishTask(t)
			d := &DesktopWorktrees{Server: f.s}
			recs, _ := f.s.Yesmen.WorktreeRecords(f.y.Name)
			recs[0].Root = nil
			if err := f.s.Yesmen.SaveWorktreeRecords(f.y.Name, recs); err != nil {
				t.Fatal(err)
			}
			private := cleanupGit(t, wt.Path, "rev-parse", "--absolute-git-dir")
			p := previewTree(t, d, f.y, &wt, true)
			switch kind {
			case "new-nonce":
				if err := f.s.Pool.RemoveWorktree(t.Context(), wt.Path, false); err != nil {
					t.Fatal(err)
				}
				if _, err := Checkout(t.Context(), f.s.Pool, f.y, CheckoutRequest{Repo: wt.Repo, Dir: wt.Dir, Ref: wt.Head}); err != nil {
					t.Fatal(err)
				}
			case "copied-pointer":
				if err := os.Rename(wt.Path, wt.Path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.CopyFS(wt.Path, os.DirFS(wt.Path+".saved")); err != nil {
					t.Fatal(err)
				}
			case "wrong-backpointer":
				if err := os.WriteFile(filepath.Join(private, "gitdir"), []byte(filepath.Join(f.y.Path, "moved", ".git")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-marker":
				if err := os.Remove(filepath.Join(private, "yesman-instance")); err != nil {
					t.Fatal(err)
				}
			}
			confirmTree(t, d, f.y, p.Token, false)
			if kind == "wrong-backpointer" {
				again := previewTree(t, d, f.y, nil, false)
				if again.Rows[0].Reason != noRegistration {
					t.Fatal(again)
				}
			}
			if kind == "missing-marker" {
				again := previewTree(t, d, f.y, nil, false)
				if again.Rows[0].Reason != unknownInstance {
					t.Fatal(again)
				}
				if _, err := os.Stat(filepath.Join(private, "yesman-instance")); !os.IsNotExist(err) {
					t.Fatalf("marker: %v", err)
				}
			}
			if _, err := os.Stat(filepath.Join(wt.Path, "README")); err != nil {
				t.Fatal(err)
			}
			recs, _ = f.s.Yesmen.WorktreeRecords(f.y.Name)
			if recs[0].Removed || recs[0].Root != nil {
				t.Fatal("adopted or retired record", recs)
			}
		})
	}
}

func TestDesktopWorktreeBulkAndAlternateSourceBranch(t *testing.T) {
	for _, branch := range []string{"feature", "integration/local"} {
		t.Run(branch, func(t *testing.T) {
			f := newCleanupFixture(t)
			wt, err := f.s.checkoutOwned(t.Context(), f.owner, f.y, CheckoutRequest{Repo: f.repo.Name, Dir: "delivered", Branch: branch, Base: f.head, Purpose: "author", Task: "t1"})
			if err != nil {
				t.Fatal(err)
			}
			f.finishTask(t)
			d := &DesktopWorktrees{Server: f.s}
			proof := f.completion("done")
			if branch == "feature" {
				res := f.remove(t, f.lead, removeWorktreeRequest{Dir: wt.Dir, Completion: proof, Handoff: f.delegate.agent.Name})
				if res.Removed || !strings.Contains(res.Reason, "handed off") {
					t.Fatal(res)
				}
				p := previewTree(t, d, f.y, nil, true)
				read := f.s.ReadPullMerge
				f.s.ReadPullMerge = func(c context.Context, repo string, n int) (github.PullMerge, error) {
					pr, err := read(c, repo, n)
					pr.Merged = false
					return pr, err
				}
				confirmTree(t, d, f.y, p.Token, false)
				f.s.ReadPullMerge = read
				p = previewTree(t, d, f.y, nil, true)
				confirmTree(t, d, f.y, p.Token, true)
			} else {
				// Same merged SHA, different integration branch. Preserve the
				// rejected proposal. Only a separately confirmed single can act.
				res := f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir, Completion: proof})
				if res.Removed || len(res.Record.Attempts) != 1 {
					t.Fatal(res)
				}
				previewTree(t, d, f.y, nil, false)
				p := previewTree(t, d, f.y, &wt, true)
				confirmTree(t, d, f.y, p.Token, true)
			}
			if got := cleanupGit(t, f.repo.Path, "rev-parse", "refs/heads/"+branch); got != wt.Head {
				t.Fatal("lost local branch", got)
			}
			if got := cleanupGit(t, f.repo.Path, "rev-parse", "refs/yesman/worktrees/"+wt.CleanupID); got != f.head {
				t.Fatal("lost source", got)
			}
		})
	}
}

func TestDesktopWorktreeRehireAndStaleRoot(t *testing.T) {
	f := newCleanupFixture(t)
	wt := f.checkout(t, "review", "review")
	f.finishTask(t)
	d := &DesktopWorktrees{Server: f.s}
	p := previewTree(t, d, f.y, &wt, true)
	archived, err := f.s.Yesmen.Archive(f.y.Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := f.s.Yesmen.Hire(f.y.Name)
	if err != nil || fresh.HiredAt.Equal(f.y.HiredAt) {
		t.Fatal("new hire", err)
	}
	if err := os.Mkdir(filepath.Join(fresh.Path, wt.Dir), 0700); err != nil {
		t.Fatal(err)
	}
	confirmTree(t, d, f.y, p.Token, false)
	for _, path := range []string{filepath.Join(archived, wt.Dir, "README"), filepath.Join(fresh.Path, wt.Dir)} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("old confirmation touched a different resource", err)
		}
	}
}

func TestDesktopWorktreeActiveOwnerAndAgentAuthority(t *testing.T) {
	x := newWorktreeFixture(t)
	wt := managedCheckout(t, x, "author")
	status := yesmen.TaskDone
	if _, err := x.f.store.ChangeBoard(x.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: &status}}}); err != nil {
		t.Fatal(err)
	}
	d := &DesktopWorktrees{Server: x.f.srv}
	p := previewTree(t, d, x.y, &wt, false)
	if !strings.Contains(p.Rows[0].Reason, "active") {
		t.Fatal(p)
	}
	for _, path := range []string{"preview", "confirm"} {
		if res := managementPost(t, x, x.w.Token, "/v1/worktrees/"+path, map[string]any{}); res.Code != 404 {
			t.Fatal(res.Code)
		}
	}
	res := managementPost(t, x, x.w.Token, "/v1/worktrees/remove", map[string]any{"dir": wt.Dir, "checkout": wt.CleanupID, "manual": true, "confirmed": true})
	var got cleanupResult
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &got) != nil || got.Removed || !strings.Contains(got.Reason, "no saved completion") {
		t.Fatal(res.Code, res.Body.String())
	}
}
