//go:build unix

package yesmen

import (
	"errors"
	"os"
	"path/filepath"
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

// A pipe in yesman.json's place, put there after Store.Get, or between
// checking the file and opening it, is refused at once, never opened for
// reading so that a writer could hold it, and no other yesman waits on it.
func TestHandoffsIdentityPipeIsRefused(t *testing.T) {
	for _, when := range []string{"after Get", "after the check"} {
		t.Run(when, func(t *testing.T) {
			s := handoffStore(t)
			if _, err := s.Hire("bob"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "one"}); err != nil {
				t.Fatal(err)
			}
			y, _ := s.Get("alice")
			meta := filepath.Join(y.MetaDir(), "yesman.json")
			toPipe := func() {
				if err := os.Remove(meta); err != nil {
					t.Error(err)
				}
				if err := syscall.Mkfifo(meta, 0o600); err != nil {
					t.Error(err)
				}
			}
			peer := make(chan error, 1)
			if when == "after Get" {
				toPipe()
			} else {
				handoffFault = func(step, path string) error {
					if step == "meta" {
						toPipe()
						// The Store's metadata lock is free meanwhile.
						go func() { _, err := s.Get("bob"); peer <- err }()
						select {
						case err := <-peer:
							peer <- err
						case <-time.After(5 * time.Second):
							t.Error("another yesman waited on the check")
						}
					}
					return nil
				}
				t.Cleanup(func() { handoffFault = nil })
			}
			done := make(chan error, 1)
			go func() {
				f, _, err := openHandoffs(s.root, y, &s.metaMu)
				if f != nil {
					f.close()
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Error("read the identity from a pipe")
				}
				t.Logf("refused: %v", err)
			case <-time.After(5 * time.Second):
				if w, err := os.OpenFile(meta, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					w.Close()
				}
				<-done
				t.Fatal("waited on the pipe")
			}
			// No reader has it open: a writer cannot connect, so none can
			// hold a read.
			if w, err := os.OpenFile(meta, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				w.Close()
				t.Error("the pipe is open for reading")
			} else if !errors.Is(err, syscall.ENXIO) {
				t.Errorf("opening the pipe to write: %v", err)
			}
			if when == "after Get" {
				go func() { _, err := s.Get("bob"); peer <- err }()
			}
			select {
			case err := <-peer:
				if err != nil {
					t.Errorf("bob: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("bob waited")
			}
		})
	}
}
