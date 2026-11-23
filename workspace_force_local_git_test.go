package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/safefs/linktest"
	"github.com/idy/yesman/internal/yesmen"
)

var sevenLocalGitRoots = []string{
	".yesman/tmp/t12/display-fw/host-jieli-sdk",
	".yesman/tmp/t14/h106-app/host/idf",
	".yesman/tmp/t14/h106-app/host-config-check/idf",
	".yesman/tmp/t14/h106-app/host-fix/idf",
	".yesman/tmp/t15/bk-rgb-opt/sdk-inspect.git",
	".yesman/tmp/t23/bk-rgb-opt/firmware-build-src",
	".yesman/tmp/t3/display-fw/host-esp/idf",
}

func seedSevenLocalGitRoots(t *testing.T, workspace string) (moduleOrigin, independent string) {
	t.Helper()
	outside := t.TempDir()
	moduleOrigin = filepath.Join(outside, "module-origin")
	initFixtureCheckout(t, moduleOrigin)
	independent = filepath.Join(outside, "independent")
	initFixtureCheckout(t, independent)
	idf := filepath.Join(outside, "idf-template")
	initFixtureCheckout(t, idf)
	for n := range 28 {
		fixtureGit(t, idf, "-c", "protocol.file.allow=always", "submodule", "add", "-q", moduleOrigin, fmt.Sprintf("components/module-%02d", n))
	}
	fixtureGit(t, idf, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "-qm", "record 28 absorbed modules")
	for _, rel := range sevenLocalGitRoots {
		dir := filepath.Join(workspace, filepath.FromSlash(rel))
		must(t, os.MkdirAll(filepath.Dir(dir), 0700))
		switch {
		case strings.HasSuffix(rel, "/idf"):
			must(t, os.CopyFS(dir, os.DirFS(idf)))
		case strings.HasSuffix(rel, ".git"):
			fixtureGit(t, outside, "clone", "--bare", "--no-hardlinks", "-q", independent, dir)
		default:
			initFixtureCheckout(t, dir)
		}
		fixtureGit(t, dir, "branch", "keep-local-history")
	}
	for n := range 10 {
		dir := filepath.Join(workspace, filepath.FromSlash(bazelCheckout), fmt.Sprintf("output-%02d", n))
		must(t, os.MkdirAll(dir, 0700))
		target := filepath.Join(workspace, "missing-remote-checkout", ".git")
		if n == 9 {
			target = filepath.Join(workspace, filepath.FromSlash(sevenLocalGitRoots[5]), ".git")
		}
		linktest.Symlink(t, target, filepath.Join(dir, ".git"))
	}
	return moduleOrigin, independent
}

func TestForceLocalGitDeletionSevenStores(t *testing.T) {
	s, y, target := deletionFixture(t)
	origin, independent := seedSevenLocalGitRoots(t, y.Path)
	before := fixtureGit(t, origin, "show-ref") + fixtureGit(t, independent, "show-ref")
	if err := s.DeleteWorkspace(t.Context(), target); err == nil {
		t.Fatal("ordinary deletion accepted a repository")
	}
	p, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
	must(t, err)
	if p.Token == "" || len(p.LocalGitStores) != 7 || len(p.LinkMarkers) != 10 || len(p.Entries) != 17 {
		t.Fatalf("missing confirmation for seven local stores and ten Git links: %s (%d/%d/%d)", p.Problem, len(p.LocalGitStores), len(p.LinkMarkers), len(p.Entries))
	}
	must(t, s.ForceDeleteWorkspace(t.Context(), target, p.Token))
	if _, err := os.Lstat(y.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("workspace remains: %v", err)
	}
	if got := fixtureGit(t, origin, "show-ref") + fixtureGit(t, independent, "show-ref"); got != before {
		t.Fatal("outside Git refs changed")
	}
}

