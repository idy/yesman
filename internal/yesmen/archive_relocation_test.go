package yesmen

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Git's relocation proof is tested with real checkouts in internal/pool. Here
// the proof is fixed so each failure is specifically a receipt flush failure.
func TestRelocationReceiptMustBeDurableBeforeArchiveDeletion(t *testing.T) {
	s := waitStore(t)
	r := WorktreeRecord{ID: "fixture", Purpose: "primary", Dir: "source", Owner: "lead"}
	if err := s.SaveWorktreeRecords("alice", []WorktreeRecord{r}); err != nil {
		t.Fatal(err)
	}
	archive, err := s.Archive("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(archive, ".yesman", "worktrees.json")
	original, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := s.StoredWorkspaces()
	if err != nil || len(targets) != 1 {
		t.Fatal(targets, err)
	}
	target := targets[0]
	folder := filepath.Join(s.archive, ".worktree-relocations")
	destination := filepath.Join(t.TempDir(), "retained")
	proof := func(context.Context, string, WorktreeRecord) (string, error) { return destination, nil }
	ioFailure := errors.New("cannot flush fixture directory")
	var published fs.FileInfo
	for _, step := range []struct {
		name, fail string
		wantCalls  []string
		published  bool
	}{
		{"persist folder", s.archive, []string{s.archive}, false},
		{"publish receipt", folder, []string{s.archive, folder}, true},
		{"retry existing folder", s.archive, []string{s.archive}, true},
		{"retry existing receipt", folder, []string{s.archive, folder}, true},
		{"complete", "", []string{s.archive, folder, s.archive, folder}, true},
	} {
		t.Run(step.name, func(t *testing.T) {
			cold := New(s.root, s.archive)
			var calls []string
			cold.syncRelocationDir = func(dir *os.Root) error {
				calls = append(calls, dir.Name())
				if dir.Name() == step.fail {
					return ioFailure
				}
				return syncRelocationDirectory(dir)
			}
			_, err := cold.Delete(t.Context(), target, func(context.Context, string) error { return nil }, nil, proof)
			if !slices.Equal(calls, step.wantCalls) {
				t.Fatalf("directory flushes %v, error %v", calls, err)
			}
			if step.fail != "" {
				if !errors.Is(err, ioFailure) {
					t.Fatalf("expected the directory flush refusal, got %v", err)
				}
				if got, err := os.ReadFile(metadata); err != nil || string(got) != string(original) {
					t.Fatalf("original archive record changed: %q %v", got, err)
				}
				if list, err := cold.StoredWorkspaces(); err != nil || len(list) != 1 || list[0] != target {
					t.Fatal("archive no longer available to retry", list, err)
				}
			} else if err != nil {
				t.Fatal(err)
			} else if _, err := os.Stat(archive); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("expected the selected archive to be gone", err)
			}
			files, err := os.ReadDir(folder)
			if err != nil || len(files) > 1 || (len(files) == 1) != step.published {
				t.Fatalf("receipt directory: %v %v", files, err)
			}
			if len(files) == 1 {
				info, err := files[0].Info()
				if err != nil || (published != nil && !os.SameFile(published, info)) {
					t.Fatalf("published receipt replaced: %v", err)
				}
				published = info
			}
		})
	}
}

func TestArchiveDeletionRechecksDurableRelocationProof(t *testing.T) {
	for _, change := range []string{"destination", "record", "missing record", "missing receipt", "replaced receipt", "late registration"} {
		t.Run(change, func(t *testing.T) {
			s := waitStore(t)
			record := WorktreeRecord{ID: "fixture", Purpose: "primary", Dir: "source", Owner: "lead"}
			mustDeletion(t, s.SaveWorktreeRecords("alice", []WorktreeRecord{record}))
			archive, err := s.Archive("alice", nil)
			mustDeletion(t, err)
			const note = "!sentinel"
			mustDeletion(t, os.WriteFile(filepath.Join(archive, note), []byte("staged bytes"), 0600))
			rows, err := s.StoredWorkspaces()
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			target := rows[0]
			stage := filepath.Join(s.archive, deletingPrefix+target.Instance+"."+target.Entry)
			folder := filepath.Join(s.archive, relocationFolder)
			destination, other := t.TempDir(), t.TempDir()
			for _, dir := range []string{destination, other} {
				mustDeletion(t, os.WriteFile(filepath.Join(dir, note), []byte("outside checkout"), 0600))
			}
			proofCalls := 0
			proof := func(_ context.Context, ws string, _ WorktreeRecord) (string, error) {
				proofCalls++
				if ws == stage && change == "destination" {
					return other, nil
				}
				return destination, nil
			}
			flushes := 0
			s.syncRelocationDir = func(dir *os.Root) error {
				flushes++
				return syncRelocationDirectory(dir)
			}
			late := errors.New("shared-pool registration appeared")
			registrations := func(_ context.Context, ws string) error {
				if ws != stage {
					return nil
				}
				meta := filepath.Join(ws, ".yesman", "worktrees.json")
				switch change {
				case "record":
					record.Owner = "different-owner"
					data, err := json.Marshal([]WorktreeRecord{record})
					mustDeletion(t, err)
					return os.WriteFile(meta, data, 0600)
				case "missing record":
					return os.Remove(meta)
				case "missing receipt", "replaced receipt":
					files, err := os.ReadDir(folder)
					if err != nil || len(files) != 1 {
						t.Fatal(files, err)
					}
					file := filepath.Join(folder, files[0].Name())
					if change == "missing receipt" {
						return os.Remove(file)
					}
					data, err := os.ReadFile(file)
					mustDeletion(t, err)
					mustDeletion(t, os.Rename(file, filepath.Join(other, "original-receipt")))
					return os.WriteFile(file, data, 0600)
				case "late registration":
					return late
				}
				return nil
			}
			wantErr := error(errRelocationReceiptChanged)
			wantCalls := 3
			switch change {
			case "late registration":
				wantErr, wantCalls = late, 2
			case "missing record":
				wantCalls = 2
			}
			if detached, err := s.Delete(t.Context(), target, registrations, nil, proof); detached || !errors.Is(err, wantErr) || flushes != 4 || proofCalls != wantCalls {
				t.Fatalf("deletion detached=%v err=%v flushes=%d proof calls=%d", detached, err, flushes, proofCalls)
			}
			mustDeletionBytes(t, filepath.Join(stage, note), "staged bytes")
			for _, dir := range []string{destination, other} {
				mustDeletionBytes(t, filepath.Join(dir, note), "outside checkout")
			}
			if change != "late registration" {
				return
			}
			// A new operation must establish durability again, even when the
			// previous operation left an exact receipt alongside the stage.
			cold := New(s.root, s.archive)
			flushFailure := errors.New("could not establish durability this time")
			cold.syncRelocationDir = func(*os.Root) error { return flushFailure }
			pending, err := cold.StoredWorkspaces()
			if err != nil || len(pending) != 1 || !pending[0].Pending {
				t.Fatal(pending, err)
			}
			empty := func(context.Context, string) error { return nil }
			if _, err := cold.Delete(t.Context(), pending[0], empty, nil, proof); !errors.Is(err, flushFailure) {
				t.Fatal(err)
			}
			mustDeletionBytes(t, filepath.Join(stage, note), "staged bytes")
			cold.syncRelocationDir = syncRelocationDirectory
			mustDeletion(t, func() error {
				_, err := cold.Delete(t.Context(), pending[0], empty, nil, proof)
				return err
			}())
			if _, err := os.Lstat(stage); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
		})
	}
}
