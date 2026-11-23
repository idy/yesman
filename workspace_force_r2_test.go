package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWorkspaceForceKeepsDependentGitWorktree(t *testing.T) {
	for _, mode := range []string{"external", "unresolved", "local", "external-core-worktree"} {
		t.Run(mode, func(t *testing.T) {
			s, y, target := deletionFixture(t)
			repo := fixtureLocalCheckout(t, y, bazelCheckout)
			other, err := s.Hire(t.Context(), "other")
			must(t, err)
			local := filepath.Join(y.MetaDir(), "tmp", "linked")
			outside := filepath.Join(other.Path, "linked")
			if mode != "external-core-worktree" {
				fixtureGit(t, repo, "worktree", "add", "-b", "linked", local)
			}
			head := fixtureGit(t, repo, "rev-parse", "HEAD")
			move := func() { fixtureGit(t, repo, "worktree", "move", local, outside) }
			switch mode {
			case "external":
				move()
			case "unresolved":
				must(t, os.Rename(filepath.Join(local, ".git"), filepath.Join(local, "keep-gitfile")))
			case "external-core-worktree":
				must(t, os.Mkdir(outside, 0700))
				fixtureGit(t, repo, "config", "core.worktree", outside)
			}
			plan, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
			must(t, err)
			if plan.Token != "" || !strings.Contains(plan.Problem, filepath.Join(repo, ".git")) {
				t.Fatalf("missing hold for local Git storage: %+v", plan)
			}
			if mode == "external" {
				if got := fixtureGit(t, outside, "rev-parse", "HEAD"); got != head {
					t.Fatal(got)
				}
			}
			mustReadFile(t, filepath.Join(repo, ".git", "HEAD"))
		})
	}
}

func TestWorkspaceForceKeepsExternalCommonDirectory(t *testing.T) {
	s, y, target := deletionFixture(t)
	external := filepath.Join(t.TempDir(), "external-repo")
	initFixtureCheckout(t, external)
	local := filepath.Join(y.MetaDir(), "tmp", "local-linked")
	must(t, os.MkdirAll(filepath.Dir(local), 0700))
	fixtureGit(t, external, "worktree", "add", "-b", "linked-local", local)
	gitDir := fixtureGit(t, local, "rev-parse", "--absolute-git-dir")
	back := mustReadFile(t, filepath.Join(gitDir, "gitdir"))
	refs := fixtureGit(t, external, "show-ref")
	plan, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
	must(t, err)
	if plan.Token == "" || len(plan.Entries) != 1 {
		t.Fatal(plan.CheckoutScan)
	}
	must(t, s.ForceDeleteWorkspace(t.Context(), target, plan.Token))
	if _, err := os.Lstat(local); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if fixtureGit(t, external, "show-ref") != refs || string(mustReadFile(t, filepath.Join(gitDir, "gitdir"))) != string(back) {
		t.Fatal("external Git state changed")
	}
}

const deepBuildDirectory = bazelCheckout + "/build/"

func deepBuildPath() string { return deepBuildDirectory + strings.Repeat("d/", 105) + "out" }

func TestWorkspaceForceReviewsDeepBuildAndRoot(t *testing.T) {
	for _, scope := range []string{"deep", "root"} {
		t.Run(scope, func(t *testing.T) {
			s, y, target := deletionFixture(t)
			deep := deepBuildPath()
			deepGitfile := filepath.Join(y.Path, filepath.FromSlash(deep), ".git")
			var deepPointer []byte
			if scope == "deep" {
				checkout := fixtureExternalGitfile(t, y, bazelCheckout)
				gitDir := fixtureGit(t, checkout, "rev-parse", "--absolute-git-dir")
				deepPointer = []byte("gitdir: " + gitDir + "\n")
				must(t, os.MkdirAll(filepath.Dir(deepGitfile), 0700))
				sidebar, err := s.WorkspaceCheckouts(t.Context(), y.Name, y.HiredAt)
				must(t, err)
				if sidebar.Problem != "" || len(sidebar.Entries) != 0 {
					t.Fatal(sidebar)
				}
			} else {
				fixtureExternalGitfile(t, y, ".")
				must(t, os.Mkdir(filepath.Join(y.Path, "src"), 0700))
				must(t, os.WriteFile(filepath.Join(y.Path, "src", "main.txt"), []byte("source"), 0600))
				fixtureGit(t, y.Path, "add", "src")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if got, err := s.CheckWorkspaceForceDeletion(ctx, target); err != nil || got.Token != "" || got.Problem == "" {
				t.Fatal(got, err)
			}
			plan, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
			must(t, err)
			if plan.Token == "" || plan.Problem != "" {
				t.Fatal(plan.CheckoutScan)
			}
			if scope == "deep" {
				// Place the late gitfile at the bottom of the existing tree.
				// Git for Windows cannot chdir to this depth during fixture setup.
				must(t, os.WriteFile(deepGitfile, deepPointer, 0600))
			} else {
				if !slices.Contains(plan.Paths, "src") || plan.Entries[0].Path != "." {
					t.Fatal(plan.CheckoutScan)
				}
				src := filepath.Join(y.Path, "src")
				must(t, os.Rename(src, filepath.Join(t.TempDir(), "original-src")))
				must(t, os.Mkdir(src, 0700))
				must(t, os.WriteFile(filepath.Join(src, "fresh.txt"), []byte("fresh"), 0600))
			}
			if err := s.ForceDeleteWorkspace(t.Context(), target, plan.Token); err == nil || !strings.Contains(err.Error(), "paths changed") {
				t.Fatalf("expected stale preview refusal, got %v", err)
			}
			if scope == "deep" {
				if got := mustReadFile(t, deepGitfile); string(got) != string(deepPointer) {
					t.Fatalf("stale confirmation changed the late gitfile: %q", got)
				}
			} else {
				mustReadFile(t, filepath.Join(y.Path, "src", "fresh.txt"))
			}
			plan, err = s.CheckWorkspaceForceDeletion(t.Context(), target)
			must(t, err)
			if plan.Token == "" || scope == "deep" && (len(plan.Entries) != 2 || plan.Entries[1].Path != deep) {
				t.Fatal(plan.CheckoutScan)
			}
			must(t, s.ForceDeleteWorkspace(t.Context(), target, plan.Token))
			if _, err := os.Lstat(y.Path); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
		})
	}
}