func TestForceLocalGitDeletionDependencies(t *testing.T) {
	for _, guard := range []string{"objects/info/alternates", "objects/info/http-alternates", "objects/pack/local.promisor", "index.lock", "include", "linked-refs", "partial", "external-core-worktree"} {
		t.Run(guard, func(t *testing.T) {
			s, y, target := deletionFixture(t)
			repo := fixtureLocalCheckout(t, y, "repo")
			outside := filepath.Join(t.TempDir(), "outside")
			initFixtureCheckout(t, outside)
			before := fixtureGit(t, outside, "show-ref")
			admin := filepath.Join(repo, ".git")
			switch guard {
			case "include":
				fixtureGit(t, repo, "config", "include.path", filepath.Join(outside, ".git", "config"))
			case "linked-refs":
				must(t, os.RemoveAll(filepath.Join(admin, "refs")))
				linktest.Symlink(t, filepath.Join(outside, ".git", "refs"), filepath.Join(admin, "refs"))
			case "external-core-worktree":
				fixtureGit(t, repo, "config", "core.worktree", outside)
			case "partial":
				must(t, os.Remove(filepath.Join(admin, "config")))
			default:
				must(t, os.WriteFile(filepath.Join(admin, filepath.FromSlash(guard)), []byte(filepath.Join(outside, ".git", "objects")+"\n"), 0600))
			}
			p, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
			must(t, err)
			if p.Token != "" || !strings.Contains(p.Problem, admin) {
				t.Fatalf("dependency was admitted: %+v", p)
			}
			if got := fixtureGit(t, outside, "show-ref"); got != before {
				t.Fatalf("outside refs changed: %s", got)
			}
			mustReadFile(t, filepath.Join(repo, "untracked.txt"))
		})
	}
}

func TestForceLocalGitDeletionPartialBareStage(t *testing.T) {
	s, y, target := deletionFixture(t)
	origin := filepath.Join(t.TempDir(), "origin")
	initFixtureCheckout(t, origin)
	repo := filepath.Join(y.MetaDir(), "tmp", "partial.git")
	must(t, os.MkdirAll(filepath.Dir(repo), 0700))
	fixtureGit(t, origin, "clone", "--bare", "--no-hardlinks", "-q", origin, repo)
	// Model an interrupted bare store with recognized Git control files
	// but without its objects directory.
	must(t, os.RemoveAll(filepath.Join(repo, "objects")))
	stage := filepath.Join(s.app.home.Yesmen(), ".deleting-"+target.Instance+"."+target.Entry)
	must(t, os.Rename(y.Path, stage))
	rows, err := s.StoredWorkspaces()
	must(t, err)
	if len(rows) != 1 || !rows[0].Pending {
		t.Fatal(rows)
	}
	p, err := s.CheckWorkspaceForceDeletion(t.Context(), rows[0])
	must(t, err)
	if p.Token != "" || !strings.Contains(p.Problem, "partial.git") {
		t.Fatalf("partial Git store was not held: %+v", p)
	}
	mustReadFile(t, filepath.Join(stage, ".yesman", "tmp", "partial.git", "HEAD"))
}

func TestForceLocalGitDeletionRechecksDrainAndIdentity(t *testing.T) {
	for _, mode := range []string{"late-process", "replaced-store", "new-registration"} {
		t.Run(mode, func(t *testing.T) {
			s, y, target := deletionFixture(t)
			repo := fixtureLocalCheckout(t, y, "repo")
			reg := s.app.pool.CheckWorkspaceDeletion
			checks := pool.WorkspaceGitDeletionChecks()
			if mode == "late-process" && runtime.GOOS == "windows" {
				t.Skip("actual cwd process control uses the Unix process scanner")
			}
			_, plan, err := s.app.yesmen.PrepareForceDeletion(t.Context(), target, reg, nil, checks)
			must(t, err)
			if plan == nil {
				t.Fatal("missing initial confirmation")
			}
			detached, err := s.app.yesmen.DeleteWithCheckoutConfirmation(t.Context(), target, plan, reg, func(yesmen.Yesman) error {
				switch mode {
				case "late-process":
					cmd := exec.CommandContext(t.Context(), "sleep", "60")
					cmd.Dir = repo
					must(t, cmd.Start())
					t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
				case "replaced-store":
					must(t, os.Rename(filepath.Join(repo, ".git"), filepath.Join(t.TempDir(), "original-git")))
					initFixtureCheckout(t, repo)
				case "new-registration":
					fixtureGit(t, repo, "worktree", "add", "-qb", "late-branch", filepath.Join(t.TempDir(), "outside-consumer"))
				}
				return nil
			}, nil)
			if err == nil || detached || errors.Is(err, context.Canceled) || errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("late state was not held before detaching: detached=%t, %v", detached, err)
			}
			mustReadFile(t, filepath.Join(repo, "untracked.txt"))
		})
	}
}
