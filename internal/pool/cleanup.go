package pool

import (
	"context"
	"fmt"
	"maps"
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
		repos, err = p.repositoryPaths(ctx)
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
	r, err := p.RecordedRepo(ctx, expected.Repo)
	if err != nil {
		return err
	}
	l := p.lock(r.Name)
	l.Lock()
	defer l.Unlock()
	_, err = p.checkTemporaryWorktree(ctx, r, workspace, expected, nil)
	return err
}

func (p *Pool) checkTemporaryWorktree(ctx context.Context, r Repo, workspace string, expected Worktree, pinned privateHistory) (string, error) {
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
	// Inspect this registration's inputs through verified handles before Git
	// reads them. Unexpected private state must survive the entire operation.
	history, _, err := inspectWorktreeHistory(ctx, r, path, expected)
	if err != nil {
		return "", fmt.Errorf("retain %s: %w", expected.Dir, err)
	}
	if pinned != nil && !maps.Equal(history, pinned) {
		return "", fmt.Errorf("retain %s: private Git history changed during recovery", expected.Dir)
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
		if expected.Root != nil {
			root, err := rootIdentity(r.Path, path, expected.CleanupID)
			if err != nil || root.Ino != expected.Root.Ino || root.Birth != expected.Root.Birth {
				return "", fmt.Errorf("retain %s: checkout directory identity changed", expected.Dir)
			}
		}
		if wt.Head != expected.Head || wt.Branch != expected.Branch || wt.Detached != expected.Detached {
			return "", fmt.Errorf("retain %s: HEAD or checkout changed since completion", expected.Dir)
		}
		if wt.Branch != "" && (wt.Branch == r.DefaultBranch || wt.Branch == "main" || wt.Branch == "master") {
			return "", fmt.Errorf("retain %s: primary branch", expected.Dir)
		}
		// Use the spelling from Git's own registration for its commands.
		// Windows Git may store a different path through a directory junction.
		return wt.Path, nil
	}
	return "", fmt.Errorf("retain %s: not registered in this workspace", expected.Dir)
}

// RemoveTemporaryWorktree removes exactly the registered checkout inspected by
// the caller. The repository lock closes races with other management requests;
// git still refuses changes made between the inspection and removal. An
// immutable recovery ref keeps the exact source alive even after squash merge
// and remote branch pruning. Existing branches, refs and tags are never changed.
func (p *Pool) RemoveTemporaryWorktree(ctx context.Context, workspace string, expected Worktree) error {
	return p.removeTemporary(ctx, workspace, expected, nil)
}

// TryReclaimTemporary uses the same checks and no-force removal. Housekeeping
// yields to interactive repository and file operations instead of holding up
// this workspace. certify saves the verified proof before any deletion.
func (p *Pool) TryReclaimTemporary(ctx context.Context, workspace string, expected Worktree, certify func() error) error {
	if certify == nil {
		return fmt.Errorf("worktree reclamation needs a completion receipt")
	}
	return p.removeTemporary(ctx, workspace, expected, certify)
}

func (p *Pool) removeTemporary(ctx context.Context, workspace string, expected Worktree, certify func() error) error {
	r, err := p.RecordedRepo(ctx, expected.Repo)
	if err != nil {
		return err
	}
	l := p.lock(r.Name)
	if certify == nil {
		l.Lock()
	} else if !l.TryLock() {
		return fmt.Errorf("repository is in use")
	}
	defer l.Unlock()
	tl, err := p.treeLock(filepath.Join(workspace, expected.Dir))
	if err != nil {
		return err
	}
	if certify == nil {
		tl.Lock()
	} else if !tl.TryLock() {
		return fmt.Errorf("checkout is in use")
	}
	defer tl.Unlock()
	path, err := p.checkTemporaryWorktree(ctx, r, workspace, expected, nil)
	if err != nil {
		return err
	}
	if certify != nil {
		if err := certify(); err != nil {
			return err
		}
	}
	if err := checkWorktreeFiles(ctx, path, expected.Dir); err != nil {
		return err
	}
	pinned, err := preserveWorktreeHistory(ctx, r, path, expected)
	if err != nil {
		return fmt.Errorf("retain %s: %w", expected.Dir, err)
	}
	if err := checkWorktreeFiles(ctx, path, expected.Dir); err != nil {
		return err
	}
	// Verify the unchanged instance, pinned history and current index under
	// the physical checkout lease, after the recovery writes and before Git.
	if _, err := p.checkTemporaryWorktree(ctx, r, workspace, expected, pinned); err != nil {
		return err
	}
	_, err = git(ctx, r.Path, "worktree", "remove", path)
	return err
}

func checkWorktreeFiles(ctx context.Context, path, dir string) error {
	// git worktree remove alone does not protect ignored files. Include every
	// unknown path and disable status configuration that hides untracked files.
	status, err := git(ctx, path, "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "status", "--porcelain=v1", "--untracked-files=all", "--ignored", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("retain %s: uncommitted, untracked or ignored files", dir)
	}
	// Index flags can hide tracked edits from status and worktree remove.
	entries, err := git(ctx, path, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(entries, "\x00") {
		if entry != "" && !strings.HasPrefix(entry, "H ") {
			return fmt.Errorf("retain %s: index flags may hide local work", dir)
		}
	}
	return nil
}
