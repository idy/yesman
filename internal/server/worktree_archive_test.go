package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

func archiveFixture(t *testing.T) (*cleanupFixture, pool.Worktree, WorktreeMove) {
	t.Helper()
	f := newCleanupFixture(t)
	if err := os.Mkdir(f.s.Yesmen.WorktreeArchiveDir(), 0700); err != nil {
		t.Fatal(err)
	}
	w := f.checkout(t, "author", "original")
	f.finishTask(t)
	return f, w, WorktreeMove{Workspace: f.y.Name, Since: f.y.HiredAt, Dir: w.Dir, ID: w.CleanupID, Action: "archive"}
}

func moveTest(t *testing.T, f *cleanupFixture, r WorktreeMove) MovePreview {
	t.Helper()
	out, err := f.s.WorktreeMove(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func rejectMove(t *testing.T, f *cleanupFixture, r WorktreeMove) {
	t.Helper()
	if _, err := f.s.WorktreeMove(context.Background(), r); err == nil {
		t.Fatalf("admitted %+v", r)
	}
}
func saveRecords(t *testing.T, f *cleanupFixture, change func(*yesmen.WorktreeRecord)) {
	t.Helper()
	records, err := f.s.Yesmen.WorktreeRecords(f.y.Name)
	if err != nil {
		t.Fatal(err)
	}
	change(&records[0])
	if err := f.s.Yesmen.SaveWorktreeRecords(f.y.Name, records); err != nil {
		t.Fatal(err)
	}
}
func writeFixture(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHumanWorktreeArchiveRestore(t *testing.T) {
	f, w, r := archiveFixture(t)
	owner := "cleanup"
	if _, err := f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Owner: &owner}}}); err != nil {
		t.Fatal(err)
	}
	cleanupGit(t, w.Path, "switch", "-c", "superseded")
	cleanupGit(t, w.Path, "commit", "--allow-empty", "-m", "local only")
	current := cleanupGit(t, w.Path, "rev-parse", "HEAD")
	writeFixture(t, filepath.Join(w.Path, "README"), "staged\n")
	cleanupGit(t, w.Path, "add", "README")
	writeFixture(t, filepath.Join(w.Path, "README"), "working copy\n")
	writeFixture(t, filepath.Join(w.Path, "untracked"), "untracked")
	if err := os.MkdirAll(filepath.Join(w.Path, "ignored", "seeds"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{".gitignore", "roll.json", "seeds/instance.json"} {
		writeFixture(t, filepath.Join(w.Path, "ignored", file), file)
	}
	status := cleanupGit(t, w.Path, "status", "--porcelain", "--ignored")
	before, _ := f.s.Yesmen.WorktreeRecords(f.y.Name)
	refs := cleanupGit(t, f.repo.Path, "show-ref")
	preview := moveTest(t, f, r)
	if preview.Branch != "superseded" || preview.Head != current || filepath.FromSlash(w.Path) != preview.Physical || preview.Stamp == "" {
		t.Fatal(preview)
	}
	if _, err := os.Stat(preview.To); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview moved worktree", err)
	}
	r.Confirm = preview.Stamp
	moveTest(t, f, r)
	if _, err := os.Stat(w.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("original path exists", err)
	}
	if wts, err := f.s.Pool.Worktrees(context.Background(), f.y.Path); err != nil || len(wts) != 0 {
		t.Fatalf("active listing: %+v %v", wts, err)
	}
	if got := cleanupGit(t, preview.To, "status", "--porcelain", "--ignored"); got != status {
		t.Fatal(got, status)
	}
	if got := cleanupGit(t, preview.To, "show", ":README"); got != "staged" {
		t.Fatal(got)
	}
	history, _ := f.s.Yesmen.WorktreeRecords(f.y.Name)
	j := history[0].Archive
	if j == nil || j.Phase != "archived" || j.TaskOwner != owner || j.Head != current || j.Private == "" {
		t.Fatal(j)
	}
	history[0].Archive = nil
	if !reflect.DeepEqual(history, before) {
		t.Fatal("archive changed original lifecycle evidence", history, before)
	}
	if out := f.remove(t, f.lead, removeWorktreeRequest{Dir: w.Dir, Completion: f.completion("done")}); out.Removed || !strings.Contains(out.Reason, "archive") {
		t.Fatal(out)
	}
	if _, err := f.s.checkoutOwned(context.Background(), f.owner, f.y, CheckoutRequest{Repo: f.repo.Name, Dir: w.Dir, Branch: "other"}); err == nil {
		t.Fatal("archived tree retired")
	}
	if _, err := CheckoutForUser(context.Background(), f.s.Pool, f.s.Yesmen, f.y, CheckoutRequest{Repo: w.Repo, Dir: w.Dir, Branch: "new"}); err == nil {
		t.Fatal("UI checkout reused reserved path")
	}
	rejectMove(t, f, r)
	restore := r
	restore.Confirm = ""
	restore.Action = "restore"
	rp := moveTest(t, f, restore)
	restore.Confirm = rp.Stamp
	if err := os.Mkdir(w.Path, 0700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(w.Path, "mine"), "keep")
	rejectMove(t, f, restore)
	if data, err := os.ReadFile(filepath.Join(w.Path, "mine")); err != nil || string(data) != "keep" {
		t.Fatal("overwrote restore collision")
	}
	if err := os.Rename(w.Path, w.Path+"-kept"); err != nil {
		t.Fatal(err)
	}
	rejectMove(t, f, restore) // the refused confirmation was consumed
	restore.Confirm = ""
	restore.Confirm = moveTest(t, f, restore).Stamp
	moveTest(t, f, restore)
	wts, err := f.s.Pool.Worktrees(context.Background(), f.y.Path)
	if err != nil || len(wts) != 1 || wts[0].CleanupID != w.CleanupID || wts[0].Head != current {
		t.Fatal(wts, err)
	}
	if cleanupGit(t, w.Path, "status", "--porcelain", "--ignored") != status || cleanupGit(t, f.repo.Path, "show-ref") != refs {
		t.Fatal("changed files, index or refs")
	}
	for _, file := range []string{"untracked", "ignored/.gitignore", "ignored/roll.json", "ignored/seeds/instance.json"} {
		if data, err := os.ReadFile(filepath.Join(w.Path, file)); err != nil || string(data) != strings.TrimPrefix(file, "ignored/") {
			t.Fatal(file, err)
		}
	}
	rejectMove(t, f, r) // an old archive stamp after an entire round trip
}

