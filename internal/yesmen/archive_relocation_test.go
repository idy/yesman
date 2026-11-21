package yesmen

import (
	"context"
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
