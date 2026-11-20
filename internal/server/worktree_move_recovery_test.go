package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/safefs"
	"github.com/idy/yesman/internal/safefs/linktest"
	"github.com/idy/yesman/internal/yesmen"
)

func confirmMove(t *testing.T, f *cleanupFixture, req WorktreeMove) (MovePreview, error) {
	t.Helper()
	req.Confirm = moveTest(t, f, req).Stamp
	return f.s.WorktreeMove(context.Background(), req)
}

func completeMove(t *testing.T, f *cleanupFixture, req WorktreeMove) MovePreview {
	t.Helper()
	out, err := confirmMove(t, f, req)
	if err != nil || out.Warning != "" {
		t.Fatalf("move: %+v, %v", out, err)
	}
	return out
}

func restartForMove(t *testing.T, f *cleanupFixture) {
	t.Helper()
	store := yesmen.New(filepath.Dir(f.y.Path), f.s.Yesmen.WorktreeArchiveDir())
	f.s = &Server{Pool: pool.New(f.s.Pool.Root()), Yesmen: store, Agents: agent.New(agent.Config{Yesmen: store})}
	t.Cleanup(f.s.Agents.StopAll)
}

func TestWorktreeMoveAfterRemovingWorker(t *testing.T) {
	for _, removed := range []string{"owner", "cleanup"} {
		t.Run(removed, func(t *testing.T) {
			f, w, req := archiveFixture(t)
			newOwner := "cleanup"
			if _, err := f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Owner: &newOwner}}}); err != nil {
				t.Fatal(err)
			}
			archived := completeMove(t, f, req)
			if err := f.s.Agents.RemoveWorker(f.y.Name, removed); err != nil {
				t.Fatal(err)
			}
			restartForMove(t, f)
			req.Action = "restore"
			if moveTest(t, f, req).Physical != archived.To {
				t.Fatal("wrong archived instance")
			}
			if err := f.s.Yesmen.AddWorker(f.y.Name, yesmen.Worker{Name: removed, StartedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			err := f.s.Agents.WithWorktreeOwner(agent.ID(f.y.Name, removed), "", func() error {
				rejectMove(t, f, req)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			title := "current work of the replacement worker"
			if _, err := f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: &title, Owner: &removed}}}); err != nil {
				t.Fatal(err)
			}
			rejectMove(t, f, req)
			done := "done"
			if _, err := f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t2", Status: &done}}}); err != nil {
				t.Fatal(err)
			}
			completeMove(t, f, req)
			if got := cleanupGit(t, w.Path, "rev-parse", "HEAD"); got != w.Head {
				t.Fatal(got)
			}
			req.Action = "archive"
			rejectMove(t, f, req) // a new archive needs the original live generations again
		})
	}
}

// Run the actual journal's write, file sync, content-recheck and replacement,
// withholding only the final durable publication barrier at the selected phase.
var failJournalSync = errors.New("fixture: archive journal sync failed")

func journalFault(t *testing.T, phase string, afterReplace bool) func(*os.Root, string, string) error {
	t.Helper()
	return func(root *os.Root, staged, name string) error {
		contents, err := root.ReadFile(staged)
		if err != nil {
			return err
		}
		var r []yesmen.WorktreeRecord
		if err := json.Unmarshal(contents, &r); err != nil {
			return err
		}
		if r[0].Archive.Phase != phase {
			return safefs.ReplaceSynced(root, staged, name)
		}
		if afterReplace {
			if err := root.Rename(staged, name); err != nil {
				return err
			}
		}
		return failJournalSync
	}
}

