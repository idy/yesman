package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/yesmen"
)

// Keep both the external separate-Git case and the indistinguishable legacy
// embedded checkout in the same matrix. Neither local layout proves ownership.
func TestWorkspaceForceProtectsGitDataAtItsLivePath(t *testing.T) {
	for _, mode := range []string{"external-gitfile", "embedded", "local-gitfile", "local-registration", "worktree-config", "bare", "private"} {
		t.Run(mode, func(t *testing.T) {
			s, y, target := deletionFixture(t)
			folder := filepath.Join(y.MetaDir(), "tmp", "project-output")
			must(t, os.MkdirAll(folder, 0700))
			must(t, os.WriteFile(filepath.Join(folder, "build.txt"), []byte("ordinary build output\n"), 0600))
			outside, err := s.HireNamed(t.Context(), "Other Git project")
			must(t, err)
			project := folder
			gitDir := filepath.Join(folder, ".git")
			switch mode {
			case "external-gitfile":
				project = filepath.Join(outside.Path, "project")
				must(t, os.Mkdir(project, 0700))
				fixtureGit(t, project, "init", "-q", "-b", "local-only", "--separate-git-dir", gitDir)
				fixtureGit(t, project, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "-c", "core.hooksPath="+os.DevNull, "commit", "-q", "--allow-empty", "-m", "outside commit")
			case "local-gitfile":
				project = fixtureLocalCheckout(t, y, ".yesman/tmp/zz-checkout")
				fixtureGit(t, project, "init", "-q", "--separate-git-dir", gitDir)
			case "local-registration":
				initFixtureCheckout(t, project)
				fixtureGit(t, project, "worktree", "add", "-b", "local-link", filepath.Join(y.MetaDir(), "tmp", "linked"))
			case "worktree-config":
				initFixtureCheckout(t, project)
				fixtureGit(t, project, "config", "extensions.worktreeConfig", "true")
				fixtureGit(t, project, "config", "--worktree", "core.worktree", outside.Path)
			case "bare":
				source := filepath.Join(t.TempDir(), "source")
				initFixtureCheckout(t, source)
				gitDir = filepath.Join(folder, "common-data")
				fixtureGit(t, folder, "clone", "--bare", "-q", source, gitDir)
				project = gitDir
			case "private":
				source := filepath.Join(t.TempDir(), "source")
				initFixtureCheckout(t, source)
				project = filepath.Join(outside.Path, "linked")
				fixtureGit(t, source, "worktree", "add", "-b", "kept-linked", project)
				private := fixtureGit(t, project, "rev-parse", "--absolute-git-dir")
				common := fixtureGit(t, project, "rev-parse", "--path-format=absolute", "--git-common-dir")
				gitDir = filepath.Join(folder, "private-data")
				must(t, os.Rename(private, gitDir))
				must(t, os.WriteFile(filepath.Join(gitDir, "commondir"), []byte(common+"\n"), 0600))
				must(t, os.WriteFile(filepath.Join(project, ".git"), []byte("gitdir: "+gitDir+"\n"), 0600))
			default:
				initFixtureCheckout(t, project)
			}
			head := fixtureGit(t, project, "rev-parse", "HEAD")
			saved := mustReadFile(t, filepath.Join(gitDir, "HEAD"))
			var pointer []byte
			if mode == "external-gitfile" || mode == "private" {
				pointer = mustReadFile(t, filepath.Join(project, ".git"))
			}
			preflight, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
			must(t, err)
			if preflight.Token != "" || len(preflight.Entries) == 0 || !strings.Contains(preflight.Problem, gitDir) || !strings.Contains(preflight.Problem, "may be used by another checkout") {
				t.Fatalf("missing live Git hold at %s: %+v", gitDir, preflight)
			}
			if err := s.DeleteWorkspace(t.Context(), target); err == nil || !strings.Contains(err.Error(), gitDir) {
				t.Fatalf("ordinary deletion did not hold Git data: %v", err)
			}
			if got := fixtureGit(t, project, "rev-parse", "HEAD"); got != head {
				t.Fatalf("HEAD changed from %s to %s", head, got)
			}
			if !bytes.Equal(mustReadFile(t, filepath.Join(gitDir, "HEAD")), saved) || pointer != nil && !bytes.Equal(mustReadFile(t, filepath.Join(project, ".git")), pointer) {
				t.Fatal("Git data or external pointer changed")
			}
			if staged, err := s.StoredWorkspaces(); err != nil || len(staged) != 0 {
				t.Fatalf("hold detached live paths: %v %v", staged, err)
			}
		})
	}
}

func TestWorkspaceForceHoldsNewGitDataAfterDrain(t *testing.T) {
	s, y, target := deletionFixture(t)
	fixtureExternalGitfile(t, y, bazelCheckout)
	checks := s.app.pool.CheckWorkspaceDeletion
	_, plan, err := s.app.yesmen.PrepareForceDeletion(t.Context(), target, checks, nil)
	must(t, err)
	if plan == nil {
		t.Fatal("external gitfile did not admit a confirmation")
	}
	repo := filepath.Join(y.MetaDir(), "tmp", "new-git-data")
	detached, err := s.app.yesmen.DeleteWithCheckoutConfirmation(t.Context(), target, plan, checks, func(yesmen.Yesman) error {
		initFixtureCheckout(t, repo)
		return nil
	}, nil)
	if detached || err == nil || !strings.Contains(err.Error(), filepath.Join(repo, ".git")) {
		t.Fatalf("new Git store did not hold before detach: %t %v", detached, err)
	}
	fixtureGit(t, repo, "rev-parse", "HEAD")
}
