package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/safefs/linktest"
	"github.com/idy/yesman/internal/yesmen"
)

func changeDesktopTask(t *testing.T, store *yesmen.Store, y yesmen.Yesman, edits ...yesmen.TaskEdit) {
	t.Helper()
	if _, err := store.ChangeBoard(y.Name, yesmen.BoardChange{Update: edits}); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopBulkReturnDuringRemoteVerification(t *testing.T) {
	for _, change := range []string{"keep", "reopen", "note"} {
		t.Run(change, func(t *testing.T) {
			x, w, _, cert := retainedBoardReturn(t, "author")
			s := x.f.srv
			d := &DesktopWorktrees{Server: s}
			plan := previewTree(t, d, x.y, nil, true)
			read := s.ReadPullMerge
			s.ReadPullMerge = func(ctx context.Context, target string, n int) (github.PullMerge, error) {
				e := yesmen.TaskEdit{ID: cert.Task}
				switch change {
				case "keep":
					keep := []yesmen.WorktreeReturn{}
					e.Worktrees = &keep
				case "reopen":
					status := yesmen.TaskDoing
					e.Status = &status
				case "note":
					note := "More detail about the same delivery"
					e.Note = &note
				}
				changeDesktopTask(t, s.Yesmen, x.y, e)
				return read(ctx, target, n)
			}
			row := confirmTree(t, d, x.y, plan.Token, change == "note")
			if change == "note" {
				if err := s.Pool.CheckWorktreeCheckpoint(t.Context(), w); err != nil {
					t.Fatal(err)
				}
				return
			}
			if row.Reason != staleDecision {
				t.Fatal(row)
			}
			assertRetained(t, x, w)
			s.ReadPullMerge = read
			// A new service and Store on the same hire must also see the withdrawal.
			coldStore := yesmen.New(filepath.Dir(x.y.Path), filepath.Dir(x.y.Path)+"-archive")
			cold := &DesktopWorktrees{Server: &Server{Pool: s.Pool, Yesmen: coldStore, Agents: s.Agents, ReadPullMerge: read, HasOpenPull: s.HasOpenPull}}
			p := previewTree(t, cold, x.y, nil, false)
			if change == "keep" {
				if p.Rows[0].Reason != boardKeep {
					t.Fatal(p.Rows)
				}
				single := previewTree(t, cold, x.y, &w, true)
				if single.Rows[0].KeepWarning != keepWarning {
					t.Fatal(single.Rows)
				}
				confirmTree(t, cold, x.y, single.Token, true)
			} else if !strings.Contains(p.Rows[0].Reason, "unfinished") {
				t.Fatal(p.Rows)
			}
		})
	}
}

func TestDesktopSingleRoundAndOpenPR(t *testing.T) {
	f := newCleanupFixture(t)
	w := f.checkout(t, "author", "canceled")
	d := &DesktopWorktrees{Server: f.s}
	dropped, doing := yesmen.TaskDropped, yesmen.TaskDoing
	changeDesktopTask(t, f.s.Yesmen, f.y, yesmen.TaskEdit{ID: "t1", Status: &dropped})
	plan := previewTree(t, d, f.y, &w, true)
	changeDesktopTask(t, f.s.Yesmen, f.y, yesmen.TaskEdit{ID: "t1", Status: &doing}, yesmen.TaskEdit{ID: "t1", Status: &dropped})
	if row := confirmTree(t, d, f.y, plan.Token, false); row.Reason != staleDecision {
		t.Fatal(row)
	}
	plan = previewTree(t, d, f.y, &w, true)
	cleanupGit(t, w.Path, "branch", "--set-upstream-to=origin/main", w.Branch)
	if row := confirmTree(t, d, f.y, plan.Token, false); row.Reason != staleCheckout {
		t.Fatal("confirmation ignored changed tracking branch", row)
	}
	f.s.HasOpenPull = func(_ context.Context, _, _, branch string) (bool, error) { return branch == "main", nil }
	if p := previewTree(t, d, f.y, &w, false); p.Rows[0].Reason != "This branch has an open pull request." {
		t.Fatal("open PR on the differently named tracking branch", p)
	}
	f.s.HasOpenPull = func(context.Context, string, string, string) (bool, error) { return false, nil }
	plan = previewTree(t, d, f.y, &w, true)
	f.s.HasOpenPull = func(context.Context, string, string, string) (bool, error) { return true, nil }
	if row := confirmTree(t, d, f.y, plan.Token, false); row.Reason != "This branch has an open pull request." {
		t.Fatal(row)
	}
	f.s.HasOpenPull = func(context.Context, string, string, string) (bool, error) { return false, errors.New("offline") }
	if p := previewTree(t, d, f.y, &w, false); !strings.Contains(p.Rows[0].Reason, "GitHub") {
		t.Fatal(p)
	}
	f.s.HasOpenPull = func(context.Context, string, string, string) (bool, error) { return false, nil }
	plan = previewTree(t, d, f.y, &w, true)
	confirmTree(t, d, f.y, plan.Token, true)
}

func TestDesktopIndexChangeDuringDialog(t *testing.T) {
	for _, mode := range []string{"single", "bulk"} {
		t.Run(mode, func(t *testing.T) {
			f := newCleanupFixture(t)
			w := f.checkout(t, "author", "work")
			f.finishTask(t)
			d := &DesktopWorktrees{Server: f.s}
			selected := &w
			if mode == "bulk" {
				out := f.remove(t, f.lead, removeWorktreeRequest{Dir: w.Dir, Completion: f.completion("done"), Handoff: f.delegate.agent.Name})
				if out.Removed || out.Record == nil || out.Record.Completion == nil {
					t.Fatal(out)
				}
				selected = nil
			}
			p := previewTree(t, d, f.y, selected, true)
			file := filepath.Join(w.Path, "README")
			cleanupGit(t, w.Path, "switch", "-qc", "other")
			if err := os.WriteFile(file, []byte("Other version\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cleanupGit(t, w.Path, "commit", "-qam", "Other version")
			if err := os.WriteFile(file, []byte("Handwritten edit\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cleanupGit(t, w.Path, "switch", "--merge", w.Branch)
			cleanupGit(t, w.Path, "checkout", "--ours", "--", "README")
			cleanupGit(t, w.Path, "add", "README")
			if status := cleanupGit(t, w.Path, "status", "--porcelain=v1", "--ignored"); status != "" {
				t.Fatal(status)
			}
			if row := confirmTree(t, d, f.y, p.Token, false); row.Reason != resolveUndo {
				t.Fatal(row)
			}
			if again := previewTree(t, d, f.y, selected, false); again.Rows[0].Reason != resolveUndo {
				t.Fatal(again)
			}
			cleanupGit(t, f.repo.Path, "reflog", "expire", "--expire=now", "--all")
			cleanupGit(t, f.repo.Path, "gc", "--prune=now")
			cleanupGit(t, w.Path, "checkout", "-m", "--", "README")
			if text := cleanupGit(t, w.Path, "show", ":3:README"); text != "Handwritten edit" {
				t.Fatal(text)
			}
		})
	}
}

func TestDesktopFilesLeaseAcrossWorkspaceAlias(t *testing.T) {
	f := newCleanupFixture(t)
	w := f.checkout(t, "author", "alias-reader")
	f.finishTask(t)
	alias := filepath.Join(t.TempDir(), "workspaces")
	if runtime.GOOS == "windows" {
		linktest.Junction(t, filepath.Dir(f.y.Path), alias)
	} else {
		linktest.Symlink(t, filepath.Dir(f.y.Path), alias)
	}
	configured := yesmen.New(alias, filepath.Join(t.TempDir(), "archive"))
	y, err := configured.Get(f.y.Name)
	if err != nil || y.Path == f.y.Path || !y.HiredAt.Equal(f.y.HiredAt) {
		t.Fatal(y, err)
	}
	a, err := os.Stat(y.Path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(f.y.Path)
	if err != nil || !os.SameFile(a, b) {
		t.Fatal("alias misses original workspace", err)
	}
	d := &DesktopWorktrees{Server: &Server{Pool: f.s.Pool, Agents: f.s.Agents, Yesmen: configured, HasOpenPull: f.s.HasOpenPull}}
	err = f.s.Pool.WithCheckoutFiles(f.y.Path, w.Repo, w.Dir, w.CleanupID, func(root *os.Root) error {
		p := previewTree(t, d, y, &w, true)
		d.Cancel(p.Token)
		if _, err := d.Confirm(t.Context(), y.Name, y.HiredAt, p.Token, nil); err == nil {
			t.Fatal("canceled token admitted")
		}
		p = previewTree(t, d, y, &w, true)
		row := confirmTree(t, d, y, p.Token, false)
		if !strings.Contains(row.Detail, "checkout is in use") {
			t.Fatal(row)
		}
		_, err := root.Stat("README")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	records, err := configured.WorktreeRecords(y.Name)
	if err != nil || len(records) != 1 || records[0].Removed || records[0].ID != w.CleanupID {
		t.Fatal(records, err)
	}
	p := previewTree(t, d, y, &w, true)
	confirmTree(t, d, y, p.Token, true)
	if err := f.s.Pool.CheckWorktreeCheckpoint(t.Context(), w); err != nil {
		t.Fatal(err)
	}
}
