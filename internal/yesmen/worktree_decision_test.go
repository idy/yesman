package yesmen

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWorktreeTaskRoundSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	cold := func() *Store { return New(filepath.Join(root, "workspaces"), filepath.Join(root, "archive")) }
	store := cold()
	y, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	// An existing dropped task predating the generation field. Reopening it
	// starts a fresh generation even if the final status and clock repeat.
	task := Task{ID: "t1", Title: "Prepare the branch", Owner: "author", Status: TaskDropped}
	if err := writeJSON(y.BoardFile(), Board{Tasks: []Task{task}, Next: 1}); err != nil {
		t.Fatal(err)
	}
	change := func(edits ...TaskEdit) Task {
		t.Helper()
		if _, err := store.ChangeBoard(y.Name, BoardChange{Update: edits}); err != nil {
			t.Fatal(err)
		}
		store = cold()
		board, err := store.Board(y.Name)
		if err != nil {
			t.Fatal(err)
		}
		return board.Tasks[0]
	}
	note := TaskEdit{ID: "t1", Note: ptr("Evidence moved")}
	if got := change(note); got.WorktreeGeneration != "" {
		t.Fatal("a note-only edit changed the legacy round")
	}
	state := func(status string) TaskEdit { return TaskEdit{ID: "t1", Status: &status} }
	one := change(state(TaskDoing), state(TaskDropped))
	two := change(state(TaskDoing), state(TaskDropped))
	if one.WorktreeGeneration == "" || two.WorktreeGeneration == "" || one.WorktreeGeneration == two.WorktreeGeneration || !two.DoneAt.IsZero() {
		t.Fatal("repeated dropped closes shared a round or changed the done statistics")
	}
	if got := change(note, state(TaskDropped)); two.WorktreeGeneration != got.WorktreeGeneration {
		t.Fatal("an unchanged close or note edit started another round")
	}
	got := change(TaskEdit{ID: "t1", Owner: ptr("other")}, TaskEdit{ID: "t1", Owner: ptr("author")})
	if got.WorktreeGeneration == two.WorktreeGeneration {
		t.Fatal("returning to the original owner reused a previous round")
	}
	link := TaskSchedule{Plan: "s1", Run: "s1-m-fixture"}
	if _, _, err := store.AddScheduledTask(y.Name, link, "Run", ""); err != nil {
		t.Fatal(err)
	}
	var first string
	for i, status := range []string{TaskDropped, TaskDoing, TaskDropped} {
		updated, _, err := store.UpdateScheduledTask(y.Name, link.Run, func(t *Task) bool { t.Status = status; return true })
		if err != nil || updated.WorktreeGeneration == "" {
			t.Fatal("missing scheduled task round", err)
		}
		if i == 0 {
			first = updated.WorktreeGeneration
		} else if updated.WorktreeGeneration == first {
			t.Fatal("scheduler reused the first close")
		}
		store = cold()
	}
}

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
