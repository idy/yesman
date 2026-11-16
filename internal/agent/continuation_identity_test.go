package agent

import (
	"context"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func TestR2ReusedWorkerNameDoesNotCarryOldTask(t *testing.T) {
	m, s := newTestManager(t, 0)
	prompts := promptsTo(m)
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", Task: "old task", StartedAt: time.Now().UTC().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	addTasks(t, s, yesmen.TaskEdit{Title: str("Old orphan"), Status: str("doing"), Owner: str("w1")})
	if err := m.RemoveWorker("alice", "w1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartWorker(context.Background(), "alice", "w1", "New unrelated task", "test-run:retry"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "alice/w1", Working)
	addTasks(t, s, yesmen.TaskEdit{Title: str("New work"), Status: str("doing"), Owner: str("w1")})
	m.continueTasks("alice")
	got := noticesOf(prompts("alice/lead"))
	if noticesListing(got, "t1") == 0 {
		t.Fatalf("same-name replacement doing new work masks old generation's orphan: %q", got)
	}
	if noticesListing(got, "t2") != 0 {
		t.Fatalf("replacement's actual task incorrectly orphaned: %q", got)
	}
}
