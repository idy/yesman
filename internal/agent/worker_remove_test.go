package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func TestColdWorkerRemovalKeepsSavedConversation(t *testing.T) {
	for _, surface := range []string{"window", "lead"} {
		t.Run(surface, func(t *testing.T) {
			m, store := newTestManager(t, 0)
			y, err := store.Get("alice")
			if err != nil {
				t.Fatal(err)
			}
			w := yesmen.Worker{Name: "builder", StartedAt: time.Now().UTC().Add(-time.Hour), FinishedAt: time.Now().UTC()}
			if err := store.AddWorker(y.Name, w); err != nil {
				t.Fatal(err)
			}
			dir := y.SessionDir(w.Name)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			saved := map[string]string{"delivery.json": "{\"version\":2,\"floor\":0,\"records\":[]}\n", "old.jsonl": "{\"type\":\"session\",\"id\":\"fixture\"}\n"}
			for name, content := range saved {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(dir, 0500); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(dir, 0700)
			var workerReads, workerWrites atomic.Int32
			m.cfg.deliveryRead = func(path string, now time.Time) (map[string]Delivery, int64, error) {
				if filepath.Dir(path) == dir {
					workerReads.Add(1)
				}
				return readDeliveries(path, now)
			}
			m.cfg.deliveryWrite = func(path string, data []byte) error {
				if filepath.Dir(path) == dir {
					workerWrites.Add(1)
				}
				return writeDeliveryFile(path, data)
			}
			worker, err := m.Get(ID(y.Name, w.Name))
			if err != nil || worker.FinishedAt.IsZero() {
				t.Fatal(worker, err)
			}
			if surface == "window" {
				err = m.RemoveWorkerOf(y.Name, w.Name, y.HiredAt, w.StartedAt)
			} else {
				lead, p, errStart := m.ensure(ID(y.Name, yesmen.LeadName))
				if errStart != nil {
					t.Fatal(errStart)
				}
				m.mu.Lock()
				info, token := lead.info, lead.token
				m.mu.Unlock()
				err = m.RemoveWorkerBy(t.Context(), token, info, y, worker)
				if m.running(info.ID) != p {
					t.Fatal("lead stopped")
				}
			}
			if err != nil || workerReads.Load() != 0 || workerWrites.Load() != 0 {
				t.Fatalf("remove: %v, receipt reads=%d writes=%d", err, workerReads.Load(), workerWrites.Load())
			}
			if workers, err := store.Workers(y.Name); err != nil || len(workers) != 0 {
				t.Fatal(workers, err)
			}
			for name, content := range saved {
				if data, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(data) != content {
					t.Fatalf("%s changed: %q %v", name, data, err)
				}
			}
		})
	}
}

func TestWorkerRemovalJoinsOpeningConversation(t *testing.T) {
	m, store := newTestManager(t, 0)
	w := yesmen.Worker{Name: "builder", StartedAt: time.Now().UTC()}
	if err := store.AddWorker("alice", w); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	defer finish()
	m.cfg.deliveryRead = func(path string, now time.Time) (map[string]Delivery, int64, error) {
		close(started)
		<-release
		return readDeliveries(path, now)
	}
	id := ID("alice", w.Name)
	open, removed := make(chan error, 1), make(chan error, 1)
	go func() { _, err := m.Messages(ctx, id); open <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { removed <- m.RemoveWorker("alice", w.Name) }()
	var a *agent
	for {
		m.mu.Lock()
		a = m.agents[id]
		reserved := a != nil && a.removing
		m.mu.Unlock()
		if reserved {
			break
		}
		select {
		case err := <-removed:
			t.Fatalf("removal passed an opening conversation: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	finish()
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	if err := <-open; !errors.Is(err, ErrRetired) && !errors.Is(err, errStopped) {
		t.Fatal("superseded open", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agents[id] != nil || a.deliveryWriter == nil || !a.deliveryWriter.finished() {
		t.Fatal("removed worker still has a live writer or process reservation")
	}
}
