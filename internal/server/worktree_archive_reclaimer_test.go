package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/safefs/linktest"
	"github.com/idy/yesman/internal/yesmen"
)

// A returned checkout may already have merged-source evidence and a recovery
// ref when the user archives it. Neither the present source before the move,
// nor the missing source afterwards, grants permission to retire the archive.
func TestWorktreeArchiveReservesReturnedCheckout(t *testing.T) {
	for _, phase := range []string{"pending", "archived"} {
		t.Run(phase, func(t *testing.T) {
			f, w, req := archiveFixture(t)
			proof := f.completion("done")
			handoff := f.remove(t, f.lead, removeWorktreeRequest{Dir: w.Dir, Completion: proof, Handoff: f.delegate.agent.Name})
			if handoff.Record == nil || handoff.Record.Completion == nil || handoff.Removed {
				t.Fatal(handoff)
			}
			cleanupGit(t, f.repo.Path, "update-ref", "refs/yesman/worktrees/"+w.CleanupID, w.Head)
			desktop := &DesktopWorktrees{Server: f.s}
			old := previewTree(t, desktop, f.y, &w, true)
			if phase == "pending" {
				f.s.Yesmen.ReplaceWorktreeJournal = journalFault(t, "archiving", true)
				if _, err := confirmMove(t, f, req); !errors.Is(err, failJournalSync) {
					t.Fatal(err)
				}
				f.s.Yesmen.ReplaceWorktreeJournal = nil
			} else {
				completeMove(t, f, req)
			}
			confirmTree(t, desktop, f.y, old.Token, false)
			if phase == "pending" {
				for _, p := range []WorktreeCleanupPreview{previewTree(t, desktop, f.y, &w, false), previewTree(t, desktop, f.y, nil, false)} {
					if p.Rows[0].Reason != archiveReserved {
						t.Fatal(p)
					}
				}
			}
			svc := NewWorktreeReclaimer(f.s)
			if err := svc.scan(f.y); err != nil || len(svc.jobs) != 0 {
				t.Fatalf("queued reserved checkout: %v, %v", svc.jobs, err)
			}
			if got := f.s.reconcileCheckout(t.Context(), f.y, w.CleanupID); got != collectedOrIneligible {
				t.Fatalf("reclaimer result %v", got)
			}
			if res := f.remove(t, f.owner, removeWorktreeRequest{Dir: w.Dir}); res.Removed || res.Record.Removed || res.Reason != "retain: "+archiveReserved {
				t.Fatal(res)
			}
			records, _, _, err := f.s.Yesmen.WorktreeState(f.y)
			if err != nil || len(records) != 1 || records[0].Removed || !records[0].ReservedForArchive() {
				t.Fatal(records, err)
			}
			if records[0].Completion.Head != proof.Head || records[0].CleanupOwner != f.delegate.agent.Name {
				t.Fatal("lost recorded completion or delegate")
			}
			if phase == "pending" {
				completeMove(t, f, req)
			}
			req.Action = "restore"
			completeMove(t, f, req)
			if got := f.s.reconcileCheckout(t.Context(), f.y, w.CleanupID); got != collectedOrIneligible {
				t.Fatal(got)
			}
			if preview := previewTree(t, desktop, f.y, nil, false); preview.Rows[0].Reason != "The restored checkout needs a fresh Board return for bulk cleanup." {
				t.Fatal(preview)
			}
			if got := cleanupGit(t, w.Path, "rev-parse", "HEAD"); got != w.Head {
				t.Fatal(got)
			}
		})
	}
}

func TestWorktreeArchiveThroughConfiguredRoot(t *testing.T) {
	f, w, req := archiveFixture(t)
	alias := filepath.Join(t.TempDir(), "settings-root")
	root := filepath.Dir(filepath.Dir(f.y.Path))
	if runtime.GOOS == "windows" {
		linktest.Junction(t, root, alias)
	} else {
		linktest.Symlink(t, root, alias)
	}
	store := yesmen.New(filepath.Join(alias, "yesmen"), filepath.Join(alias, "archive"))
	f.s = &Server{Yesmen: store, Pool: pool.New(filepath.Join(alias, "repos")), Agents: agent.New(agent.Config{Yesmen: store})}
	t.Cleanup(f.s.Agents.StopAll)
	moved := completeMove(t, f, req)
	expectedPath, err := pool.PhysicalPath(w.Path)
	// The source is now absent. Its verified physical parent still exists.
	if !os.IsNotExist(err) {
		t.Fatalf("old path still resolves to %s: %v", expectedPath, err)
	}
	parent, err := pool.PhysicalPath(filepath.Dir(w.Path))
	if err != nil {
		t.Fatal(err)
	}
	if moved.From != filepath.Join(parent, w.Dir) {
		t.Fatalf("unexpected source %s", moved.From)
	}
	req.Action = "restore"
	completeMove(t, f, req)
	if got := cleanupGit(t, w.Path, "rev-parse", "HEAD"); got != w.Head {
		t.Fatal(got)
	}
}

func TestWorktreeArchiveRestoreRequiresFreshBoardReturn(t *testing.T) {
	x, w, receipt := returnFixture(t, "author", "")
	if err := os.MkdirAll(x.f.store.WorktreeArchiveDir(), 0700); err != nil {
		t.Fatal(err)
	}
	submitReturn(t, x, receipt)
	x.f.m.Stop(x.me.agent.ID)
	move := WorktreeMove{Workspace: x.y.Name, Since: x.y.HiredAt, Dir: w.Dir, ID: w.CleanupID, Action: "archive"}
	f := &cleanupFixture{s: x.f.srv, y: x.y}
	completeMove(t, f, move)
	move.Action = "restore"
	completeMove(t, f, move)
	if reason := f.s.reconcileCheckout(context.Background(), x.y, w.CleanupID); reason != collectedOrIneligible {
		t.Fatal(reason)
	}
	assertRetained(t, x, w)
	// Repeat the actual Board return using the current owner's process. A
	// newly authenticated return may reclaim the checkout as usual.
	x.w = x.f.start(x.me.agent.ID)
	submitReturn(t, x, receipt)
	x.f.m.Stop(x.me.agent.ID)
	f.s.reconcileCheckout(context.Background(), x.y, w.CleanupID)
	if !recordOf(t, x, w.Dir).Removed {
		t.Fatal("new explicit return did not reclaim the restored checkout")
	}
	if _, err := os.Lstat(filepath.Join(x.y.Path, w.Dir)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
