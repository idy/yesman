package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/yesmen"
)

func TestWorkspaceForceEffectiveWorktreeConfig(t *testing.T) {
	for _, mode := range []string{"local", "external", "linked-private", "during-drain"} {
		t.Run(mode, func(t *testing.T) {
			s, y, target := deletionFixture(t)
			repo := fixtureLocalCheckout(t, y, bazelCheckout)
			outside := filepath.Join(t.TempDir(), "outside-worktree")
			must(t, os.Mkdir(outside, 0700))
			file := filepath.Join(outside, "keep.txt")
			must(t, os.WriteFile(file, []byte("outside"), 0600))
			configured := repo
			if mode == "linked-private" {
				configured = filepath.Join(y.MetaDir(), "tmp", "local-linked")
				fixtureGit(t, repo, "worktree", "add", "-b", "linked-local", configured)
			}
			fixtureGit(t, repo, "config", "extensions.worktreeConfig", "true")
			setExternal := func() {
				fixtureGit(t, configured, "config", "--worktree", "core.worktree", outside)
				if got := fixtureGit(t, configured, "rev-parse", "--show-toplevel"); !strings.HasSuffix(filepath.ToSlash(got), "/outside-worktree") {
					t.Fatalf("Git did not use config.worktree: %q", got)
				}
			}
			switch mode {
			case "during-drain":
				checks := s.app.pool.CheckWorkspaceDeletion
				_, plan, err := s.app.yesmen.PrepareForceDeletion(t.Context(), target, checks, nil)
				must(t, err)
				if plan == nil {
					t.Fatal("initial local preview refused")
				}
				detached, err := s.app.yesmen.DeleteWithCheckoutConfirmation(t.Context(), target, plan, checks, func(yesmen.Yesman) error {
					setExternal()
					return nil
				}, nil)
				if detached || err == nil || !strings.Contains(err.Error(), outside) {
					t.Fatalf("changed per-worktree config: detached=%t, err=%v", detached, err)
				}
			case "local":
				fixtureGit(t, repo, "config", "--worktree", "core.worktree", repo)
				plan, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
				must(t, err)
				if plan.Token == "" {
					t.Fatal(plan.CheckoutScan)
				}
				must(t, s.ForceDeleteWorkspace(t.Context(), target, plan.Token))
				if _, err := os.Stat(y.Path); !os.IsNotExist(err) {
					t.Fatal(err)
				}
			default:
				setExternal()
				plan, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
				must(t, err)
				if plan.Token != "" || !strings.Contains(plan.Problem, outside) {
					t.Fatalf("external effective config admitted: %+v", plan)
				}
			}
			if got := string(mustReadFile(t, file)); got != "outside" {
				t.Fatal(got)
			}
			if mode != "local" {
				mustReadFile(t, filepath.Join(repo, ".git", "HEAD"))
			}
		})
	}
}

func TestWorkspaceForceRecognizesLocalSeparateGitDirectory(t *testing.T) {
	s, y, target := deletionFixture(t)
	repo := fixtureLocalCheckout(t, y, ".yesman/tmp/checkout")
	storage := filepath.Join(y.MetaDir(), "tmp", "git-storage")
	must(t, os.Mkdir(storage, 0700))
	gitDir := filepath.Join(storage, ".git")
	fixtureGit(t, repo, "init", "--separate-git-dir", gitDir)
	fixtureGit(t, repo, "rev-parse", "HEAD")
	confirm, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
	must(t, err)
	if confirm.Token == "" || len(confirm.Entries) != 2 {
		t.Fatal(confirm.CheckoutScan)
	}
	must(t, s.ForceDeleteWorkspace(t.Context(), target, confirm.Token))
	if _, err := os.Stat(y.Path); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := os.Stat(gitDir); !os.IsNotExist(err) {
		t.Fatal(fs.ErrExist, err)
	}
}
