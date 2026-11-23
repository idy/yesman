package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

func TestDesktopBulkIdleBlockedTaskAndLateOwner(t *testing.T) {
	f := newCleanupFixture(t)
	w := f.checkout(t, "author", "idle-blocked")
	blocked, doing := yesmen.TaskBlocked, yesmen.TaskDoing
	changeDesktopTask(t, f.s.Yesmen, f.y, yesmen.TaskEdit{ID: "t1", Status: &blocked})
	owner := f.owner.agent.Name
	title := "Unrelated open task by the same owner"
	if _, err := f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: &title, Owner: &owner, Status: &doing}}}); err != nil {
		t.Fatal(err)
	}
	d := &DesktopWorktrees{Server: f.s}
	previewTree(t, d, f.y, &w, false)
	if err := os.WriteFile(filepath.Join(w.Path, "README"), []byte("idle local working copy"), 0600); err != nil {
		t.Fatal(err)
	}
	p := previewTree(t, d, f.y, nil, true)
	if p.Rows[0].TaskStatus != blocked {
		t.Fatal(p)
	}
	changeDesktopTask(t, f.s.Yesmen, f.y, yesmen.TaskEdit{ID: "t1", Status: &doing})
	if row := confirmTree(t, d, f.y, p.Token, false); row.Reason != staleDecision {
		t.Fatal(row)
	}
	changeDesktopTask(t, f.s.Yesmen, f.y, yesmen.TaskEdit{ID: "t1", Status: &blocked})
	p = previewTree(t, d, f.y, nil, true)
	// Another holder has already reserved this owner by the time the preview
	// is confirmed. Use the same Manager admission boundary as real startup.
	if err := f.s.Agents.WithWorktreeOwner(agent.ID(f.y.Name, owner), "", func() error {
		if row := confirmTree(t, d, f.y, p.Token, false); !strings.Contains(row.Reason, "reserved") {
			t.Fatal(row)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p = previewTree(t, d, f.y, nil, true)
	confirmTree(t, d, f.y, p.Token, true)
	b, err := f.s.Yesmen.Board(f.y.Name)
	if err != nil || b.Tasks[0].Status != blocked {
		t.Fatal("cleanup altered the Board", b, err)
	}
}

func TestDesktopBulkDeadPointerTracking(t *testing.T) {
	f := newCleanupFixture(t)
	w := f.checkout(t, "author", "older-local")
	f.finishTask(t)
	private := cleanupGit(t, w.Path, "rev-parse", "--absolute-git-dir")
	kept := filepath.Join(t.TempDir(), "kept-registration")
	if err := os.Rename(private, kept); err != nil {
		t.Fatal(err)
	}
	// Track a branch with a different name from the original author branch.
	cleanupGit(t, f.repo.Path, "branch", "--set-upstream-to=origin/main", w.Branch)
	open := true
	checked := 0
	f.s.HasOpenPull = func(_ context.Context, _, _, branch string) (bool, error) {
		if branch == "main" {
			checked++
			return open, nil
		}
		return false, nil
	}
	d := &DesktopWorktrees{Server: f.s}
	if p := previewTree(t, d, f.y, nil, false); checked != 1 || !strings.Contains(p.Rows[0].Reason, "open pull request") {
		t.Fatal(p, checked)
	}
	open = false
	p := previewTree(t, d, f.y, nil, true)
	open = true
	if row := confirmTree(t, d, f.y, p.Token, false); checked != 3 || !strings.Contains(row.Reason, "open pull request") {
		t.Fatal(row, checked)
	}
	open = false
	p = previewTree(t, d, f.y, nil, true)
	cleanupGit(t, f.repo.Path, "branch", "--set-upstream-to=origin/feature", w.Branch)
	if row := confirmTree(t, d, f.y, p.Token, false); !strings.Contains(row.Detail, "tracking branch changed") {
		t.Fatal(row)
	}
	// A live pointer to a separate repo does not qualify as a dead pointer.
	marker := filepath.Join(w.Path, ".git")
	if err := os.WriteFile(marker, []byte("gitdir: "+kept+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if p := previewTree(t, d, f.y, nil, false); !strings.Contains(p.Rows[0].Detail, "private registration") {
		t.Fatal(p)
	}
	if _, err := os.Stat(filepath.Join(kept, "HEAD")); err != nil {
		t.Fatal(err)
	}
}

// Adds ordinary fixture data after the filesystem has unlinked the marker,
// so the actual final rmdir hits a nonempty directory and must report it.
type discardFinalFixture struct {
	context.Context
	path   string
	cancel context.CancelFunc
	fired  bool
	write  error
}

func (f *discardFinalFixture) Err() error {
	if !f.fired {
		if _, err := os.Lstat(filepath.Join(f.path, ".git")); os.IsNotExist(err) {
			f.fired = true
			f.write = os.WriteFile(filepath.Join(f.path, "remaining.txt"), []byte("late fixture output"), 0600)
			if f.cancel != nil {
				f.cancel()
			}
		}
	}
	return f.Context.Err()
}

func TestDesktopBulkInterruptedFinalRemove(t *testing.T) {
	for _, mode := range []string{"nonempty", "canceled", "replacement", "old-gitfile"} {
		t.Run(mode, func(t *testing.T) {
			f := newCleanupFixture(t)
			w := f.checkout(t, "author", "wide-local")
			f.finishTask(t)
			private := cleanupGit(t, w.Path, "rev-parse", "--absolute-git-dir")
			if mode == "old-gitfile" {
				if err := os.Rename(private, filepath.Join(t.TempDir(), "kept-admin")); err != nil {
					t.Fatal(err)
				}
			}
			refs := cleanupGit(t, f.repo.Path, "for-each-ref", "--format=%(refname) %(objectname)")
			for i := range 512 {
				if err := os.WriteFile(filepath.Join(w.Path, fmt.Sprintf("output-%04d", i)), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			d := &DesktopWorktrees{Server: f.s}
			p := previewTree(t, d, f.y, nil, true)
			baseCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fault := &discardFinalFixture{Context: baseCtx, path: w.Path}
			if mode == "canceled" {
				fault.cancel = cancel
			}
			rows, err := d.Confirm(fault, f.y.Name, f.y.HiredAt, p.Token, nil)
			if err != nil || len(rows) != 1 || rows[0].Removed || !fault.fired || fault.write != nil || !strings.Contains(rows[0].Detail, `"remaining.txt"`) {
				t.Fatal(rows, err, fault.fired, fault.write)
			}
			if mode == "canceled" && !strings.Contains(rows[0].Detail, "context canceled") || mode != "canceled" && !strings.Contains(rows[0].Detail, "not empty") {
				t.Fatal("partial result lost the final error", rows)
			}
			entries, err := os.ReadDir(w.Path)
			if err != nil || len(entries) != 1 || entries[0].Name() != "remaining.txt" {
				t.Fatal(entries, err)
			}
			root := filepath.Dir(f.y.Path)
			coldStore := yesmen.New(root, root+"-archives")
			coldManager := agent.New(agent.Config{Yesmen: coldStore})
			t.Cleanup(coldManager.StopAll)
			cold := &DesktopWorktrees{Server: &Server{Yesmen: coldStore, Agents: coldManager, Pool: f.s.Pool, HasOpenPull: f.s.HasOpenPull}}
			p = previewTree(t, cold, f.y, nil, true)
			if !p.Rows[0].Interrupted {
				t.Fatal("cold preview did not see the partial directory", p)
			}
			if mode == "replacement" {
				moved := w.Path + "-saved"
				if err := os.Rename(w.Path, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.CopyFS(w.Path, os.DirFS(moved)); err != nil {
					t.Fatal(err)
				}
				confirmTree(t, cold, f.y, p.Token, false)
				previewTree(t, cold, f.y, nil, false)
				for _, dir := range []string{w.Path, moved} {
					if data, err := os.ReadFile(filepath.Join(dir, "remaining.txt")); err != nil || string(data) != "late fixture output" {
						t.Fatal(dir, err)
					}
				}
			} else {
				confirmTree(t, cold, f.y, p.Token, true)
				for _, dir := range []string{w.Path, private} {
					if _, err := os.Lstat(dir); !os.IsNotExist(err) {
						t.Fatal("directory remains", dir, err)
					}
				}
				recs, err := coldStore.WorktreeRecords(f.y.Name)
				if err != nil || !recs[0].Removed || recs[0].DiscardRetry != nil || recs[0].CleanupHead != "" {
					t.Fatal(recs, err)
				}
			}
			if after := cleanupGit(t, f.repo.Path, "for-each-ref", "--format=%(refname) %(objectname)"); refs != after {
				t.Fatal("partial cleanup modified refs")
			}
		})
	}
}
