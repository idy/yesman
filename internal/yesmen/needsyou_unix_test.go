//go:build unix

package yesmen

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// A pipe in the file's place is refused at once: reading never waits on it.
func TestHandoffsPipeIsRefused(t *testing.T) {
	s := handoffStore(t)
	y, _ := s.Get("alice")
	if err := syscall.Mkfifo(y.HandoffsFile(), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Handoffs("alice"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("read a pipe")
		}
	case <-time.After(5 * time.Second):
		// Open the pipe so the blocked read ends, then fail.
		f, _ := os.OpenFile(y.HandoffsFile(), os.O_WRONLY, 0)
		f.Close()
		<-done
		t.Fatal("reading waited on a pipe")
	}
	if fi, err := os.Lstat(y.HandoffsFile()); err != nil || fi.Mode().Type() != os.ModeNamedPipe {
		t.Errorf("the pipe changed: %v", err)
	}
}
