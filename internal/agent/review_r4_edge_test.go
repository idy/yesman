package agent

import (
	"encoding/json"
	"github.com/idy/yesman/internal/yesmen"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func reviewR4WorkerRecord(t *testing.T, s *yesmen.Store, name string) yesmen.Worker {
	t.Helper()
	ws, err := s.Workers("alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.Name == name {
			return w
		}
	}
	t.Fatal("missing worker")
	return yesmen.Worker{}
}

func TestReviewR4HireTimeOrderIsNotIncarnationOrder(t *testing.T) {
	m, s := newTestManager(t, 0)
	y, _ := s.Get("alice")
	// Synthetic metadata models a prior hire made while the system clock was ahead.
	// No OS clock is changed. HiredAt is persisted identity, not a monotonic clock.
	path := filepath.Join(y.MetaDir(), "yesman.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	stamp, _ := json.Marshal(time.Now().UTC().Add(time.Hour))
	obj["hiredAt"] = stamp
	data, _ = json.Marshal(obj)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	old, _ := s.Get("alice")
	m.StopByUser("alice/lead")
	if _, err := s.Archive("alice", func(y yesmen.Yesman) error { return m.StopYesman(y.Name) }); err != nil {
		t.Fatal(err)
	}
	m.ForgetYesman("alice", old.HiredAt)
	now, err := s.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	if !now.HiredAt.Before(old.HiredAt) {
		t.Fatal("fixture did not establish reversed wall-clock order")
	}
	addTasks(t, s, yesmen.TaskEdit{Title: str("New current goal")})
	m.StopByUser("alice/lead")
	c, err := s.Continuation("alice")
	if err != nil {
		t.Fatal(err)
	}
	if c.LeadPausedAt.IsZero() {
		t.Error("current hire Stop silently discarded because its timestamp precedes archived hire")
	}
	m.StopAll()
	again := New(m.cfg)
	t.Cleanup(again.StopAll)
	again.continueTasks("alice")
	if again.running("alice/lead") != nil {
		t.Fatal("restart wakes current hire despite completed user Stop")
	}
}

func TestReviewR4CloseRPCFailureLeavesWorkerOpen(t *testing.T) {
	m, s := newTestManager(t, 0)
	if err := s.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	a, p, err := m.ensure("alice/w1")
	if err != nil {
		t.Fatal(err)
	}
	// Actual RPC pipe close forces modelState to fail. The process remains this a's
	// until watcher records exit; no manager/run fields are fabricated.
	m.mu.Lock()
	mark := runMark{p: p, inbox: a.inbox, run: a.settled}
	m.mu.Unlock()
	p.stdin.Close()
	m.closeIfFinished(a, mark, time.Now().UTC())
	if w := reviewR4WorkerRecord(t, s, "w1"); !w.FinishedAt.IsZero() {
		t.Fatal("failed close get_state recorded success")
	}
}