func TestHumanWorktreeMoveRefusesActiveProcess(t *testing.T) {
	x := newWorktreeFixture(t)
	w := managedCheckout(t, x, "author")
	done := "done"
	if _, err := x.f.store.ChangeBoard(x.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: &done}}}); err != nil {
		t.Fatal(err)
	}
	req := WorktreeMove{Workspace: x.y.Name, Since: x.y.HiredAt, Dir: w.Dir, ID: w.CleanupID, Action: "archive"}
	if _, err := x.f.srv.WorktreeMove(context.Background(), req); err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("live owner: %v", err)
	}
	if rec := recordOf(t, x, w.Dir); rec.Archive != nil {
		t.Fatal("active tree changed")
	}
	if got := managementPost(t, x, x.w.Token, "/v1/worktrees/archive", req).Code; got != 404 {
		t.Fatalf("agent archive endpoint returned %d", got)
	}
}

func TestHumanWorktreeArchiveRefusals(t *testing.T) {
	for _, kind := range []string{"owner generation", "legacy root", "unknown purpose", "removed", "open task", "open owner task", "primary branch", "nonce", "root replaced", "old head", "old branch", "wrong hire", "foreign lock", "index lock", "common directory", "archive collision", "traversal", "no confirm"} {
		t.Run(kind, func(t *testing.T) {
			f, w, r := archiveFixture(t)
			p := moveTest(t, f, r)
			r.Confirm = p.Stamp
			private := cleanupGit(t, w.Path, "rev-parse", "--absolute-git-dir")
			switch kind {
			case "owner generation":
				saveRecords(t, f, func(r *yesmen.WorktreeRecord) { r.OwnerSince = time.Now() })
			case "legacy root":
				saveRecords(t, f, func(r *yesmen.WorktreeRecord) { r.Root = nil })
			case "unknown purpose":
				saveRecords(t, f, func(r *yesmen.WorktreeRecord) { r.Purpose = "primary" })
			case "removed":
				saveRecords(t, f, func(r *yesmen.WorktreeRecord) { r.Removed = true })
			case "open task":
				s := "blocked"
				f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: &s}}})
			case "open owner task":
				s, o := "more work", "owner"
				f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: &s, Owner: &o}}})
			case "primary branch":
				cleanupGit(t, w.Path, "switch", "-c", "main")
			case "nonce":
				writeFixture(t, filepath.Join(private, "yesman-instance"), strings.Repeat("1", 32))
			case "root replaced":
				if err := os.Rename(w.Path, w.Path+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(w.Path, 0700); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(w.Path+"-original", ".git"))
				if err != nil {
					t.Fatal(err)
				}
				writeFixture(t, filepath.Join(w.Path, ".git"), string(data))
			case "old head":
				cleanupGit(t, w.Path, "commit", "--allow-empty", "-m", "later")
			case "old branch":
				cleanupGit(t, w.Path, "switch", "-c", "same-head")
			case "wrong hire":
				r.Since = r.Since.Add(time.Nanosecond)
			case "foreign lock":
				cleanupGit(t, f.repo.Path, "worktree", "lock", w.Path)
			case "index lock":
				writeFixture(t, filepath.Join(private, "index.lock"), "keep")
			case "common directory":
				writeFixture(t, filepath.Join(private, "commondir"), "../different\n")
			case "archive collision":
				if err := os.Mkdir(p.To, 0700); err != nil {
					t.Fatal(err)
				}
			case "traversal":
				r.Dir = "../outside"
			case "no confirm":
				r.Confirm = "yes"
			}
			rejectMove(t, f, r)
			if _, err := os.Stat(w.Path); err != nil {
				t.Fatal("source lost", err)
			}
			records, err := f.s.Yesmen.WorktreeRecords(f.y.Name)
			if err != nil || records[0].Archive != nil {
				t.Fatal("refusal wrote move intent", err)
			}
		})
	}
}

