package pool

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/yesmen"
)

func TestDeleteArchiveKeepsRelocatedPrimary(t *testing.T) { testDeleteRelocatedPrimary(t, true) }
func TestDeleteLiveKeepsRelocatedPrimary(t *testing.T)    { testDeleteRelocatedPrimary(t, false) }

func testDeleteRelocatedPrimary(t *testing.T, archived bool) {
	t.Helper()
	p, repo, wt, _ := newTestCheckout(t)
	base := t.TempDir()
	live, archives := filepath.Join(base, "yesmen"), filepath.Join(base, "archive")
	store := yesmen.New(live, archives)
	y, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	var sibling string
	if !archived {
		// The old archive and this same-name live hire are separate targets.
		sibling, err = store.Archive(y.Name, nil)
		if err != nil {
			t.Fatal(err)
		}
		y, err = store.Hire(y.Name)
		if err != nil {
			t.Fatal(err)
		}
	}
	tree := filepath.Join(y.Path, wt.Dir)
	run(t, repo.Path, "worktree", "move", wt.Path, tree)
	if err := os.WriteFile(filepath.Join(tree, "unfinished.txt"), []byte("keep my draft"), 0600); err != nil {
		t.Fatal(err)
	}
	record := yesmen.WorktreeRecord{ID: wt.CleanupID, Dir: wt.Dir, Repo: repo.Name, Owner: "lead", OwnerSince: y.HiredAt, Purpose: "primary", Head: wt.Head, Branch: wt.Branch, Root: &yesmen.WorktreeRoot{Ino: wt.Root.Ino, Birth: wt.Root.Birth}}
	if !archived {
		record.Owner = "original-worker"
		if err := store.AddWorker(y.Name, yesmen.Worker{Name: record.Owner, StartedAt: record.OwnerSince}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveWorktreeRecords(y.Name, []yesmen.WorktreeRecord{record}); err != nil {
		t.Fatal(err)
	}
	selected, parent := y.Path, live
	if archived {
		selected, err = store.Archive(y.Name, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Repair(t.Context(), []string{filepath.Join(selected, record.Dir)}); err != nil {
			t.Fatal(err)
		}
		parent = archives
	} else if err := store.RemoveWorker(y.Name, record.Owner); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(selected, record.Dir)
	readTarget := func() yesmen.DeletionTarget {
		t.Helper()
		if archived {
			list, err := store.StoredWorkspaces()
			if err != nil || len(list) != 1 {
				t.Fatal(list, err)
			}
			return list[0]
		}
		target, err := store.PrepareDeletion(y.Name, y.HiredAt)
		if err != nil {
			t.Fatal(err)
		}
		return target
	}
	store = yesmen.New(live, archives)
	target := readTarget()
	remove := func() error {
		_, err := store.Delete(t.Context(), target, p.CheckWorkspaceDeletion, func(yesmen.Yesman) error { return nil }, p.RelocatedPrimary)
		return err
	}
	if err := remove(); err == nil {
		t.Fatal("present primary was accepted")
	}
	kept := filepath.Join(base, "kept")
	if err := os.Rename(old, kept); err != nil {
		t.Fatal(err)
	}
	if err := remove(); err == nil {
		t.Fatal("absent path alone was accepted")
	}
	if err := os.Rename(kept, old); err != nil {
		t.Fatal(err)
	}
	run(t, repo.Path, "worktree", "move", old, kept)
	private, err := privateWorktreeDir(repo.Path, kept)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(private, worktreeInstanceFile)
	if err := os.Rename(marker, marker+".kept"); err != nil {
		t.Fatal(err)
	}
	if err := remove(); err == nil {
		t.Fatal("relocation without instance marker was accepted")
	}
	if err := os.Rename(marker+".kept", marker); err != nil {
		t.Fatal(err)
	}
	bad := record
	wrong := *record.Root
	wrong.Ino++
	bad.Root = &wrong
	if _, err := p.RelocatedPrimary(t.Context(), selected, bad); err == nil {
		t.Fatal("different checkout root was accepted")
	}
	before := run(t, repo.Path, "for-each-ref", "--format=%(refname) %(objectname)")
	// An occupied receipt folder fails on every platform, after the real Git
	// relocation and before deletion. Reopen the Store to retry the same target.
	receipts := filepath.Join(parent, ".worktree-relocations")
	metadata := filepath.Join(selected, ".yesman", "worktrees.json")
	original, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receipts, []byte("fixture blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := remove(); err == nil || !strings.Contains(err.Error(), "Could not save the relocation record") {
		t.Fatalf("expected receipt publication refusal: %v", err)
	}
	if b, err := os.ReadFile(metadata); err != nil || string(b) != string(original) {
		t.Fatal("original archive record changed on refusal", err)
	}
	if err := os.Remove(receipts); err != nil {
		t.Fatal(err)
	}
	store = yesmen.New(live, archives)
	if fresh := readTarget(); fresh != target {
		t.Fatal("deletion target changed on reopen", fresh, target)
	}
	if err := remove(); err != nil {
		t.Fatal(err)
	}
	if got := run(t, repo.Path, "for-each-ref", "--format=%(refname) %(objectname)"); got != before {
		t.Fatal("refs changed")
	}
	if yes, err := p.HasWorktreeRegistration(t.Context(), repo.Name, kept); err != nil || !yes {
		t.Fatal("registration", yes, err)
	}
	if b, err := os.ReadFile(filepath.Join(kept, "unfinished.txt")); err != nil || string(b) != "keep my draft" {
		t.Fatal("dirty bytes", string(b), err)
	}
	if _, err := os.Stat(selected); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if !archived {
		fresh, err := store.Hire(y.Name)
		if err != nil || fresh.HiredAt.Equal(y.HiredAt) || remove() == nil {
			t.Fatal("same-name replacement or stale deletion", err)
		}
		list, err := store.StoredWorkspaces()
		if err != nil || len(list) != 1 || list[0].Entry != filepath.Base(sibling) {
			t.Fatal("other archive changed", list, err)
		}
	}
	files, err := os.ReadDir(receipts)
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	b, err := os.ReadFile(filepath.Join(receipts, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Archive, Workspace, Instance, Destination string
		Record                                    yesmen.WorktreeRecord
	}
	if err := json.Unmarshal(b, &evidence); err != nil || evidence.Instance != target.Instance || realPath(evidence.Destination) != realPath(kept) || evidence.Record.ID != wt.CleanupID || evidence.Record.Purpose != "primary" || !strings.Contains(string(b), record.Head) {
		t.Fatal("relocation evidence", evidence, err)
	}
	if archived && (evidence.Archive != target.Entry || evidence.Workspace != "") || !archived && (evidence.Workspace != target.Entry || evidence.Archive != "") {
		t.Fatal("receipt for wrong workspace", evidence)
	}
}
