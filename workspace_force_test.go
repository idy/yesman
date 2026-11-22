package main

import (
	"errors"
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

func TestWorkspaceForceDeleteBazelCheckout(t *testing.T) {
	s, y, target := deletionFixture(t)
	dir := fixtureLocalCheckout(t, y, bazelCheckout)
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
	if local.Problem != "" || len(local.Entries) != 2 || local.Entries[0].Path != bazelCheckout || local.Entries[1].Path != ".yesman/tmp/z-external-pointer" {
		t.Fatalf("local inventory: %+v", local)
	}
	if err := s.DeleteWorkspace(t.Context(), target); err == nil || !strings.Contains(err.Error(), bazelCheckout) {
		t.Fatalf("expected nested checkout blocker: %v", err)
	}
	plan, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
	must(t, err)
	if plan.Token == "" || plan.Problem != "" || len(plan.Entries) != 2 {
		t.Fatalf("force confirmation: %+v", plan)
	}
	must(t, s.ForceDeleteWorkspace(t.Context(), target, plan.Token))
	if _, err := os.Stat(y.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("workspace remains: %v", err)
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

func TestWorkspaceForceCannotBypassRegisteredWorktree(t *testing.T) {
	s, y, target := deletionFixture(t)
	fixtureLocalCheckout(t, y, bazelCheckout)
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
	fixtureLocalCheckout(t, y, bazelCheckout)
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
	extra := fixtureLocalCheckout(t, y, ".yesman/tmp/new-checkout")
	if err := s.ForceDeleteWorkspace(t.Context(), target, stale); err == nil || !strings.Contains(err.Error(), "paths changed") {
		t.Fatalf("stale confirmation: %v", err)
	}
	mustReadFile(t, filepath.Join(extra, "untracked.txt"))
	stale = newPlan()
	must(t, os.Rename(extra, filepath.Join(t.TempDir(), "moved")))
	fixtureLocalCheckout(t, y, ".yesman/tmp/new-checkout")
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
