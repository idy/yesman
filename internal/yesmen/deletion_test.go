package yesmen

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Interrupt after the atomic detach and an actual unlink. On reopen only
// an explicit retry of this physical directory may finish deleting it.
func TestInterruptedDeletionReopen(t *testing.T) {
	s := waitStore(t)
	y, err := s.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.PrepareDeletion(y.Name, y.HiredAt)
	if err != nil {
		t.Fatal(err)
	}
	entry := deletingPrefix + want.Instance + "." + y.Name
	pending := filepath.Join(s.root, entry)
	if err := os.WriteFile(filepath.Join(y.Path, "A-partial"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(y.Path, "b-remains"), []byte("keep until confirmed"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fault := interruptDeletion{ctx, func() {
		if _, err := os.Stat(pending); err == nil {
			if _, err := os.Stat(filepath.Join(pending, "A-partial")); errors.Is(err, fs.ErrNotExist) {
				cancel()
			}
		}
	}}
	check := func(context.Context, string) error { return nil }
	detached, err := s.Delete(fault, want, check, func(Yesman) error { return nil })
	if !detached || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "Settings") {
		t.Fatalf("detached %v error %v", detached, err)
	}
	if _, err := os.Stat(filepath.Join(pending, "b-remains")); err != nil {
		t.Fatal(err)
	}
	newStore := New(s.root, s.archive)
	fresh, err := newStore.Hire(y.Name)
	if err != nil {
		t.Fatal(err)
	}
	list, err := newStore.StoredWorkspaces()
	if err != nil || len(list) != 1 || !list[0].Pending || list[0].Instance != want.Instance {
		t.Fatal(list, err)
	}
	if _, err := newStore.Delete(t.Context(), want, check, func(Yesman) error { t.Fatal("stopped new hire"); return nil }); err == nil {
		t.Fatal("stale retry accepted")
	}
	if detached, err := newStore.Delete(t.Context(), list[0], check, func(Yesman) error { t.Fatal("stopped new hire"); return nil }); err != nil || detached {
		t.Fatal(detached, err)
	}
	if got, err := newStore.Get(y.Name); err != nil || got != fresh {
		t.Fatal(got, err)
	}
	if _, err := os.Stat(pending); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestDeletionAllowsConcurrentSameNameHireAfterDetach(t *testing.T) {
	s := waitStore(t)
	y, err := s.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.PrepareDeletion(y.Name, y.HiredAt)
	if err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(s.root, deletingPrefix+old.Instance+"."+old.Entry)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var once sync.Once
	ctx := interruptDeletion{t.Context(), func() {
		if _, err := os.Stat(pending); err == nil {
			once.Do(func() { close(entered); <-release })
		}
	}}
	done := make(chan error, 1)
	go func() {
		_, err := s.Delete(ctx, old, func(context.Context, string) error { return nil }, func(Yesman) error { return nil })
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("expected detached deletion: %v", err)
	}
	fresh, err := s.Hire(y.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Use(y.Name, func(current Yesman) error {
		if current != fresh {
			t.Fatalf("unexpected hire %v", current)
		}
		return os.WriteFile(filepath.Join(current.Path, "keep"), []byte("new hire"), 0600)
	}); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(fresh.Path, "keep")); err != nil || string(got) != "new hire" {
		t.Fatal(string(got), err)
	}
}

func TestDeletionWaitsForMetadataTransaction(t *testing.T) {
	s := waitStore(t)
	y, err := s.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.PrepareDeletion(y.Name, y.HiredAt)
	if err != nil {
		t.Fatal(err)
	}
	transaction := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.Delete(t.Context(), target, func(context.Context, string) error { return nil }, func(Yesman) error {
			s.mu.Lock()
			close(transaction)
			return nil
		})
		done <- err
	}()
	select {
	case <-transaction:
	case err := <-done:
		t.Fatalf("expected transaction before rename: %v", err)
	}
	select {
	case err := <-done:
		s.mu.Unlock()
		t.Fatalf("deletion overtook metadata: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	_, err = os.Stat(y.Path)
	s.mu.Unlock()
	if err != nil {
		t.Fatal("workspace moved while metadata held", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A supported registry can exceed the old deletion reader's 16 MiB budget.
// Check the whole registry, including an outstanding checkout at the end.
func TestDeletionWithLargeWorktreeHistory(t *testing.T) {
	for _, mode := range []string{"live", "archived"} {
		t.Run(mode, func(t *testing.T) {
			s := waitStore(t)
			y, err := s.Get("alice")
			if err != nil {
				t.Fatal(err)
			}
			const head = "0123456789abcdef0123456789abcdef01234567"
			evidence := strings.Repeat("See the saved report for the verified source and recovery instructions. ", 210)
			rows := make([]WorktreeRecord, 1150)
			for i := range rows {
				rows[i] = WorktreeRecord{ID: fmt.Sprintf("%032x", i+1), Dir: "review", Repo: "github.com/example/project", Purpose: "review", Head: head,
					Owner: "reviewer", OwnerSince: y.HiredAt, Removed: true, Completion: &WorktreeCompletion{Head: head, Ref: head, Evidence: evidence, Outcome: "pass", Unused: true}}
			}
			last := len(rows) - 1
			rows[last].Dir = "last-review"
			rows[last].Removed = mode == "archived"
			if err := s.SaveWorktreeRecords(y.Name, rows); err != nil {
				t.Fatal(err)
			}
			st, err := os.Stat(filepath.Join(y.MetaDir(), "worktrees.json"))
			if err != nil {
				t.Fatal(err)
			}
			if st.Size() <= 16<<20 || st.Size() > maxWorktreeState {
				t.Fatal(st.Size())
			}
			cold := New(s.root, s.archive)
			check := func(context.Context, string) error { return nil }
			stop := func(Yesman) error { return nil }
			target, err := cold.PrepareDeletion(y.Name, y.HiredAt)
			if err != nil {
				t.Fatal(err)
			}
			dir := y.Path
			if mode == "live" {
				if moved, err := cold.Delete(t.Context(), target, check, stop); moved || err == nil || !strings.Contains(err.Error(), rows[last].Dir) {
					t.Fatalf("outstanding record at end of %d bytes: moved=%v err=%v", st.Size(), moved, err)
				}
				completed := rows[last]
				completed.Removed = true
				if err := cold.CommitWorktreeRecord(y, rows[last], completed); err != nil {
					t.Fatal(err)
				}
			} else {
				dir, err = cold.Archive(y.Name, nil)
				if err != nil {
					t.Fatal(err)
				}
				stored, err := cold.StoredWorkspaces()
				if err != nil || len(stored) != 1 {
					t.Fatal(stored, err)
				}
				target = stored[0]
			}
			if _, err := cold.Delete(t.Context(), target, check, stop); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
		})
	}
}

type interruptDeletion struct {
	context.Context
	interrupt func()
}

func (c interruptDeletion) Err() error { c.interrupt(); return c.Context.Err() }
