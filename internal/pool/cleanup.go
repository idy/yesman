package pool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var cleanupIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// HasWorktreeRegistration includes prunable/missing registrations, which the
// normal workspace display omits. An empty repo scans all pool repositories;
// recovery must not mistake any remaining registration for successful removal.
func (p *Pool) HasWorktreeRegistration(ctx context.Context, repo, path string) (bool, error) {
	var repos []Repo
	if repo == "" {
		var err error
		repos, err = p.List()
		if err != nil {
			return false, err
		}
	} else {
		r, err := p.Get(repo)
		if err != nil {
			return false, err
		}
		repos = []Repo{r}
	}
	for _, r := range repos {
		text, err := git(ctx, r.Path, "worktree", "list", "--porcelain")
		if err != nil {
			return false, err
		}
		for _, line := range strings.Split(text, "\n") {
			if value, ok := strings.CutPrefix(line, "worktree "); ok && realPath(value) == realPath(path) {
				return true, nil
			}
		}
	}
	return false, nil
}

// CheckTemporaryWorktree validates source identity without freezing a cleanup
// authorization or changing refs. Remove rechecks the same instance under lock.
func (p *Pool) CheckTemporaryWorktree(ctx context.Context, workspace string, expected Worktree) error {
	r, err := p.Get(expected.Repo)
	if err != nil {
		return err
	}
	l := p.lock(r.Name)
	l.Lock()
	defer l.Unlock()
	_, err = p.checkTemporaryWorktree(ctx, r, workspace, expected)
	return err
}

func (p *Pool) checkTemporaryWorktree(ctx context.Context, r Repo, workspace string, expected Worktree) (string, error) {
	path := filepath.Join(workspace, expected.Dir)
	if filepath.Base(expected.Dir) != expected.Dir || expected.Dir == "." || realPath(path) != filepath.Join(realPath(workspace), expected.Dir) {
		return "", fmt.Errorf("retain %s: not a direct worktree of this workspace", expected.Dir)
	}
	if st, err := os.Lstat(path); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("retain %s: worktree directory missing or replaced", expected.Dir)
	}
	if !cleanupIDRE.MatchString(expected.CleanupID) {
		return "", fmt.Errorf("retain %s: invalid checkout ID", expected.Dir)
	}
	wts, err := p.worktrees(ctx, r)
	if err != nil {
		return "", err
	}
	for _, wt := range wts {
		if realPath(wt.Path) != realPath(path) {
			continue
		}
		if wt.CleanupID != expected.CleanupID {
			return "", fmt.Errorf("retain %s: worktree instance changed or marker missing", expected.Dir)
		}
		if wt.Head != expected.Head || wt.Branch != expected.Branch || wt.Detached != expected.Detached {
			return "", fmt.Errorf("retain %s: HEAD or checkout changed since completion", expected.Dir)
		}
		if wt.Branch != "" && (wt.Branch == r.DefaultBranch || wt.Branch == "main" || wt.Branch == "master") {
			return "", fmt.Errorf("retain %s: primary branch", expected.Dir)
		}
		return path, nil
	}
	return "", fmt.Errorf("retain %s: not registered in this workspace", expected.Dir)
}

// RemoveTemporaryWorktree removes exactly the registered checkout inspected by
// the caller. The repository lock closes races with other management requests;
// git still refuses changes made between the inspection and removal. An
// immutable recovery ref keeps the exact source alive even after squash merge
// and remote branch pruning. Existing branches, refs and tags are never changed.
func (p *Pool) RemoveTemporaryWorktree(ctx context.Context, workspace string, expected Worktree) error {
	r, err := p.Get(expected.Repo)
	if err != nil {
		return err
	}
	l := p.lock(r.Name)
	l.Lock()
	defer l.Unlock()
	path, err := p.checkTemporaryWorktree(ctx, r, workspace, expected)
	if err != nil {
		return err
	}
	// git worktree remove alone does not protect ignored files. Include every
	// unknown path and disable status configuration that hides untracked files.
	status, err := git(ctx, path, "status", "--porcelain=v1", "--untracked-files=all", "--ignored", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("retain %s: uncommitted, untracked or ignored files", expected.Dir)
	}
	// Index flags can hide tracked edits from status and worktree remove.
	entries, err := git(ctx, path, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(entries, "\x00") {
		if entry != "" && !strings.HasPrefix(entry, "H ") {
			return fmt.Errorf("retain %s: index flags may hide local work", expected.Dir)
		}
	}
	for _, state := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer", "BISECT_LOG"} {
		statePath, err := git(ctx, path, "rev-parse", "--git-path", state)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(statePath) {
			statePath = filepath.Join(path, statePath)
		}
		if _, err := os.Lstat(statePath); err == nil || !os.IsNotExist(err) {
			return fmt.Errorf("retain %s: git operation in progress or state unreadable", expected.Dir)
		}
	}
	// A remote-tracking ref can disappear on the next fetch after a squash
	// merge. Pin this generation's source before removing its last checkout.
	if !cleanupIDRE.MatchString(expected.CleanupID) {
		return fmt.Errorf("retain %s: invalid checkout ID", expected.Dir)
	}
	checkpoint := "refs/yesman/worktrees/" + expected.CleanupID
	if old, err := git(ctx, path, "rev-parse", "--verify", checkpoint); err == nil {
		if old != expected.Head {
			return fmt.Errorf("retain %s: recovery ref changed", expected.Dir)
		}
	} else if _, err := git(ctx, path, "update-ref", checkpoint, expected.Head, strings.Repeat("0", len(expected.Head))); err != nil {
		return fmt.Errorf("retain %s: cannot preserve exact HEAD: %w", expected.Dir, err)
	}
	// Re-read the actual private Git marker and source immediately before
	// deleting, under the same lock used by every pool checkout/removal path.
	if _, err := p.checkTemporaryWorktree(ctx, r, workspace, expected); err != nil {
		return err
	}
	_, err = git(ctx, r.Path, "worktree", "remove", path)
	return err
}
