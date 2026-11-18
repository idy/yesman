package agent

import (
	"context"
	"github.com/idy/yesman/internal/yesmen"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReviewR5QueuedEvaluationPersistsLatestStop(t *testing.T) {
	m, s := newTestManager(t, 0)
	y, _ := s.Get("alice")
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	addTasks(t, s, yesmen.TaskEdit{Title: str("unfinished"), Owner: str("w1"), Status: str("doing")})
	if _, _, err := m.ensure("alice/w1"); err != nil {
		t.Fatal(err)
	}
	m.StopByUser("alice/w1")
	first := reviewR4WorkerRecord(t, s, "w1").PausedAt
	fail := filepath.Join(y.MetaDir(), "workers.json.tmp")
	if err := os.Mkdir(fail, 0700); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	m.beforeNotice = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	if err := m.Prompt(context.Background(), "alice/w1", "new work"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no actual evaluation reached gate")
	}
	m.StopByUser("alice/w1")
	if err := os.Remove(fail); err != nil {
		t.Fatal(err)
	}
	m.continueTasks("alice") // already running: queues another evaluation and returns
	if got := reviewR4WorkerRecord(t, s, "w1").PausedAt; !got.Equal(first) {
		t.Errorf("fixture did not retain the old value before queued evaluation: %v", got)
	}
	close(release)
	reviewR5Drain(t, m)
	got := reviewR4WorkerRecord(t, s, "w1").PausedAt
	if got.IsZero() || got.Equal(first) {
		t.Errorf("actual completed retry did not persist newer Stop: %v", got)
	}
	m.StopAll()
	again := New(m.cfg)
	t.Cleanup(again.StopAll)
	after := promptsTo(again)
	again.continueTasks("alice")
	quiet(t, after, "alice/lead", 0)
	if again.running("alice/lead") != nil {
		t.Fatal("cold restart ignored latest persisted worker Stop")
	}
	t.Log("continueTasks returned while an evaluator ran; after actual chain drain the newer Stop persisted and cold scope remained paused")
}
