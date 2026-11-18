package yesmen

import (
	"path/filepath"
	"testing"
)

// The scheduler's board writes count like every board change, before they
// are written, so a task notice read before them is refused (Revision).
func TestR8ScheduledBoardWritesCount(t *testing.T) {
	tmp := t.TempDir()
	s := New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	if _, err := s.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	link := TaskSchedule{Plan: "s1", Run: "s1-20261118t0900"}
	r0 := s.Revision("alice")
	if _, added, err := s.AddScheduledTask("alice", link, "Morning check", "d"); err != nil || !added {
		t.Fatalf("add: %v %v", added, err)
	}
	r1 := s.Revision("alice")
	if r1 == r0 {
		t.Fatal("adding a scheduled run's task did not count")
	}
	if _, added, err := s.AddScheduledTask("alice", link, "Morning check", "d"); err != nil || added {
		t.Fatalf("re-add: %v %v", added, err)
	}
	if s.Revision("alice") != r1 {
		t.Fatal("finding the run's existing task counted as a change")
	}
	if _, changed, err := s.UpdateScheduledTask("alice", link.Run, func(t *Task) bool { t.Status = TaskDone; return true }); err != nil || !changed {
		t.Fatalf("update: %v %v", changed, err)
	}
	if s.Revision("alice") == r1 {
		t.Fatal("updating a scheduled run's task did not count")
	}
}
