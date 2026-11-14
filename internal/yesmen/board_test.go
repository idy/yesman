package yesmen

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ptr(s string) *string { return &s }

func TestBoard(t *testing.T) {
	tmp := t.TempDir()
	s := New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	if _, err := s.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	b, err := s.Board("alice")
	if err != nil || b.Tasks == nil || len(b.Tasks) != 0 {
		t.Fatalf("new board = %+v, %v", b, err)
	}

	b, err = s.ChangeBoard("alice", BoardChange{Add: []TaskEdit{
		{Title: ptr("  Fix the   login timeout ")},
		{Title: ptr("Update the install notes"), Status: ptr("Doing"), Owner: ptr("readme")},
		{Title: ptr("Windows build")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Tasks) != 3 || b.Tasks[0].ID != "t1" || b.Tasks[0].Title != "Fix the login timeout" || b.Tasks[0].Status != TaskTodo {
		t.Fatalf("after adding: %+v", b.Tasks)
	}
	if b.Tasks[1].Status != TaskDoing || b.Tasks[1].Owner != "readme" {
		t.Errorf("second task = %+v", b.Tasks[1])
	}

	// IDs as agents write them; a removed task's ID is not reused.
	b, err = s.ChangeBoard("alice", BoardChange{
		Update: []TaskEdit{{ID: "#1", Status: ptr("blocked"), Note: ptr("waiting for credentials")}},
		Remove: []string{"3"},
		Add:    []TaskEdit{{Title: ptr("Release")}},
		Order:  []string{"t4", "T2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, task := range b.Tasks {
		ids = append(ids, task.ID)
	}
	if strings.Join(ids, " ") != "t4 t2 t1" {
		t.Errorf("order = %v", ids)
	}
	if got := b.Tasks[2]; got.Status != TaskBlocked || got.Note != "waiting for credentials" {
		t.Errorf("updated task = %+v", got)
	}

	// An invalid part leaves the whole change unapplied.
	for _, bad := range []BoardChange{
		{Update: []TaskEdit{{ID: "t1", Status: ptr("finished")}}},
		{Update: []TaskEdit{{ID: "t9", Title: ptr("x")}}},
		{Add: []TaskEdit{{Title: ptr("ok")}, {Title: ptr("   ")}}},
		{Add: []TaskEdit{{Detail: ptr("no title")}}},
		{Add: []TaskEdit{{ID: "t1", Title: ptr("again")}}},
		{Add: []TaskEdit{{Title: ptr("bad owner"), Owner: ptr("Not A Name")}}},
		{Remove: []string{"t2", "t2"}},
		{Order: []string{"t7"}},
	} {
		if _, err := s.ChangeBoard("alice", bad); err == nil {
			t.Errorf("change %+v was accepted", bad)
		}
	}
	after, _ := s.Board("alice")
	if len(after.Tasks) != 3 || after.Next != 4 || after.Tasks[1].Status != TaskDoing {
		t.Errorf("a rejected change changed the board: %+v", after)
	}
}

func TestWorkerSubjectAndReopen(t *testing.T) {
	tmp := t.TempDir()
	s := New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	if _, err := s.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	b, err := s.ChangeBoard("alice", BoardChange{Add: []TaskEdit{
		{Title: ptr("Old docs pass"), Status: ptr("done"), Owner: ptr("docs")},
		{Title: ptr("Write the install notes"), Status: ptr("doing"), Owner: ptr("docs")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		w    Worker
		want string
	}{
		{Worker{Name: "docs", Title: "Given title", Task: "x"}, "Given title"},
		{Worker{Name: "docs", Task: "Do task t2.\nThen report."}, "Write the install notes"},
		{Worker{Name: "other", Task: "\n  Do task t9.\nThen report."}, "Do task t9."},
	} {
		if got := c.w.Subject(b); got != c.want {
			t.Errorf("Subject(%+v) = %q, want %q", c.w, got, c.want)
		}
	}

	if err := s.AddWorker("alice", Worker{Name: "docs", Task: "t", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if reopened, err := s.ReopenWorker("alice", "docs"); err != nil || reopened {
		t.Errorf("reopening an open worker = %v, %v", reopened, err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	if err := s.FinishWorker("alice", "docs", at); err != nil {
		t.Fatal(err)
	}
	if ws, _ := s.Workers("alice"); !ws[0].FinishedAt.Equal(at) {
		t.Errorf("finished at %v, want %v", ws[0].FinishedAt, at)
	}
	if reopened, err := s.ReopenWorker("alice", "docs"); err != nil || !reopened {
		t.Errorf("reopening a finished worker = %v, %v", reopened, err)
	}
	if ws, _ := s.Workers("alice"); !ws[0].FinishedAt.IsZero() {
		t.Error("the worker is still finished")
	}
	if err := s.FinishWorker("alice", "gone", at); err != nil {
		t.Errorf("finishing a removed worker: %v", err)
	}
}

func TestWorkerNumbers(t *testing.T) {
	tmp := t.TempDir()
	s := New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	y, err := s.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	// Workers recorded before numbers are numbered in the order they started.
	t0 := time.Now().UTC()
	if err := writeJSON(filepath.Join(y.MetaDir(), "workers.json"), []Worker{
		{Name: "late", Task: "t", StartedAt: t0.Add(time.Minute)},
		{Name: "early", Task: "t", StartedAt: t0},
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"next", "last"} {
		if err := s.AddWorker("alice", Worker{Name: name, Task: "t", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RemoveWorker("alice", "next"); err != nil {
		t.Fatal(err)
	}
	ws, _ := s.Workers("alice")
	got := map[string]int{}
	for _, w := range ws {
		got[w.Name] = w.Number
	}
	if got["early"] != 1 || got["late"] != 2 || got["last"] != 4 || len(got) != 3 {
		t.Errorf("numbers = %v", got)
	}
}

// A task records when it became done and who owned it then, keeps them
// while it stays done, and forgets them when reopened or dropped.
func TestBoardDoneMetadata(t *testing.T) {
	tmp := t.TempDir()
	s := New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	if _, err := s.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	b, err := s.ChangeBoard("alice", BoardChange{Add: []TaskEdit{
		{Title: ptr("Ship"), Owner: ptr("w1")},
		{Title: ptr("Born done"), Status: ptr("done"), Owner: ptr("w2")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !b.Tasks[0].DoneAt.IsZero() || b.Tasks[0].DoneOwner != "" {
		t.Fatalf("open task has done metadata: %+v", b.Tasks[0])
	}
	if b.Tasks[1].DoneAt.IsZero() || b.Tasks[1].DoneOwner != "w2" {
		t.Fatalf("task added done = %+v", b.Tasks[1])
	}

	b, err = s.ChangeBoard("alice", BoardChange{Update: []TaskEdit{{ID: "t1", Status: ptr("done")}}})
	if err != nil {
		t.Fatal(err)
	}
	done := b.Tasks[0]
	if done.DoneAt.IsZero() || done.DoneOwner != "w1" || !done.DoneAt.Equal(done.UpdatedAt) {
		t.Fatalf("done task = %+v", done)
	}

	// Staying done keeps the time and owner, even when the owner changes
	// and the note is edited later.
	time.Sleep(2 * time.Millisecond)
	b, err = s.ChangeBoard("alice", BoardChange{Update: []TaskEdit{{ID: "t1", Status: ptr("done"), Owner: ptr("lead"), Note: ptr("merged")}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Tasks[0]; !got.DoneAt.Equal(done.DoneAt) || got.DoneOwner != "w1" || got.Owner != "lead" {
		t.Fatalf("still done = %+v, want doneAt %v by w1", got, done.DoneAt)
	}

	// Reopening forgets them; done again records the new time and owner.
	for _, status := range []string{"doing", "dropped"} {
		b, err = s.ChangeBoard("alice", BoardChange{Update: []TaskEdit{{ID: "t1", Status: ptr(status)}}})
		if err != nil {
			t.Fatal(err)
		}
		if got := b.Tasks[0]; !got.DoneAt.IsZero() || got.DoneOwner != "" {
			t.Fatalf("%s task kept done metadata: %+v", status, got)
		}
	}
	b, err = s.ChangeBoard("alice", BoardChange{Update: []TaskEdit{{ID: "t1", Status: ptr("done")}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Tasks[0]; !got.DoneAt.After(done.DoneAt) || got.DoneOwner != "lead" {
		t.Fatalf("done again = %+v", got)
	}

	// Persisted, and read back.
	b, err = s.Board("alice")
	if err != nil || b.Tasks[0].DoneOwner != "lead" || b.Tasks[0].DoneAt.IsZero() {
		t.Fatalf("read back = %+v, %v", b.Tasks, err)
	}
}

// A task done before the metadata existed is not given a completion time.
func TestBoardLegacyDoneStaysUnrecorded(t *testing.T) {
	b := Board{Tasks: []Task{{ID: "t1", Title: "Old", Status: TaskDone, Owner: "w1"}}, Next: 1}
	if err := b.apply(BoardChange{Update: []TaskEdit{{ID: "t1", Note: ptr("released")}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := b.Tasks[0]; !got.DoneAt.IsZero() || got.DoneOwner != "" {
		t.Fatalf("legacy done task = %+v", got)
	}
}
