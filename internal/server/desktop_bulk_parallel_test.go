//go:build darwin || linux

package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func TestDesktopBulkChecksTrackingAndRechecksItsBinding(t *testing.T) {
	f := newCleanupFixture(t)
	w := f.checkout(t, "author", "tracked")
	f.finishTask(t)
	cleanupGit(t, w.Path, "branch", "--set-upstream-to=origin/main", w.Branch)
	d := &DesktopWorktrees{Server: f.s}
	f.s.HasOpenPull = func(_ context.Context, _, _, branch string) (bool, error) { return branch == "main", nil }
	if p := previewTree(t, d, f.y, nil, false); p.Rows[0].Reason != "This branch has an open pull request." {
		t.Fatal(p.Rows)
	}
	f.s.HasOpenPull = func(context.Context, string, string, string) (bool, error) { return false, nil }
	p := previewTree(t, d, f.y, nil, true)
	cleanupGit(t, w.Path, "branch", "--unset-upstream", w.Branch)
	if row := confirmTree(t, d, f.y, p.Token, false); row.Reason == "" || row.Removed {
		t.Fatal("confirmation ignored the changed tracking branch", row)
	}
	p = previewTree(t, d, f.y, nil, true)
	confirmTree(t, d, f.y, p.Token, true)
}

func TestDesktopBulkOverlapsIndependentOwnersAndDrainsOnCancel(t *testing.T) {
	f := newCleanupFixture(t)
	f.checkout(t, "review", "a-first")
	f.checkout(t, "review", "b-same-owner")
	owner, title := "cleanup", "Other owner"
	board, err := f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: &title, Owner: &owner}}})
	if err != nil {
		t.Fatal(err)
	}
	otherTask := board.Tasks[len(board.Tasks)-1].ID
	if _, err := f.s.checkoutOwned(t.Context(), f.delegate, f.y, CheckoutRequest{
		Repo: f.repo.Name, Dir: "c-other-owner", Purpose: "review", Task: otherTask, Ref: f.head,
	}); err != nil {
		t.Fatal(err)
	}
	f.finishTask(t)
	done := yesmen.TaskDone
	if _, err := f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: otherTask, Status: &done}}}); err != nil {
		t.Fatal(err)
	}

	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gate := t.TempDir()
	// Hold one index check for each owner by its actual Git cwd.
	// Even with both using the same repository, both owners must enter.
	// The same-owner sibling must wait without generating a spurious hold.
	shim := `#!/bin/sh
for arg do
    if test "$arg" = --resolve-undo; then
        tree=${PWD##*/}
        : > "$BULK_TEST_GATE/$tree"
        case "$tree" in
          a-first|b-same-owner|c-other-owner)
            if ! test -f "$BULK_TEST_GATE/release"; then exec /bin/sleep 10; fi ;;
        esac
        break
    fi
done
exec "$BULK_TEST_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(gate, "git"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", gate+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BULK_TEST_GIT", git)
	t.Setenv("BULK_TEST_GATE", gate)
	d := &DesktopWorktrees{Server: f.s}
	request := prepareCheck(t, d, f.y.Name, f.y.HiredAt, nil)
	defer d.Cancel(request)
	type answer struct {
		preview WorktreeCleanupPreview
		err     error
	}
	result := make(chan answer, 1)
	ctx, cancel := context.WithCancel(t.Context())
	joined := make(chan struct{})
	defer func() {
		cancel()
		<-joined
	}()
	go func() {
		defer close(joined)
		p, err := d.PreviewRequest(ctx, f.y.Name, f.y.HiredAt, nil, nil, request)
		result <- answer{p, err}
	}()
	entered := func(name string) bool {
		_, err := os.Stat(filepath.Join(gate, name))
		return err == nil
	}
	deadline := time.NewTimer(6 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !(entered("a-first") || entered("b-same-owner")) || !entered("c-other-owner") {
		select {
		case got := <-result:
			t.Fatalf("bulk preview returned before the independent Git checks overlapped: %+v", got)
		case <-deadline.C:
			t.Fatalf("independent checks did not overlap: a=%t b=%t c=%t", entered("a-first"), entered("b-same-owner"), entered("c-other-owner"))
		case <-tick.C:
		}
	}
	if entered("a-first") && entered("b-same-owner") {
		t.Fatal("same-owner rows ran at the same time")
	}
	d.Cancel(request)
	select {
	case got := <-result:
		if got.err == nil || got.preview.Token != "" {
			t.Fatalf("canceled check returned authority: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled preview did not join its local checks")
	}
	if entered("a-first") && entered("b-same-owner") {
		t.Fatal("queued Git check started after cancellation")
	}

	if err := os.WriteFile(filepath.Join(gate, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	p := previewTree(t, d, f.y, nil, true)
	defer d.Cancel(p.Token)
	if len(p.Rows) != 3 {
		t.Fatalf("retry rows: %+v", p.Rows)
	}
	for i, want := range []string{"a-first", "b-same-owner", "c-other-owner"} {
		if p.Rows[i].Dir != want || p.Rows[i].Reason != "" {
			t.Fatalf("retry row %d: %+v", i, p.Rows[i])
		}
	}
}
