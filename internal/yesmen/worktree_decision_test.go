package yesmen

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWorktreeDecisionSerializesBoardMutation(t *testing.T) {
	root := t.TempDir()
	store := New(filepath.Join(root, "workspaces"), filepath.Join(root, "archive"))
	y, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Hire("bob"); err != nil {
		t.Fatal(err)
	}
	ready, cut := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- store.TryWorktreeCommit(y, func() error { close(ready); <-cut; return nil })
	}()
	<-ready
	title := "next task"
	add := BoardChange{Add: []TaskEdit{{Title: &title}}}
	alice := make(chan error, 1)
	go func() { _, err := store.ChangeBoard("alice", add); alice <- err }()
	_, independentErr := store.ChangeBoard("bob", add)
	select {
	case <-alice:
		close(cut)
		<-finished
		t.Fatal("board changed during its final worktree decision")
	case <-time.After(30 * time.Millisecond):
	}
	close(cut)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := <-alice; err != nil {
		t.Fatal(err)
	}
	if independentErr != nil {
		t.Fatal(independentErr)
	}
}
