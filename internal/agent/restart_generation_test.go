package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRestartCompositionRetiredReportAtPipe(t *testing.T) {
	store, tmp, _ := restartStore(t)
	w := addWorker(t, store, "worker")
	m, got := restartInstance(t, store, tmp)
	_, lead, err := m.ensure("alice/lead")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once, unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })
	lead.reviewBeforeWrite = func(v any) {
		if cmd, ok := v.(map[string]any); ok && cmd["type"] == "prompt" {
			once.Do(func() { close(entered); <-release })
		}
	}
	a, p, err := m.ensure("alice/worker")
	if err != nil {
		t.Fatal(err)
	}
	m.record(a, p, "agent_start", runStart)
	m.record(a, p, "agent_settled", runSettled)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("report not at pipe")
	}
	if err := m.RemoveWorker("alice", "worker"); err != nil {
		t.Fatal(err)
	}
	w.StartedAt = w.StartedAt.Add(time.Second)
	if err := store.AddWorker("alice", w); err != nil {
		t.Fatal(err)
	}
	y, _ := store.Get("alice")
	path := filepath.Join(y.Path, ".yesman", "workers.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unblock.Do(func() { close(release) })
	eventually(t, "retired report drained", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return a.reports == 0 })
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) || len(got.reports("alice/lead", "worker")) != 0 {
		t.Fatal("retired report crossed replacement")
	}
}
