package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

func writeSized(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", n)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMeasureWorkspace(t *testing.T) {
	root := t.TempDir()
	writeSized(t, filepath.Join(root, "app--work", "main.go"), 100)
	writeSized(t, filepath.Join(root, "app--work", "nested--wt", "x.go"), 7)
	writeSized(t, filepath.Join(root, ".yesman", "knowledge", "INDEX.md"), 10)
	writeSized(t, filepath.Join(root, ".yesman", "sessions", "lead", "s.jsonl"), 50)
	writeSized(t, filepath.Join(root, ".yesman", "yesman.json"), 3)
	writeSized(t, filepath.Join(root, "scratch.txt"), 4)
	wts := []pool.Worktree{{Dir: "app--work"}, {Dir: filepath.Join("app--work", "nested--wt")}, {Dir: "empty--wt"}}

	u, err := measureWorkspace(context.Background(), root, wts)
	if err != nil {
		t.Fatal(err)
	}
	if u.Bytes != 174 || u.Files != 6 || u.Partial {
		t.Errorf("total %d bytes in %d files, partial %v", u.Bytes, u.Files, u.Partial)
	}
	got := map[string]int64{}
	for _, p := range u.Parts {
		got[p.Kind+":"+p.Path] = p.Bytes
	}
	want := map[string]int64{
		"worktree:app--work": 100, "worktree:app--work/nested--wt": 7, "worktree:empty--wt": 0,
		"knowledge:.yesman/knowledge": 10, "sessions:.yesman/sessions": 50, "yesman:.yesman": 3, "other:.": 4,
	}
	if len(got) != len(want) {
		t.Errorf("parts %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d (parts %v)", k, got[k], v, got)
		}
	}
	if u.Parts[0].Path != "app--work" {
		t.Errorf("largest part first: %+v", u.Parts)
	}
}

func TestKnowledgeInfo(t *testing.T) {
	y := yesmen.Yesman{Path: t.TempDir()}
	if k := knowledgeInfo(y); k.Notes != 0 || k.Index {
		t.Errorf("no knowledge = %+v", k)
	}
	writeSized(t, filepath.Join(y.KnowledgeDir(), "INDEX.md"), 1)
	writeSized(t, filepath.Join(y.KnowledgeDir(), "a.md"), 1)
	writeSized(t, filepath.Join(y.KnowledgeDir(), "deep", "b.MD"), 1)
	writeSized(t, filepath.Join(y.KnowledgeDir(), "c.png"), 1)
	if k := knowledgeInfo(y); k.Notes != 2 || !k.Index || k.UpdatedAt.IsZero() {
		t.Errorf("knowledge = %+v", k)
	}
}
