//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// addSpecialSessionFiles adds a named pipe and a link among the sessions,
// newer than any, neither of which may be read or block.
func addSpecialSessionFiles(t *testing.T, dir, target string) {
	t.Helper()
	if err := syscall.Mkfifo(filepath.Join(dir, "d.jsonl"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "e.jsonl")); err != nil {
		t.Fatal(err)
	}
}
