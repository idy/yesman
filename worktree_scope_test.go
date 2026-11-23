package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/safefs/linktest"
	"github.com/idy/yesman/internal/yesmen"
)

func TestWorktreeSidebarPrunesOnlyWorkspaceTemp(t *testing.T) {
	s, y, target := deletionFixture(t)
	for _, rel := range []string{"old-root-checkout", "tmp/nested", ".yesman/evidence/tmp/recorded", ".yesman/tmp-old/another"} {
		fixtureExternalGitfile(t, y, rel)
	}
	// The depth limit gives a deterministic reproduction of an incomplete
	// enumeration, independent of I/O speed and machine load.
	dir := fixtureExternalGitfile(t, y, bazelCheckout)
	for range 110 {
		dir = filepath.Join(dir, "d")
	}
	must(t, os.MkdirAll(dir, 0700))
	for i := range 24 {
		must(t, os.MkdirAll(filepath.Join(dir, string(rune('a'+i))), 0700))
	}
	got, err := s.WorkspaceCheckouts(t.Context(), y.Name, y.HiredAt)
	must(t, err)
	want := []string{".yesman/evidence/tmp/recorded", ".yesman/tmp-old/another", "old-root-checkout", "tmp/nested"}
	var paths []string
	for _, entry := range got.Entries {
		paths = append(paths, entry.Path)
	}
	if got.Problem != "" || !slices.Equal(paths, want) {
		t.Fatalf("sidebar must omit only workspace .yesman/tmp: %+v", got)
	}
	plan, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
	must(t, err)
	if plan.Token == "" || plan.Problem != "" || !slices.ContainsFunc(plan.Entries, func(e yesmen.CheckoutEntry) bool { return e.Path == bazelCheckout }) {
		t.Fatalf("deep deletion review missed Bazel checkout: %+v", plan)
	}
}

func TestForceDeleteBazelSymlinkLeavesGitDestination(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "dangling"
		if live {
			name = "outside-git"
		}
		t.Run(name, func(t *testing.T) {
			s, y, target := deletionFixture(t)
			outside := filepath.Join(t.TempDir(), "external")
			initFixtureCheckout(t, outside)
			refs := fixtureGit(t, outside, "show-ref")
			gitHead := mustReadFile(t, filepath.Join(outside, ".git", "HEAD"))
			checkout := filepath.Join(y.Path, filepath.FromSlash(bazelCheckout))
			must(t, os.MkdirAll(checkout, 0700))
			marker := filepath.Join(checkout, ".git")
			referent := filepath.Join(y.Path, "gizos-display-pr", ".git")
			if live {
				referent = filepath.Join(outside, ".git")
			}
			linktest.Symlink(t, referent, marker)
			if err := s.DeleteWorkspace(t.Context(), target); err == nil || !strings.Contains(err.Error(), bazelCheckout) {
				t.Fatalf("ordinary delete must require explicit Force Delete: %v", err)
			}
			plan, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
			must(t, err)
			if plan.Token == "" || plan.Problem != "" || len(plan.Entries) != 1 || plan.Entries[0].Path != bazelCheckout {
				t.Fatalf("Force Delete cannot confirm the local link: %+v", plan)
			}
			must(t, os.Rename(marker, marker+"-held"))
			linktest.Symlink(t, filepath.Join(outside, ".git"), marker)
			if err := s.ForceDeleteWorkspace(t.Context(), target, plan.Token); err == nil {
				t.Fatal("stale confirmation deleted a replacement marker")
			}
			must(t, os.Remove(marker+"-held"))
			plan, err = s.CheckWorkspaceForceDeletion(t.Context(), target)
			must(t, err)
			must(t, s.ForceDeleteWorkspace(t.Context(), target, plan.Token))
			if _, err := os.Lstat(y.Path); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("workspace remains: %v", err)
			}
			if string(mustReadFile(t, filepath.Join(outside, ".git", "HEAD"))) != string(gitHead) || fixtureGit(t, outside, "show-ref") != refs {
				t.Fatal("outside Git metadata changed")
			}
		})
	}
}
