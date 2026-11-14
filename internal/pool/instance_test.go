package pool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeInstancePrivateMarker(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "origin")
	newOrigin(t, origin)
	p := New(filepath.Join(tmp, "repos"))
	r, err := p.Add(context.Background(), origin, "")
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(tmp, "workspace")
	wt, err := p.AddWorktree(context.Background(), r, WorktreeOptions{Dest: filepath.Join(workspace, "review"), Ref: "origin/feature"})
	if err != nil {
		t.Fatal(err)
	}
	if !cleanupIDRE.MatchString(wt.CleanupID) {
		t.Fatal("missing actual Git generation", wt.CleanupID)
	}
	private, err := privateWorktreeDir(r.Path, wt.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(private, worktreeInstanceFile)); err != nil || string(got) != wt.CleanupID {
		t.Fatalf("marker=%s err=%v", got, err)
	}
	if got := run(t, wt.Path, "status", "--porcelain", "--untracked-files=all", "--ignored"); got != "" {
		t.Fatalf("private nonce dirtied checkout: %s", got)
	}
	if err := p.RemoveWorktree(context.Background(), wt.Path, false); err != nil {
		t.Fatal(err)
	}
	replacement, err := p.AddWorktree(context.Background(), r, WorktreeOptions{Dest: wt.Path, Ref: wt.Head})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.CleanupID == "" || replacement.CleanupID == wt.CleanupID {
		t.Fatal("same path/HEAD reused old generation")
	}
	if err := p.RemoveTemporaryWorktree(context.Background(), workspace, wt); err == nil {
		t.Fatal("stale generation removed a replacement")
	}
	if _, err := os.Stat(replacement.Path); err != nil {
		t.Fatal("replacement lost", err)
	}
	private, err = privateWorktreeDir(r.Path, replacement.Path)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(private, worktreeInstanceFile)
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveTemporaryWorktree(context.Background(), workspace, replacement); err == nil {
		t.Fatal("legacy/no marker accepted")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("backfilled unknown instance marker")
	}
	if err := os.WriteFile(marker, []byte(strings.Repeat("0", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveTemporaryWorktree(context.Background(), workspace, replacement); err == nil {
		t.Fatal("mismatched marker accepted")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != strings.Repeat("0", 32) {
		t.Fatal("repaired unknown marker", err)
	}
}
