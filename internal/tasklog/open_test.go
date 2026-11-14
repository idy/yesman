package tasklog

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestReadUsesVerifiedDescriptor(t *testing.T) {
	ws := fixture(t, []byte("safe original"))
	external := filepath.Join(t.TempDir(), "external.md")
	if err := os.WriteFile(external, []byte("outside synthetic sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := Open(root, TOC)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	original := filepath.Join(ws, TOC)
	if err = os.Rename(original, original+".saved"); err != nil {
		t.Logf("platform prevented open-file replacement: %v", err)
		return
	}
	if err = os.Link(external, original); err != nil {
		t.Fatal(err)
	}
	got, err := readOpened(f, maxSource)
	if err == nil || bytes.Equal(got, []byte("outside synthetic sentinel")) {
		t.Fatal("consumed an unchecked replacement")
	}
}

func TestReadRejectsParentReplacement(t *testing.T) {
	ws := fixture(t, []byte("source"))
	dir := filepath.Join(ws, Dir, "nested")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "part.md"), []byte("safe original"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := Open(root, Dir+"/nested/part.md")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = os.Rename(dir, dir+"-saved"); err != nil {
		t.Logf("platform prevented parent replacement: %v", err)
		return
	}
	// An ordinary replacement directory also has a different identity.
	if err = os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "part.md"), []byte("replacement sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = readOpened(f, maxSource); err == nil {
		t.Fatal("changed parent generation accepted")
	}
}
