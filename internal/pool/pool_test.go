package pool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNameFromURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/idy/yesman":         "github.com/idy/yesman",
		"https://github.com/idy/yesman.git":     "github.com/idy/yesman",
		"git@github.com:idy/yesman.git":         "github.com/idy/yesman",
		"ssh://git@GitHub.com:22/idy/yesman":    "github.com/idy/yesman",
		"https://gitlab.com/group/sub/proj.git": "gitlab.com/group/sub/proj",
		"file:///tmp/some/origin.git":           "local/origin",
	}
	for in, want := range cases {
		got, err := NameFromURL(in)
		if err != nil || got != want {
			t.Errorf("NameFromURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "nope", "https://github.com/../x", "https://github.com/a b/c"} {
		if got, err := NameFromURL(bad); err == nil {
			t.Errorf("NameFromURL(%q) = %q, want an error", bad, got)
		}
	}
}

// run runs git in dir and fails the test on error.
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newOrigin makes a repository with a main and a feature branch.
func newOrigin(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "--quiet", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", ".")
	run(t, dir, "commit", "--quiet", "-m", "first")
	run(t, dir, "checkout", "--quiet", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("f\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", ".")
	run(t, dir, "commit", "--quiet", "-m", "feature")
	run(t, dir, "tag", "v1")
	run(t, dir, "checkout", "--quiet", "main")
}

func TestPool(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	ctx := context.Background()
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "src", "origin")
	newOrigin(t, origin)

	p := New(filepath.Join(tmp, "repos"))
	r, err := p.Add(ctx, origin, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "local/origin" || r.DefaultBranch != "main" || r.URL != origin {
		t.Fatalf("added %+v", r)
	}
	if heads := run(t, r.Path, "for-each-ref", "refs/heads/"); heads != "" {
		t.Errorf("pool repo keeps local branches:\n%s", heads)
	}
	if again, err := p.Add(ctx, origin, ""); err != nil || again.Path != r.Path {
		t.Errorf("adding again = %+v, %v", again, err)
	}
	if got, err := p.Get("origin"); err != nil || got.Name != r.Name {
		t.Errorf("Get by short name = %+v, %v", got, err)
	}

	ws := filepath.Join(tmp, "yesmen", "alice")

	// A new branch starts from the remote's default branch.
	wt, err := p.AddWorktree(ctx, r, WorktreeOptions{Dest: filepath.Join(ws, "origin--work"), Branch: "yesman/alice/work"})
	if err != nil {
		t.Fatal(err)
	}
	if wt.Branch != "yesman/alice/work" || wt.Warning != "" {
		t.Errorf("worktree %+v", wt)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "README.md")); err != nil {
		t.Errorf("worktree has no files: %v", err)
	}
	// A new branch must not track main, or a push could target it.
	if out, err := exec.Command("git", "-C", wt.Path, "config", "--get", "branch.yesman/alice/work.merge").Output(); err == nil {
		t.Errorf("new branch tracks %q", out)
	}

	// A branch the remote has is continued and tracked.
	wt2, err := p.AddWorktree(ctx, r, WorktreeOptions{Dest: filepath.Join(ws, "origin--feature"), Branch: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt2.Path, "feature.txt")); err != nil {
		t.Errorf("feature worktree lacks feature.txt: %v", err)
	}

	// A ref is checked out detached.
	wt3, err := p.AddWorktree(ctx, r, WorktreeOptions{Dest: filepath.Join(ws, "origin@v1"), Ref: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if !wt3.Detached {
		t.Errorf("ref checkout is not detached: %+v", wt3)
	}

	// An existing destination is refused.
	if _, err := p.AddWorktree(ctx, r, WorktreeOptions{Dest: wt.Path, Branch: "other"}); err == nil {
		t.Error("checking out over an existing directory succeeded")
	}

	// A commit in a worktree lands in the pool, and new remote commits
	// arrive with the next forced fetch.
	if err := os.WriteFile(filepath.Join(wt.Path, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wt.Path, "add", ".")
	run(t, wt.Path, "commit", "--quiet", "-m", "work")
	if err := os.WriteFile(filepath.Join(origin, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, origin, "add", ".")
	run(t, origin, "commit", "--quiet", "-m", "later")
	if err := p.Fetch(ctx, "origin", true); err != nil {
		t.Fatal(err)
	}
	if got, want := run(t, r.Path, "rev-parse", "origin/main"), run(t, origin, "rev-parse", "main"); got != want {
		t.Errorf("origin/main = %s after fetch, want %s", got, want)
	}

	wts, err := p.Worktrees(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 3 {
		t.Fatalf("worktrees under the workspace = %+v", wts)
	}
	if other, err := p.Worktrees(ctx, filepath.Join(tmp, "yesmen", "bob")); err != nil || len(other) != 0 {
		t.Errorf("another workspace sees %+v, %v", other, err)
	}

	if err := p.RemoveWorktree(ctx, wt3.Path, false); err != nil {
		t.Fatal(err)
	}
	// Uncommitted changes keep a worktree unless forced.
	if err := os.WriteFile(filepath.Join(wt.Path, "dirty.txt"), []byte("d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveWorktree(ctx, wt.Path, false); err == nil {
		t.Error("removed a worktree with uncommitted changes without force")
	}
	if err := p.RemoveWorktree(ctx, wt.Path, true); err != nil {
		t.Fatal(err)
	}
	wts, _ = p.Worktrees(ctx, ws)
	if len(wts) != 1 {
		t.Errorf("after removing two: %+v", wts)
	}

	// A moved workspace keeps its worktrees once repaired, and a prune
	// leaves them alone.
	archived := filepath.Join(tmp, "archive", "alice")
	if err := os.MkdirAll(filepath.Dir(archived), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(ws, archived); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(archived, filepath.Base(wt2.Path))
	if err := p.Repair(ctx, []string{moved}); err != nil {
		t.Fatal(err)
	}
	if err := p.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if wts, _ := p.Worktrees(ctx, archived); len(wts) != 1 || wts[0].Branch != "feature" {
		t.Errorf("worktrees after archiving = %+v", wts)
	}
	run(t, moved, "status", "--short")

	// The upstream of a fork is added to an existing repository.
	up := filepath.Join(tmp, "src", "upstream")
	newOrigin(t, up)
	r, err = p.Add(ctx, origin, up)
	if err != nil {
		t.Fatal(err)
	}
	if r.Upstream != up {
		t.Errorf("upstream = %q", r.Upstream)
	}
	run(t, r.Path, "rev-parse", "--verify", "upstream/main")
}
