//go:build !windows

package server

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// An unrelated checkout whose .git is a FIFO with no writer does not hold
// up a write elsewhere, and a write into that checkout is refused without
// waiting for one.
func TestManagedFilesDoNotWaitOnACheckoutsFIFO(t *testing.T) {
	x := newWorktreeFixture(t)
	x.checkout(t, "mine", "feature")
	x.checkout(t, "other", "other")
	other := filepath.Join(x.y.Path, "other")
	if err := os.Remove(filepath.Join(other, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(other, ".git"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Should a reader wait on the FIFO, this unblocks it so the test ends.
	defer func() {
		if f, err := os.OpenFile(filepath.Join(other, ".git"), os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
	}()
	scratch, _, _ := x.f.m.ProcessTemp(x.w.Token)
	done := make(chan [3]int, 1)
	go func() {
		done <- [3]int{
			x.write(filepath.Join(x.y.Path, filepath.FromSlash(scratch.Rel), "a.txt"), "scratch"),
			x.write(filepath.Join(x.y.Path, "mine", "feature"), "mine\n"),
			x.write(filepath.Join(other, "feature"), "x"),
		}
	}()
	select {
	case codes := <-done:
		if codes[0] != 200 || codes[1] != 200 || codes[2] == 200 {
			t.Fatalf("scratch %d, own checkout %d, FIFO checkout %d", codes[0], codes[1], codes[2])
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a managed write waited on a checkout's FIFO")
	}
}
