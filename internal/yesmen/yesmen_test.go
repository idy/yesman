package yesmen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkersConcurrentReadersAndUpdates(t *testing.T) {
	base := t.TempDir()
	s := New(filepath.Join(base, "yesmen"), filepath.Join(base, "archive"))
	if _, err := s.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	failures := make(chan error, 2)
	var readers sync.WaitGroup
	for range 2 {
		readers.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
				}
				if _, err := s.Workers("alice"); err != nil {
					failures <- err
					return
				}
			}
		})
	}
	defer func() {
		close(done)
		readers.Wait()
		close(failures)
		for err := range failures {
			t.Errorf("read worker metadata: %v", err)
		}
	}()
	for range 100 {
		if err := s.AddWorker("alice", Worker{Name: "w1", Task: "task", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveWorker("alice", "w1"); err != nil {
			t.Fatal(err)
		}
	}
	if workers, err := s.Workers("alice"); err != nil || len(workers) != 0 {
		t.Fatalf("workers after updates: %+v, %v", workers, err)
	}
}

func TestArchiveWaitsForUse(t *testing.T) {
	tmp := t.TempDir()
	s := New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	if _, err := s.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	inUse, release := make(chan struct{}), make(chan struct{})
	used := make(chan error, 1)
	go func() {
		used <- s.Use("alice", func(Yesman) error {
			close(inUse)
			<-release
			return nil
		})
	}()
	<-inUse
	archived := make(chan error, 1)
	go func() {
		_, err := s.Archive("alice", nil)
		archived <- err
	}()
	select {
	case err := <-archived:
		t.Fatalf("Archive finished while the workspace was in use: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-used; err != nil {
		t.Fatal(err)
	}
	if err := <-archived; err != nil {
		t.Fatal(err)
	}
	if err := s.Use("alice", func(Yesman) error { return nil }); err == nil {
		t.Error("Use succeeded on an archived yesman")
	}
	if _, err := s.Hire("alice"); err != nil {
		t.Errorf("hiring the name again: %v", err)
	}
}

// Every start of a worker is kept, removed workers' included, so that a
// name started again does not merge what its starts did.
func TestWorkerStartsOutliveRemoval(t *testing.T) {
	tmp := t.TempDir()
	s := New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	y, err := s.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	// A worker from before starts were recorded is recorded when the list
	// next changes.
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if err := writeJSON(filepath.Join(y.MetaDir(), "workers.json"), []Worker{{Name: "old", Task: "x", StartedAt: t0}}); err != nil {
		t.Fatal(err)
	}
	if starts, err := s.WorkerStarts("alice"); err != nil || len(starts) != 0 {
		t.Fatalf("starts before any change = %+v, %v", starts, err)
	}
	for i, step := range []func() error{
		func() error { return s.AddWorker("alice", Worker{Name: "w", Task: "x", StartedAt: t0.Add(time.Hour)}) },
		func() error { return s.RemoveWorker("alice", "w") },
		func() error {
			return s.AddWorker("alice", Worker{Name: "w", Task: "x", StartedAt: t0.Add(2 * time.Hour)})
		},
		func() error { return s.RemoveWorker("alice", "w") },
		func() error {
			return s.AddWorker("alice", Worker{Name: "w", Task: "x", StartedAt: t0.Add(3 * time.Hour)})
		},
		func() error { return s.RemoveWorker("alice", "old") },
	} {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	starts, err := s.WorkerStarts("alice")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range starts {
		got = append(got, fmt.Sprintf("%s@%d", x.Name, x.StartedAt.Sub(t0)/time.Hour))
	}
	if strings.Join(got, " ") != "old@0 w@1 w@2 w@3" {
		t.Fatalf("starts = %v", got)
	}

	// An unreadable file is left alone, and workers still start.
	if err := os.WriteFile(y.startsFile(), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWorker("alice", Worker{Name: "next", Task: "x", StartedAt: t0.Add(4 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(y.startsFile()); string(data) != "{broken" {
		t.Fatalf("unreadable starts replaced: %q", data)
	}
	if _, err := s.WorkerStarts("alice"); err == nil {
		t.Fatal("unreadable starts read without an error")
	}
}
