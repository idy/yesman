package yesmen

import (
	"path/filepath"
	"strings"
	"testing"
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
