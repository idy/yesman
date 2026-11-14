package tasklog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryReadSetSupportsExistingCustomMarkdown(t *testing.T) {
	ws := fixture(t, []byte("# Existing TOC\n"))
	rel := Dir + "/资料/旧 记录.MD"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(ws, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, rel), []byte("# Existing custom Markdown\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !IsPath(rel) {
		t.Fatal("existing Unicode/space/nested Markdown excluded")
	}
	root, err := os.OpenRoot(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = Check(root, rel, false); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{Dir + "/file.md.bak", Dir + "/../outside.md", Dir + "//file.md", Dir + "/file.md/../../outside.md", Dir + "-neighbor/file.md", "notes.md", TOC + "/child.md"} {
		if IsPath(path) {
			t.Fatalf("non-history path accepted: %s", path)
		}
	}
}
