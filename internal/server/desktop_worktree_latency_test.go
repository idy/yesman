package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

func prepareCheck(t *testing.T, d *DesktopWorktrees, name string, since time.Time, selected *pool.Worktree) string {
	t.Helper()
	id, err := d.Prepare(name, since, selected)
	if err != nil || id == "" {
		t.Fatalf("prepare: %q %v", id, err)
	}
	return id
}

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
	request := prepareCheck(t, d, f.y.Name, f.y.HiredAt, nil)
	go func() {
		result, err := d.PreviewRequest(t.Context(), f.y.Name, f.y.HiredAt, nil, nil, request)
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
	d.Cancel(request)
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
	d := &DesktopWorktrees{Server: f.s}
	assertBusy := func() {
		t.Helper()
		p, err := d.Preview(t.Context(), f.y.Name, f.y.HiredAt, nil, nil)
		if !errors.Is(err, yesmen.ErrWorkspaceBusy) || p.Token != "" {
			t.Fatalf("busy preview: %+v, %v", p, err)
		}
	}
	lock := f.s.worktreeLock(f.y.Name)
	lock.Lock()
	assertBusy()
	lock.Unlock()
	if err := f.s.Yesmen.TryWorktreeCommit(f.y, func() error {
		assertBusy() // the exclusive Store lease is held this time
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopPreviewQueuedCancelDoesNotInvalidateRetry(t *testing.T) {
	for _, name := range []string{"single", "bulk"} {
		t.Run(name, func(t *testing.T) {
			f := newCleanupFixture(t)
			w := f.checkout(t, "author", "private-tree")
			f.finishTask(t)
			d := &DesktopWorktrees{Server: f.s}
			selected := &w
			if name == "bulk" {
				selected = nil
			}
			var reads atomic.Int32
			f.s.HasOpenPull = func(context.Context, string, string, string) (bool, error) { reads.Add(1); return false, nil }
			queued := prepareCheck(t, d, f.y.Name, f.y.HiredAt, selected)
			older := prepareCheck(t, d, f.y.Name, f.y.HiredAt, selected)
			replacement := prepareCheck(t, d, f.y.Name, f.y.HiredAt, selected)
			d.Cancel(queued)
			// MyGo calls enter Go in separate goroutines. Deliver the retry
			// before the canceled and superseded invocations enter the service.
			check := func(id string) (WorktreeCleanupPreview, error) {
				return d.PreviewRequest(t.Context(), f.y.Name, f.y.HiredAt, selected, nil, id)
			}
			retry, err := check(replacement)
			if err != nil || retry.Token == "" {
				t.Fatalf("retry: %+v %v", retry, err)
			}
			for _, id := range []string{queued, older, replacement, "", retry.Token} {
				stale, err := check(id)
				if !errors.Is(err, errPreviewExpired) || stale.Token != "" {
					t.Fatalf("stale call reached preview: %+v %v", stale, err)
				}
			}
			d.Cancel(queued)
			d.Cancel(older)
			if got := reads.Load(); got != 1 {
				t.Fatalf("stale request performed remote work: %d reads", got)
			}
			if _, err := os.Stat(w.Path); err != nil {
				t.Fatal(err)
			}
			confirmTree(t, d, f.y, retry.Token, true)
			if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
				t.Fatal("the original retry token did not remove its checkout", err)
			}
			if _, err := d.Confirm(t.Context(), f.y.Name, f.y.HiredAt, retry.Token, nil); err == nil {
				t.Fatal("confirmation token was reused")
			}
		})
	}
}

func TestDesktopPreviewTicketScopeAndBound(t *testing.T) {
	f := newCleanupFixture(t)
	w := f.checkout(t, "author", "private-tree")
	f.finishTask(t)
	d := &DesktopWorktrees{Server: f.s}
	name, since := f.y.Name, f.y.HiredAt
	current := previewTree(t, d, f.y, &w, true)
	id := prepareCheck(t, d, name, since, &w)
	wrong := w
	wrong.Dir = "other-tree"
	for _, test := range []struct {
		name     string
		since    time.Time
		selected *pool.Worktree
	}{
		{"other-workspace", since, &w},
		{name, since.Add(time.Second), &w},
		{name, since, nil},
		{name, since, &wrong},
	} {
		p, err := d.PreviewRequest(t.Context(), test.name, test.since, test.selected, nil, id)
		if p.Token != "" || !errors.Is(err, errPreviewExpired) {
			t.Fatalf("cross-scope ticket: %+v %v", p, err)
		}
	}
	for range maxWorktreeChecks - 1 {
		prepareCheck(t, d, name, since, &w)
	}
	if _, err := d.Prepare(name, since, &w); !errors.Is(err, yesmen.ErrWorkspaceBusy) {
		t.Fatal("unbounded reservations", err)
	}
	// Simulate the lease aging while a bridge call is delayed.
	for key, ticket := range d.checks {
		ticket.expires = time.Now().Add(-time.Second)
		d.checks[key] = ticket
	}
	if p, err := d.PreviewRequest(t.Context(), name, since, &w, nil, id); p.Token != "" || !errors.Is(err, errPreviewExpired) {
		t.Fatalf("expired reservation ran: %+v %v", p, err)
	}
	fresh := prepareCheck(t, d, name, since, &w)
	if len(d.checks) != 1 || fresh == id {
		t.Fatal("expired reservation was retained or reused")
	}
	for range 1000 {
		d.Cancel("unknown")
	}
	if len(d.checks) != 1 {
		t.Fatal("cancel stored unknown requests")
	}
	d.Cancel(fresh)
	confirmTree(t, d, f.y, current.Token, true)
}