func TestWorktreeMoveSyncBeforeFirstAndRetriedRename(t *testing.T) {
	for _, action := range []string{"archive", "restore"} {
		t.Run(action, func(t *testing.T) {
			f, w, req := archiveFixture(t)
			phase := "archiving"
			if action == "restore" {
				completeMove(t, f, req)
				req.Action, phase = "restore", "restoring"
			}
			before := moveTest(t, f, req)
			for attempt := range 2 {
				f.s.Yesmen.ReplaceWorktreeJournal = journalFault(t, phase, true)
				if _, err := confirmMove(t, f, req); !errors.Is(err, failJournalSync) {
					t.Fatalf("attempt %d: %v", attempt, err)
				}
				if got := cleanupGit(t, before.From, "rev-parse", "HEAD"); got != w.Head {
					t.Fatal("source changed", got)
				}
				if _, err := os.Lstat(before.To); !os.IsNotExist(err) {
					t.Fatal("destination after failed barrier", err)
				}
				records, err := f.s.Yesmen.WorktreeRecords(f.y.Name)
				if err != nil || records[0].Archive.Phase != phase {
					t.Fatal("expected visible pending journal", records, err)
				}
				if _, err := os.Stat(filepath.Join(records[0].Archive.Private, "locked")); !os.IsNotExist(err) {
					t.Fatal("locked before the journal was durable", err)
				}
				if out := f.remove(t, f.lead, removeWorktreeRequest{Dir: w.Dir, Completion: f.completion("done")}); out.Removed || !strings.Contains(out.Reason, "archive") {
					t.Fatal(out)
				}
				if err := f.s.retireMissingWorktree(context.Background(), f.owner, f.y, records, 0); err == nil || !strings.Contains(err.Error(), "reserved") {
					t.Fatal("retired a reserved archive", err)
				}
				if attempt == 0 {
					if err := f.s.Agents.RemoveWorker(f.y.Name, "owner"); err != nil {
						t.Fatal(err)
					}
				}
				restartForMove(t, f)
			}
			if out := completeMove(t, f, req); out.Physical != before.To {
				t.Fatal(out)
			}
		})
	}
}

func TestWorktreeMoveFinalJournalWarning(t *testing.T) {
	for _, action := range []string{"archive", "restore"} {
		for _, cut := range []string{"before-replace", "after-replace"} {
			t.Run(action+"/"+cut, func(t *testing.T) {
				f, w, req := archiveFixture(t)
				final := "archived"
				if action == "restore" {
					completeMove(t, f, req)
					req.Action, final = "restore", "restored"
				}
				p := moveTest(t, f, req)
				f.s.Yesmen.ReplaceWorktreeJournal = journalFault(t, final, cut == "after-replace")
				out, err := confirmMove(t, f, req)
				if err != nil || out.Physical != p.To || !strings.Contains(out.Warning, p.To) || !strings.Contains(out.Warning, "Finish") {
					t.Fatal(out, err)
				}
				if got := cleanupGit(t, p.To, "rev-parse", "HEAD"); got != w.Head {
					t.Fatal(got)
				}
				restartForMove(t, f)
				if cut == "before-replace" {
					cancel := req
					cancel.Action = "cancel"
					rejectMove(t, f, cancel)
					// Local bookkeeping must remain possible with origin offline.
					cleanupGit(t, f.repo.Path, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "offline"))
					completeMove(t, f, req)
				}
				records, err := f.s.Yesmen.WorktreeRecords(f.y.Name)
				if err != nil || records[0].Archive.Phase != final {
					t.Fatal(records, err)
				}
			})
		}
	}
}

func TestWorktreeMoveLiveDefault(t *testing.T) {
	f, w, req := archiveFixture(t)
	origin := cleanupGit(t, f.repo.Path, "config", "--get", "remote.origin.url")
	p := moveTest(t, f, req)
	cleanupGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/"+w.Branch)
	if err := f.s.Pool.Fetch(context.Background(), f.repo.Name, true); err != nil {
		t.Fatal(err)
	}
	for _, confirm := range []string{p.Stamp, ""} {
		req.Confirm = confirm
		if _, err := f.s.WorktreeMove(context.Background(), req); err == nil || !strings.Contains(err.Error(), "protected") {
			t.Fatalf("admitted new default: %v", err)
		}
	}
	cleanupGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")
	req.Confirm = ""
	p = moveTest(t, f, req)
	cleanupGit(t, f.repo.Path, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "offline"))
	req.Confirm = p.Stamp
	rejectMove(t, f, req)
	records, err := f.s.Yesmen.WorktreeRecords(f.y.Name)
	if err != nil || records[0].Archive != nil {
		t.Fatal("remote unavailable wrote an intent", records, err)
	}
}

