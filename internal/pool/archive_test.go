package pool

import (
	"os"
	"path/filepath"
	"testing"
)

// A directory can appear after preview (even in the last instant before
// rename). Git and ordinary POSIX rename can consume an empty directory.
func TestArchiveRenameNeverReplacesAnEmptyDirectory(t *testing.T) {
	base := t.TempDir()
	src, dst := filepath.Join(base, "from"), filepath.Join(base, "restore")
	for _, p := range []string{src, dst} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	old, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if err := moveNoReplace(src, dst); err == nil {
		t.Fatal("replaced the destination")
	}
	got, err := os.Stat(dst)
	if err != nil || !os.SameFile(old, got) {
		t.Fatal("destination changed", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("source lost", err)
	}
}
