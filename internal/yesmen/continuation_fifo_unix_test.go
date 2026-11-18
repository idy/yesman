//go:build unix

package yesmen

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestR2ContinuationFIFOIsolation(t *testing.T) {
	s := waitStore(t)
	if _, err := s.Hire("bob"); err != nil {
		t.Fatal(err)
	}
	y, _ := s.Get("alice")
	if err := syscall.Mkfifo(y.ContinuationFile(), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Continuation("alice"); done <- err }()
	// A writer can open only once the real reader has reached the FIFO.
	var fd int
	var err error
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		fd, err = syscall.Open(y.ContinuationFile(), syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		select {
		case <-done:
			return
		default:
			t.Fatal(err)
		}
	}
	bob := make(chan error, 1)
	go func() { _, err := s.Continuation("bob"); bob <- err }()
	blocked := false
	select {
	case err := <-bob:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(150 * time.Millisecond):
		blocked = true
	}
	// Clean up the synthetic FIFO by writing a complete record and EOF.
	_, _ = syscall.Write(fd, []byte("{}"))
	_ = syscall.Close(fd)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("FIFO reader cleanup")
	}
	if blocked {
		select {
		case <-bob:
		case <-time.After(time.Second):
			t.Fatal("Bob cleanup")
		}
	}
	_ = os.Remove(y.ContinuationFile())
	if blocked {
		t.Fatal("Alice continuation FIFO blocks Bob under global continuationMu; reader should reject nonregular file without waiting")
	}
}