func TestWorktreeCancelPendingMoveAtSource(t *testing.T) {
	for _, action := range []string{"archive", "restore"} {
		t.Run(action, func(t *testing.T) {
			f, w, req := archiveFixture(t)
			if action == "restore" {
				completeMove(t, f, req)
				req.Action = "restore"
			}
			p := moveTest(t, f, req)
			origin := cleanupGit(t, f.repo.Path, "config", "--get", "remote.origin.url")
			// Change the remote default between commit admission and the last
			// pre-rename check. The source is intact with the intent's own lock.
			f.s.Yesmen.ReplaceWorktreeJournal = func(root *os.Root, staged, name string) error {
				if err := safefs.ReplaceSynced(root, staged, name); err != nil {
					return err
				}
				cleanupGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/"+w.Branch)
				return nil
			}
			req.Confirm = p.Stamp
			if _, err := f.s.WorktreeMove(context.Background(), req); err == nil || !strings.Contains(err.Error(), "protected") {
				t.Fatal(err)
			}
			restartForMove(t, f)
			cancel := req
			cancel.Action, cancel.Confirm = "cancel", ""
			private := cleanupGit(t, p.From, "rev-parse", "--absolute-git-dir")
			lock := filepath.Join(private, "locked")
			owned, err := os.ReadFile(lock)
			if err != nil {
				t.Fatal(err)
			}
			preview := moveTest(t, f, cancel)
			writeFixture(t, lock, "foreign lock")
			cancel.Confirm = preview.Stamp
			rejectMove(t, f, cancel)
			if got, err := os.ReadFile(lock); err != nil || string(got) != "foreign lock" {
				t.Fatal("foreign lock changed", err)
			}
			writeFixture(t, lock, string(owned))
			cancel.Confirm = ""
			out := completeMove(t, f, cancel)
			if out.Physical != p.From || out.To != p.From {
				t.Fatal(out)
			}
			if _, err := os.Stat(lock); !os.IsNotExist(err) {
				t.Fatal("owned lock remains", err)
			}
			records, err := f.s.Yesmen.WorktreeRecords(f.y.Name)
			if err != nil || records[0].Archive.CanceledAt.IsZero() || records[0].ReservedForArchive() != (action == "restore") {
				t.Fatal(records, err)
			}
		})
	}
}

func TestWorktreeMoveGitPathThroughStorageAlias(t *testing.T) {
	f, w, req := archiveFixture(t)
	base := filepath.Dir(filepath.Dir(f.y.Path))
	alias := filepath.Join(t.TempDir(), "storage-link")
	if runtime.GOOS == "windows" {
		linktest.Junction(t, base, alias)
	} else {
		linktest.Symlink(t, base, alias)
	}
	private := filepath.FromSlash(cleanupGit(t, w.Path, "rev-parse", "--absolute-git-dir"))
	rel := filepath.Join("repos", filepath.FromSlash(f.repo.Name)+".git", "worktrees", filepath.Base(private))
	writeFixture(t, filepath.Join(w.Path, ".git"), "gitdir: "+filepath.ToSlash(filepath.Join(alias, rel))+"\n")
	writeFixture(t, filepath.Join(private, "gitdir"), filepath.ToSlash(filepath.Join(alias, "yesmen", f.y.Name, w.Dir, ".git"))+"\n")
	p := completeMove(t, f, req)
	records, err := f.s.Yesmen.WorktreeRecords(f.y.Name)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(records[0].Archive.Private, alias) || p.Physical != p.To {
		t.Fatalf("expected canonical location: %+v", p)
	}
	req.Action = "restore"
	completeMove(t, f, req)
	if got := cleanupGit(t, w.Path, "rev-parse", "HEAD"); got != w.Head {
		t.Fatal(got)
	}
}

func TestWorktreeCancelAfterPermissionFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions enforced for this user")
	}
	f, _, req := archiveFixture(t)
	p := moveTest(t, f, req)
	dir := filepath.Dir(p.To)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	req.Confirm = p.Stamp
	if _, err := f.s.WorktreeMove(context.Background(), req); !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
	req.Action, req.Confirm = "cancel", ""
	if out := completeMove(t, f, req); out.Physical != p.From {
		t.Fatal(out)
	}
}
