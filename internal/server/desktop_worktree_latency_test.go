package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDesktopPreviewIncompleteRowsCannotBeConfirmed(t *testing.T) {
	f := newCleanupFixture(t)
	fast, err := f.s.checkoutOwned(t.Context(), f.owner, f.y, CheckoutRequest{
		Repo: f.repo.Name, Dir: "fast", Branch: "fast", Purpose: "author", Task: "t1",
	})
	if err != nil {
		t.Fatal(err)
	}
	slow := f.checkout(t, "author", "slow")
	f.finishTask(t)
	f.s.HasOpenPull = func(ctx context.Context, _, _, branch string) (bool, error) {
		if branch == slow.Branch {
			<-ctx.Done()
			return false, ctx.Err()
		}
		return false, nil
	}
	for _, w := range []string{fast.Path, slow.Path} {
		if err := os.WriteFile(filepath.Join(w, "local-only"), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	d := &DesktopWorktrees{Server: f.s}
	preview := previewTree(t, d, f.y, nil, true)
	if !preview.Partial || len(preview.Rows) != 2 || preview.Rows[0].Dir != fast.Dir || preview.Rows[0].Reason != "" ||
		preview.Rows[1].Dir != slow.Dir || preview.Rows[1].Reason != previewTimedOut {
		t.Fatalf("preview = %+v", preview)
	}
	confirmTree(t, d, f.y, preview.Token, true)
	if _, err := os.Stat(fast.Path); !os.IsNotExist(err) {
		t.Fatal("verified checkout remained", err)
	}
	if got, err := os.ReadFile(filepath.Join(slow.Path, "local-only")); err != nil || string(got) != "fixture" {
		t.Fatal("unchecked checkout was modified", err)
	}
	if _, err := d.Confirm(t.Context(), f.y.Name, f.y.HiredAt, preview.Token, nil); err == nil {
		t.Fatal("reused confirmation token")
	}
}

func TestDesktopPreviewCancelStopsSourceCheck(t *testing.T) {
	f := newCleanupFixture(t)
	w := f.checkout(t, "author", "slow")
	f.finishTask(t)
	entered := make(chan struct{})
	f.s.HasOpenPull = func(ctx context.Context, _, _, _ string) (bool, error) {
		close(entered)
		<-ctx.Done()
		return false, ctx.Err()
	}
	d := &DesktopWorktrees{Server: f.s}
	type answer struct {
		result WorktreeCleanupPreview
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		result, err := d.PreviewRequest(t.Context(), f.y.Name, f.y.HiredAt, nil, nil, "current-check")
		done <- answer{result, err}
	}()
	select {
	case <-entered:
	case <-time.After(6 * time.Second):
		t.Fatal("preview did not reach the remote check")
	}
	d.Cancel("old-check")
	select {
	case <-done:
		t.Fatal("a stale cancellation stopped the current check")
	default:
	}
	d.Cancel("current-check")
	select {
	case a := <-done:
		if a.result.Token != "" || a.err == nil {
			t.Fatalf("canceled preview issued confirmation: %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not stop the remote check")
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Fatal("canceled preview changed the directory", err)
	}
}

func TestDesktopPreviewWhenWorkspaceBusy(t *testing.T) {
	f := newCleanupFixture(t)
	lock := f.s.worktreeLock(f.y.Name)
	lock.Lock()
	defer lock.Unlock()
	d := &DesktopWorktrees{Server: f.s}
	p, err := d.Preview(t.Context(), f.y.Name, f.y.HiredAt, nil, nil)
	if err == nil || err.Error() != previewBusy || p.Token != "" {
		t.Fatalf("busy preview: %+v, %v", p, err)
	}
}
