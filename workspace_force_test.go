package main

import (
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

const bazelCheckout = ".yesman/tmp/t12/display-fw/bazel-output/execroot/_main"

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if runtime.GOOS == "windows" {
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.longpaths", "GIT_CONFIG_VALUE_0=true")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func fixtureLocalCheckout(t *testing.T, y yesmen.Yesman, rel string) string {
	t.Helper()
	dir := filepath.Join(y.Path, filepath.FromSlash(rel))
	initFixtureCheckout(t, dir)
	return dir
}

func initFixtureCheckout(t *testing.T, dir string) {
	t.Helper()
	must(t, os.MkdirAll(dir, 0700))
	fixtureGit(t, dir, "init", "-q", "-b", "local-only")
	fixtureGit(t, dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "-c", "core.hooksPath="+os.DevNull, "commit", "-q", "--allow-empty", "-m", "private commit")
	must(t, os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("dirty workspace content"), 0600))
}

func fixtureExternalGitfile(t *testing.T, y yesmen.Yesman, rel string) string {
	t.Helper()
	dir := fixtureLocalCheckout(t, y, rel)
	separateFixtureGitData(t, dir)
	return dir
}

func separateFixtureGitData(t *testing.T, dir string) {
	t.Helper()
	fixtureGit(t, dir, "init", "-q", "--separate-git-dir", filepath.Join(t.TempDir(), "kept-git-data"))
}

func TestWorkspaceForceDeleteBazelCheckout(t *testing.T) {
	s, y, target := deletionFixture(t)
	dir := fixtureLocalCheckout(t, y, bazelCheckout)
	fixtureGit(t, dir, "worktree", "add", "-b", "dependent", filepath.Join(t.TempDir(), "dependent"))
	dependent := filepath.Join(t.TempDir(), "second-dependent")
	fixtureGit(t, dir, "worktree", "add", "-b", "second-dependent", dependent)
	external, err := s.HireNamed(t.Context(), "External checkout")
	must(t, err)
	outside := fixtureLocalCheckout(t, external, "keep")
	refs := fixtureGit(t, outside, "show-ref")
	head := mustReadFile(t, filepath.Join(outside, ".git", "HEAD"))
	// A gitfile may point at other Git data. Delete only the local entry.
	pointer := filepath.Join(y.MetaDir(), "tmp", "z-external-pointer")
	must(t, os.MkdirAll(pointer, 0700))
	must(t, os.WriteFile(filepath.Join(pointer, ".git"), []byte("gitdir: "+filepath.Join(outside, ".git")+"\n"), 0600))
	linktest.Symlink(t, outside, filepath.Join(dir, "linked-outside"))
	p, _ := poolWith(t, "untouched")
	s.app.pool = p
	repos, err := p.List()
	must(t, err)
	poolRefs := fixtureGit(t, repos[0].Path, "show-ref")
	poolGitConfig := mustReadFile(t, filepath.Join(repos[0].Path, "config"))
	info, err := s.Info(t.Context(), y.Name)
	must(t, err)
	if len(info.Worktrees) != 0 {
		t.Fatalf("discovery granted managed inventory: %+v", info.Worktrees)
	}
	local, err := s.WorkspaceCheckouts(t.Context(), y.Name, y.HiredAt)
	must(t, err)
	if local.Problem != "" || len(local.Entries) != 0 {
		t.Fatalf("sidebar must skip workspace temp: %+v", local)
	}
	gitDataHold := "Git data at " + filepath.Join(dir, ".git") + " may be used by another checkout."
	if err := s.DeleteWorkspace(t.Context(), target); err == nil || !strings.Contains(err.Error(), gitDataHold) {
		t.Fatalf("expected nested Git data hold %q, got %v", gitDataHold, err)
	}
	held, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
	must(t, err)
	if held.Token != "" || len(held.Entries) != 1 || held.Entries[0].Path != bazelCheckout || !strings.Contains(held.Problem, gitDataHold) {
		t.Fatalf("legacy Git data should hold at its live path: %+v", held)
	}
	dependentHead := fixtureGit(t, dependent, "rev-parse", "HEAD")
	// Git retains the known dependent and its registrations outside the workspace.
	separateFixtureGitData(t, dir)
	fixtureGit(t, dir, "worktree", "repair", dependent)
	if fixtureGit(t, dependent, "rev-parse", "HEAD") != dependentHead {
		t.Fatal("dependent checkout changed")
	}
	kept := fixtureGit(t, dir, "rev-parse", "--absolute-git-dir")
	keptHead := mustReadFile(t, filepath.Join(kept, "HEAD"))
	plan, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
	must(t, err)
	if plan.Token == "" || plan.Problem != "" || len(plan.Entries) != 2 {
		t.Fatalf("force confirmation: %+v", plan)
	}
	must(t, s.ForceDeleteWorkspace(t.Context(), target, plan.Token))
	if _, err := os.Stat(y.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("workspace remains: %v", err)
	}
	if got := mustReadFile(t, filepath.Join(kept, "HEAD")); string(got) != string(keptHead) {
		t.Fatal("the former local checkout's relocated Git metadata changed")
	}
	if got := fixtureGit(t, outside, "show-ref"); got != refs || string(mustReadFile(t, filepath.Join(outside, ".git", "HEAD"))) != string(head) || string(mustReadFile(t, filepath.Join(outside, "untracked.txt"))) != "dirty workspace content" {
		t.Fatal("another workspace's Git state or files changed")
	}
	if fixtureGit(t, repos[0].Path, "show-ref") != poolRefs || string(mustReadFile(t, filepath.Join(repos[0].Path, "config"))) != string(poolGitConfig) {
		t.Fatal("shared Git refs or config changed")
	}
	if err := s.ForceDeleteWorkspace(t.Context(), target, plan.Token); err == nil {
		t.Fatal("replayed consumed token")
	}
}

