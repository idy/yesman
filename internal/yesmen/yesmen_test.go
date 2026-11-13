package yesmen

import (
	"path/filepath"
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
