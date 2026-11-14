package yesmen

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// A run's board task is added once however often it is asked for, keeps its
// link through ordinary edits, and agents cannot set or forge the link.
func TestAddScheduledTaskIsIdempotentAndItsLinkIsTheSchedulers(t *testing.T) {
	tmp := t.TempDir()
	s := New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	if _, err := s.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	link := TaskSchedule{Plan: "s2", Run: "s2-20261116t0900"}
	first, added, err := s.AddScheduledTask("alice", link, strings.Repeat("长", 300), "detail")
	if err != nil || !added || first.ID != "t1" || first.Schedule == nil || *first.Schedule != link || len([]rune(first.Title)) != 200 {
		t.Fatalf("first: %+v %v %v", first, added, err)
	}
	again, added, err := s.AddScheduledTask("alice", link, "Other", "other")
	if err != nil || added || again.ID != "t1" {
		t.Fatalf("again: %+v %v %v", again, added, err)
	}
	done := TaskDone
	b, err := s.ChangeBoard("alice", BoardChange{Update: []TaskEdit{{ID: "t1", Status: &done}}})
	if err != nil || b.Tasks[0].Schedule == nil || *b.Tasks[0].Schedule != link || b.Tasks[0].Status != TaskDone {
		t.Fatalf("after an edit: %+v %v", b, err)
	}
	// The board tool's JSON has no way to name a run.
	var change BoardChange
	if err := json.Unmarshal([]byte(`{"add":[{"title":"forged","schedule":{"plan":"s9","run":"s9-x"}}]}`), &change); err != nil {
		t.Fatal(err)
	}
	b, err = s.ChangeBoard("alice", change)
	if err != nil || b.Tasks[1].Schedule != nil {
		t.Fatalf("a forged link: %+v %v", b.Tasks[1], err)
	}
	if _, _, err := s.AddScheduledTask("alice", TaskSchedule{Plan: "s2"}, "x", ""); err == nil {
		t.Fatal("a task without its run")
	}
}

// The scheduler changes only its own run's task, and the link stays.
func TestUpdateScheduledTask(t *testing.T) {
	tmp := t.TempDir()
	s := New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	if _, err := s.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	link := TaskSchedule{Plan: "s1", Run: "s1-m-0123456789abcdef"}
	if _, _, err := s.AddScheduledTask("alice", link, "Run", ""); err != nil {
		t.Fatal(err)
	}
	title := "Mine"
	if _, err := s.ChangeBoard("alice", BoardChange{Add: []TaskEdit{{Title: &title}}}); err != nil {
		t.Fatal(err)
	}
	task, changed, err := s.UpdateScheduledTask("alice", link.Run, func(t *Task) bool {
		t.Status, t.Note, t.Schedule = TaskBlocked, strings.Repeat("n", 5000), nil
		return true
	})
	if err != nil || !changed || task.Status != TaskBlocked || task.Schedule == nil || *task.Schedule != link || len(task.Note) > maxNoteLen*4 {
		t.Fatalf("update: %+v %v %v", task, changed, err)
	}
	if _, changed, _ := s.UpdateScheduledTask("alice", "s9-none", func(*Task) bool { return true }); changed {
		t.Fatal("changed a task of another run")
	}
	b, _ := s.Board("alice")
	if b.Tasks[1].Status != TaskTodo || b.Tasks[1].Title != "Mine" {
		t.Fatalf("another task changed: %+v", b.Tasks[1])
	}
	if _, _, err := s.UpdateScheduledTask("alice", link.Run, func(t *Task) bool { t.Status = "finished"; return true }); err == nil {
		t.Fatal("an unknown status was saved")
	}
}