func TestWorkspaceForceReportsGitHoldBeforeLateScanLimit(t *testing.T) {
	s, y, target := deletionFixture(t)
	marker := filepath.Join(y.Path, filepath.FromSlash(bazelCheckout), ".git")
	must(t, os.MkdirAll(filepath.Dir(marker), 0700))
	linktest.Symlink(t, filepath.Join(y.Path, "gizos-display-pr", ".git"), marker)
	repo := filepath.Join(y.MetaDir(), "tmp", "t12", "display-fw", "host-jieli-sdk")
	must(t, os.MkdirAll(repo, 0700))
	initFixtureCheckout(t, repo)
	outside := filepath.Join(t.TempDir(), "dependent")
	fixtureGit(t, repo, "worktree", "add", "-b", "dependent", outside)
	head := fixtureGit(t, outside, "rev-parse", "HEAD")

	// A deterministic substitute for a slow build tree: the old scanner
	// exhausts its result limit in this later subtree before reporting the
	// earlier protected store. The ordinary route stops at the Bazel link.
	tail := filepath.Join(y.MetaDir(), "tmp", "t20")
	for i := range 256 {
		base := filepath.Join(tail, fmt.Sprintf("checkout-%03d", i))
		must(t, os.MkdirAll(base, 0700))
		must(t, os.WriteFile(filepath.Join(base, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git")+"\n"), 0600))
	}
	if err := s.DeleteWorkspace(t.Context(), target); err == nil || !strings.Contains(err.Error(), bazelCheckout+" contains a Git checkout") {
		t.Fatalf("ordinary deletion did not stop at the Bazel checkout: %v", err)
	}
	result, err := s.ReviewWorkspaceForceDeletion(t.Context(), target, strings.Repeat("a", 36))
	must(t, err)
	if result.Token != "" || !strings.Contains(result.Problem, filepath.Join(repo, ".git")) || len(result.Entries) != 2 {
		t.Fatalf("expected only the discovered paths and an actionable protected-store hold, got %d entries, token present %t, problem %q", len(result.Entries), result.Token != "", result.Problem)
	}
	got, err := os.Readlink(marker)
	must(t, err)
	if got != filepath.Join(y.Path, "gizos-display-pr", ".git") || fixtureGit(t, outside, "rev-parse", "HEAD") != head {
		t.Fatal("ordinary deletion or review modified the checkout or its external consumer")
	}
}

func TestWorkspaceForceCannotBypassRegisteredWorktree(t *testing.T) {
	s, y, target := deletionFixture(t)
	fixtureExternalGitfile(t, y, bazelCheckout)
	p, _ := poolWith(t, "project")
	s.app.pool = p
	repos, err := p.List()
	must(t, err)
	repo := repos[0]
	w, err := p.AddWorktree(t.Context(), repo, pool.WorktreeOptions{Dest: filepath.Join(y.Path, "registered"), Ref: "origin/main"})
	must(t, err)
	plan, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
	must(t, err)
	if plan.Token != "" || !strings.Contains(plan.Problem, w.Dir) {
		t.Fatalf("registered checkout was not held: %+v", plan)
	}
	if got, err := p.HasWorktreeRegistration(t.Context(), w.Repo, w.Path); err != nil || !got {
		t.Fatalf("registered checkout changed: %v %v", got, err)
	}
	mustReadFile(t, filepath.Join(w.Path, ".git"))
}

func TestWorkspaceForceDeletionScopeAndRefresh(t *testing.T) {
	s, y, target := deletionFixture(t)
	fixtureExternalGitfile(t, y, bazelCheckout)
	other, err := s.HireNamed(t.Context(), "Same scope test")
	must(t, err)
	otherTarget, err := s.PrepareWorkspaceDeletion(other.Name, other.HiredAt)
	must(t, err)
	newPlan := func() string {
		t.Helper()
		p, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
		must(t, err)
		if p.Token == "" || p.Problem != "" {
			t.Fatalf("force confirmation: %+v", p)
		}
		return p.Token
	}
	if err := s.ForceDeleteWorkspace(t.Context(), otherTarget, newPlan()); err == nil {
		t.Fatal("accepted a different workspace")
	}
	canceled := newPlan()
	s.CancelWorkspaceForceDeletion(canceled)
	if err := s.ForceDeleteWorkspace(t.Context(), target, canceled); err == nil {
		t.Fatal("accepted a canceled token")
	}
	stale := newPlan()
	extra := fixtureExternalGitfile(t, y, ".yesman/tmp/new-checkout")
	if err := s.ForceDeleteWorkspace(t.Context(), target, stale); err == nil || !strings.Contains(err.Error(), "paths changed") {
		t.Fatalf("stale confirmation: %v", err)
	}
	mustReadFile(t, filepath.Join(extra, "untracked.txt"))
	stale = newPlan()
	must(t, os.Rename(extra, filepath.Join(t.TempDir(), "moved")))
	fixtureExternalGitfile(t, y, ".yesman/tmp/new-checkout")
	if err := s.ForceDeleteWorkspace(t.Context(), target, stale); err == nil {
		t.Fatal("accepted same-path replacement checkout")
	}
	old := newPlan()
	must(t, s.ForceDeleteWorkspace(t.Context(), target, newPlan()))
	fresh, err := s.HireNamed(t.Context(), y.Label())
	must(t, err)
	if fresh.Name != y.Name {
		t.Fatalf("rehired wrong workspace: %+v", fresh)
	}
	if err := s.ForceDeleteWorkspace(t.Context(), target, old); err == nil {
		t.Fatal("accepted old workspace generation")
	}
	mustReadFile(t, filepath.Join(fresh.MetaDir(), "yesman.json"))
	mustReadFile(t, filepath.Join(other.MetaDir(), "yesman.json"))
}