func TestHumanWorktreeMoveCrashRecovery(t *testing.T) {
	for _, direction := range []string{"archive", "restore"} {
		for _, cut := range []string{"intent", "locked", "renamed", "registered", "unlocked"} {
			t.Run(direction+"/"+cut, func(t *testing.T) {
				f, w, r := archiveFixture(t)
				p := moveTest(t, f, r)
				if direction == "restore" {
					r.Confirm = p.Stamp
					moveTest(t, f, r)
					r.Action = "restore"
					r.Confirm = ""
					p = moveTest(t, f, r)
				}
				private := filepath.FromSlash(cleanupGit(t, p.From, "rev-parse", "--absolute-git-dir"))
				active, stored, _ := f.s.Yesmen.WorktreePaths(f.y, w.Dir, w.CleanupID)
				const tx = "interrupted"
				phase := "archiving"
				if direction == "restore" {
					phase = "restoring"
				}
				j := yesmen.WorktreeArchive{LinkedLocation: pool.LinkedLocation{Active: active, Stored: stored, Common: f.repo.Path, Private: private, Head: p.Head, Branch: p.Branch}, Transaction: tx, ConfirmedAt: time.Now().UTC(), TaskOwner: "owner", Phase: phase}
				err := f.s.Yesmen.WorktreeMoveFile(f.y, func(_ []yesmen.WorktreeRecord, publish func(int, yesmen.WorktreeArchive) error) error {
					return publish(0, j)
				})
				if err != nil {
					t.Fatal(err)
				}
				if cut != "intent" {
					cleanupGit(t, f.repo.Path, "worktree", "lock", "--reason", "yesman move "+tx, p.From)
				}
				if cut == "renamed" || cut == "registered" || cut == "unlocked" {
					if err := os.Rename(p.From, p.To); err != nil {
						t.Fatal(err)
					}
					cleanupGit(t, f.repo.Path, "worktree", "prune", "--expire", "now")
				}
				if cut == "registered" || cut == "unlocked" {
					writeFixture(t, filepath.Join(private, "gitdir"), filepath.ToSlash(filepath.Join(p.To, ".git"))+"\n")
				}
				if cut == "unlocked" {
					cleanupGit(t, f.repo.Path, "worktree", "unlock", p.To)
				}
				store := yesmen.New(filepath.Dir(f.y.Path), f.s.Yesmen.WorktreeArchiveDir())
				f.s = &Server{Yesmen: store, Pool: pool.New(f.s.Pool.Root()), Agents: agent.New(agent.Config{Yesmen: store})}
				t.Cleanup(f.s.Agents.StopAll)
				p = moveTest(t, f, r)
				r.Confirm = p.Stamp
				moveTest(t, f, r)
				if got := cleanupGit(t, p.To, "rev-parse", "HEAD"); got != w.Head {
					t.Fatal(got)
				}
				if _, err := os.Stat(filepath.Join(private, "locked")); !os.IsNotExist(err) {
					t.Fatal("move lock retained")
				}
				rejectMove(t, f, r)
			})
		}
	}
}

func TestHumanWorktreeMoveConcurrentConfirmation(t *testing.T) {
	f, _, r := archiveFixture(t)
	r.Confirm = moveTest(t, f, r).Stamp
	var wg sync.WaitGroup
	result := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := f.s.WorktreeMove(context.Background(), r); result <- err })
	}
	wg.Wait()
	close(result)
	var moved int
	for err := range result {
		if err == nil {
			moved++
		}
	}
	if moved != 1 {
		t.Fatalf("%d confirmations succeeded", moved)
	}
}
