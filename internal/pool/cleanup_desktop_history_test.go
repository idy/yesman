package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeRecoveryKeepsPrivateReflogCommits(t *testing.T) {
	p, repo, wt, workspace := newTestCheckout(t)
	run(t, repo.Path, "config", "core.logAllRefUpdates", "always")
	run(t, wt.Path, "switch", "--detach", "HEAD")
	run(t, wt.Path, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "An unpublished detached commit")
	old := run(t, wt.Path, "rev-parse", "HEAD")
	run(t, wt.Path, "switch", wt.Branch)
	if !strings.Contains(run(t, wt.Path, "reflog", "show", "--format=%H", "HEAD"), old) {
		t.Fatal("fixture has no detached reflog entry")
	}
	if err := p.RemoveTemporaryWorktree(t.Context(), workspace, wt); err != nil {
		t.Fatal(err)
	}
	run(t, repo.Path, "reflog", "expire", "--expire=now", "--all")
	run(t, repo.Path, "gc", "--prune=now")
	ref := "refs/yesman/worktree-history/" + wt.CleanupID + "/" + old
	if got := run(t, repo.Path, "show-ref", "--verify", "--hash", ref); got != old {
		t.Fatalf("reflog checkpoint = %s", got)
	}
	run(t, repo.Path, "cat-file", "-e", old+"^{commit}")
	if got := run(t, repo.Path, "show-ref", "--verify", "--hash", "refs/heads/"+wt.Branch); got != wt.Head {
		t.Fatal("local branch changed", got)
	}
}

func TestWorktreePreviewAndRemovalProtectCleanSubmodule(t *testing.T) {
	p, repo, wt, workspace := newTestCheckout(t)
	run(t, wt.Path, "update-index", "--add", "--cacheinfo", "160000,"+wt.Head+",module")
	run(t, wt.Path, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "Add a gitlink")
	if err := os.Mkdir(filepath.Join(wt.Path, "module"), 0755); err != nil {
		t.Fatal(err)
	}
	wt.Head = run(t, wt.Path, "rev-parse", "HEAD")
	if got := run(t, wt.Path, "status", "--porcelain=v1", "--ignore-submodules=none"); got != "" {
		t.Fatal("expected clean gitlink fixture", got)
	}
	_, err := p.InspectTemporaryWorktree(t.Context(), workspace, wt)
	if err == nil || !strings.Contains(err.Error(), "submodule") {
		t.Fatalf("preview: %v", err)
	}
	if err := p.RemoveTemporaryWorktree(t.Context(), workspace, wt); err == nil || !strings.Contains(err.Error(), "submodule") {
		t.Fatalf("remove: %v", err)
	}
	if refExists(t.Context(), repo.Path, "refs/yesman/worktrees/"+wt.CleanupID) {
		t.Fatal("declined checkout was checkpointed")
	}
}
