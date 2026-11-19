package yesmen

import (
	"context"
	"errors"
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

type interruptDeletion struct {
	context.Context
	interrupt func()
}

func (c interruptDeletion) Err() error { c.interrupt(); return c.Context.Err() }
