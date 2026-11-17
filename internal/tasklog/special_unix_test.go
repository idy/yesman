//go:build !windows

package tasklog

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSpecialHistoryFileIsRefusedBeforeOpen(t *testing.T) {
	ws := fixture(t, []byte("original"))
	if err := os.Remove(filepath.Join(ws, TOC)); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(ws, TOC), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ws, Options{Action: "dry-run"}); err == nil {
		t.Fatal("FIFO accepted as history")
	}
}
