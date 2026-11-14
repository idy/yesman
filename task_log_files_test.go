package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/tasklog"
)

func TestTaskLogFilesReadOnlyAndLinkedPaths(t *testing.T) {
	s, ws := testFiles(t)
	part := ".yesman/task-log/2026-01-15-t3-detail.md"
	const text = "# Detail\n[Contents](../task-log.md)\n"
	if err := os.WriteFile(filepath.Join(ws, part), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{tasklog.TOC, part} {
		v, e := s.ReadFile("alice", path)
		if e != nil || v.Kind != "text" || v.Text == "" {
			t.Fatalf("read %s: %v", path, e)
		}
		if _, e = s.WriteFile("alice", path, "overwrite", v.Hash); e == nil {
			t.Fatalf("history writable: %s", path)
		}
	}
	custom := ".yesman/task-log/资料/旧 记录.md"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(ws, custom)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, custom), []byte("# Existing custom history\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := s.ReadFile("alice", custom); err != nil || v.Kind != "text" {
		t.Fatalf("custom Markdown read: %v", err)
	}
	if _, err := s.WriteFile("alice", custom, "overwrite", ""); err == nil {
		t.Fatal("custom history was editable")
	}
	large := ".yesman/task-log/large.md"
	if err := os.WriteFile(filepath.Join(ws, large), []byte(strings.Repeat("bounded history line\n", 300000)), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := s.ReadFile("alice", large)
	if err != nil || !v.Truncated || len(v.Text) > textPreviewHead || v.Hash != "" {
		t.Fatalf("large record preview: truncated=%v bytes=%d err=%v", v.Truncated, len(v.Text), err)
	}
	for _, path := range []string{"notes.md", ".yesman/task-log.md.bak"} {
		if _, e := s.WriteFile("alice", path, "ordinary edit", ""); e != nil {
			t.Fatalf("ordinary editing blocked: %v", e)
		}
	}
	if string(mustReadFile(t, filepath.Join(ws, part))) != text {
		t.Fatal("part changed")
	}
	target := filepath.Join(ws, "shop", "README.md")
	for _, kind := range []string{"symlink", "dangling", "hardlink", "directory"} {
		path := ".yesman/task-log/" + kind + ".md"
		abs := filepath.Join(ws, path)
		var e error
		switch kind {
		case "symlink":
			e = os.Symlink(target, abs)
		case "dangling":
			e = os.Symlink(filepath.Join(ws, "shop", "absent.go"), abs)
		case "hardlink":
			e = os.Link(target, abs)
		case "directory":
			e = os.Mkdir(abs, 0o755)
		}
		if e != nil {
			t.Skip(e)
		}
		if _, e = s.ReadFile("alice", path); e == nil {
			t.Fatalf("read unsafe record %s", kind)
		}
	}
	if !strings.HasPrefix(string(mustReadFile(t, target)), "# Shop") {
		t.Fatal("other workspace file changed")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
