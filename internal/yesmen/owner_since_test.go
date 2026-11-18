package yesmen

import (
	"testing"
	"time"
)

// A task belongs to the worker it was handed to, not to its name.
func TestTaskOwnerIncarnation(t *testing.T) {
	s := waitStore(t)
	old := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	// Handed over before the worker starts: bound to whichever w1 is current.
	if _, err := s.ChangeBoard("alice", BoardChange{Add: []TaskEdit{{Title: sp("Early"), Owner: sp("w1")}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWorker("alice", Worker{Name: "w1", Task: "x", StartedAt: old}); err != nil {
		t.Fatal(err)
	}
	b, err := s.ChangeBoard("alice", BoardChange{Add: []TaskEdit{{Title: sp("Late"), Owner: sp("w1")}}})
	if err != nil {
		t.Fatal(err)
	}
	if !b.Tasks[0].OwnerSince.IsZero() || !b.Tasks[1].OwnerSince.Equal(old) {
		t.Fatalf("binding: %+v", b.Tasks)
	}
	// Removing w1 binds what still has no incarnation to the one removed.
	if err := s.RemoveWorker("alice", "w1"); err != nil {
		t.Fatal(err)
	}
	if b, _ = s.Board("alice"); !b.Tasks[0].OwnerSince.Equal(old) || !b.Tasks[1].OwnerSince.Equal(old) {
		t.Fatalf("after removal: %+v", b.Tasks)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.AddWorker("alice", Worker{Name: "w1", Task: "y", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	// A note keeps it; handing it to w1 again binds it to the new one.
	b, err = s.ChangeBoard("alice", BoardChange{Update: []TaskEdit{{ID: "t1", Note: sp("n")}, {ID: "t2", Owner: sp("w1")}}})
	if err != nil {
		t.Fatal(err)
	}
	if !b.Tasks[0].OwnerSince.Equal(old) || !b.Tasks[1].OwnerSince.Equal(now) {
		t.Fatalf("rebinding: %+v", b.Tasks)
	}
	// No owner, no incarnation.
	if b, _ = s.ChangeBoard("alice", BoardChange{Update: []TaskEdit{{ID: "t2", Owner: sp("")}}}); !b.Tasks[1].OwnerSince.IsZero() {
		t.Fatalf("cleared owner kept an incarnation: %+v", b.Tasks[1])
	}
}
