package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/yesmen"
)

func testFiles(t *testing.T) (*service, string) {
	t.Helper()
	tmp := t.TempDir()
	store := yesmen.New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	y, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		p := filepath.Join(y.Path, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("shop/README.md", "# Shop\n")
	write("shop/.git", "gitdir: elsewhere\n")
	write("shop/cmd/main.go", "package main\n")
	write("shop/Zeta.txt", "z\n")
	write("shop/alpha.txt", "a\n")
	write("shop/logo.png", "\x89PNG\r\n\x1a\n")
	write("shop/blob.bin", "a\x00b")
	write("big.log", strings.Repeat("line of text\n", maxTextPreview/13+10))
	write(".yesman/sessions/lead/run.jsonl", "{}\n")
	if err := os.WriteFile(filepath.Join(tmp, "secret"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tmp, "secret"), filepath.Join(y.Path, "shop", "escape")); err != nil {
		t.Skipf("these tests need a symbolic link out of the workspace: %v", err) // Windows without the privilege
	}
	return &service{app: &app{yesmen: store}}, y.Path
}

func TestListFiles(t *testing.T) {
	s, _ := testFiles(t)
	entries, err := s.ListFiles("alice", "shop")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
		if e.Path != "shop/"+e.Name {
			t.Errorf("path of %s = %s", e.Name, e.Path)
		}
	}
	// Directories first, then files regardless of case; no .git.
	want := []string{"cmd", "alpha.txt", "blob.bin", "escape", "logo.png", "README.md", "Zeta.txt"}
	if !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	// Paths that climb out stop at the workspace's root.
	for _, bad := range []string{"..", "../..", "shop/../.."} {
		if entries, err := s.ListFiles("alice", bad); err != nil || len(entries) == 0 || entries[0].Name != ".yesman" {
			t.Errorf("ListFiles(%q) = %v, %v", bad, entries, err)
		}
	}
	if _, err := s.ListFiles("alice", "/etc"); err == nil {
		t.Error("/etc was listed: absolute paths must be inside the workspace")
	}
}

func TestReadFile(t *testing.T) {
	s, _ := testFiles(t)
	if v, err := s.ReadFile("alice", "shop/README.md"); err != nil || v.Kind != "text" || v.Text != "# Shop\n" {
		t.Errorf("README = %+v, %v", v, err)
	}
	if v, err := s.ReadFile("alice", "shop/logo.png"); err != nil || v.Kind != "image" || !strings.HasPrefix(v.DataURL, "data:image/png;base64,") {
		t.Errorf("logo = %+v, %v", v, err)
	}
	if v, err := s.ReadFile("alice", "shop/blob.bin"); err != nil || v.Kind != "binary" || v.Text != "" {
		t.Errorf("blob = %+v, %v", v, err)
	}
	v, err := s.ReadFile("alice", "big.log")
	if err != nil || v.Kind != "text" || !v.Truncated || len(v.Text) > textPreviewHead || !strings.HasSuffix(v.Text, "\n") {
		t.Errorf("big.log: kind %s, truncated %v, %d bytes, %v", v.Kind, v.Truncated, len(v.Text), err)
	}
	if _, err := s.ReadFile("alice", "shop/escape"); err == nil {
		t.Error("a link out of the workspace was read")
	}
	if _, err := s.ReadFile("alice", "shop"); err == nil {
		t.Error("a directory was read as a file")
	}
}

func TestFindFiles(t *testing.T) {
	s, _ := testFiles(t)
	got, err := s.FindFiles(context.Background(), "alice", "main", "")
	if err != nil || len(got) == 0 || got[0] != "shop/cmd/main.go" {
		t.Errorf("main = %v, %v", got, err)
	}
	// Names first, then paths, then characters in order; never sessions.
	got, _ = s.FindFiles(context.Background(), "alice", "md", "")
	if len(got) < 2 || got[0] != "shop/README.md" || !slices.Contains(got, "shop/cmd/main.go") {
		t.Errorf("md = %v", got)
	}
	if got, _ := s.FindFiles(context.Background(), "alice", "run.jsonl", ""); len(got) != 0 {
		t.Errorf("session logs were found: %v", got)
	}
	// Within a folder, its own name matches nothing.
	if got, _ := s.FindFiles(context.Background(), "alice", "main", "shop/cmd"); len(got) != 1 || got[0] != "shop/cmd/main.go" {
		t.Errorf("main under shop/cmd = %v", got)
	}
	if got, _ := s.FindFiles(context.Background(), "alice", "shop", "shop"); len(got) != 0 {
		t.Errorf("shop under shop = %v", got)
	}
}

func TestWriteFile(t *testing.T) {
	s, ws := testFiles(t)
	v, err := s.ReadFile("alice", "shop/README.md")
	if err != nil || v.Hash == "" {
		t.Fatalf("read = %+v, %v", v, err)
	}
	if err := os.Chmod(filepath.Join(ws, "shop", "README.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved, err := s.WriteFile("alice", "shop/README.md", "# Shop\n\nEdited.\n", v.Hash)
	if err != nil || saved.Hash == v.Hash || saved.Text != "# Shop\n\nEdited.\n" {
		t.Fatalf("save = %+v, %v", saved, err)
	}
	// Windows keeps only a read-only flag, not Unix permission bits.
	st, _ := os.Stat(filepath.Join(ws, "shop", "README.md"))
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("permissions became %v", st.Mode().Perm())
	}
	if entries, _ := os.ReadDir(filepath.Join(ws, "shop")); len(entries) != 8 {
		t.Errorf("saving left files behind: %d entries", len(entries))
	}

	// An agent changes it: a save from the old read is refused.
	if err := os.WriteFile(filepath.Join(ws, "shop", "README.md"), []byte("agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteFile("alice", "shop/README.md", "mine\n", saved.Hash); !errors.Is(err, errChanged) {
		t.Errorf("a stale save = %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(ws, "shop", "README.md")); string(data) != "agent\n" {
		t.Errorf("the stale save wrote %q", data)
	}
	// Overwriting on purpose, and saving a file that is gone, write it.
	if _, err := s.WriteFile("alice", "shop/README.md", "mine\n", ""); err != nil {
		t.Error(err)
	}
	if _, err := s.WriteFile("alice", "shop/new/notes.txt", "x", ""); err == nil {
		t.Error("a file was written into a directory that does not exist")
	}
	if _, err := s.WriteFile("alice", "shop/fresh.txt", "x", "deadbeef"); !errors.Is(err, errChanged) {
		t.Errorf("a save of a vanished file = %v", err)
	}
	// It stays in the workspace.
	if _, err := s.WriteFile("alice", "shop/escape", "x", ""); err == nil {
		t.Error("a link out of the workspace was written through")
	}
	if data, _ := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(ws)), "secret")); string(data) != "outside" {
		t.Errorf("the file outside became %q", data)
	}
}
