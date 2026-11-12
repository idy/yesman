package yesmen

import (
	"path/filepath"
	"testing"
	"time"
)

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
