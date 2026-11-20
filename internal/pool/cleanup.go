package pool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/idy/yesman/internal/safefs"
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
	if !filepath.IsLocal(expected.Dir) || filepath.Base(expected.Dir) != expected.Dir || expected.Dir == "." || realPath(path) != filepath.Join(realPath(workspace), expected.Dir) {
		return "", &RemovalHold{"The directory is outside this workspace.", expected.Dir}
	}
	if st, err := os.Lstat(path); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", &RemovalHold{"The checkout directory is missing or was replaced.", expected.Dir}
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
		if realPath(filepath.Dir(wt.Path)) != realPath(workspace) || filepath.Base(wt.Path) != expected.Dir || wt.CleanupID != expected.CleanupID {
			return "", &RemovalHold{"The worktree instance changed or its private marker is missing.", expected.Dir}
		}
		if wt.Head != expected.Head || wt.Branch != expected.Branch || wt.Detached != expected.Detached {
			return "", &RemovalHold{"The checkout changed. Refresh Worktrees and review it again.", expected.Dir}
		}
		if wt.Branch != "" && r.DefaultBranch == "" {
			return "", &RemovalHold{"The repository’s default branch could not be verified.", wt.Branch}
		}
		if wt.Branch != "" && (wt.Branch == r.DefaultBranch || wt.Branch == "main" || wt.Branch == "master") {
			return "", &RemovalHold{"The primary branch is protected.", wt.Branch}
		}
		if expected.Root != nil {
			id, err := rootIdentity(r.Path, path, expected.CleanupID)
			if err != nil || id.Ino != expected.Root.Ino || id.Birth != expected.Root.Birth || (expected.Root.Dev != 0 && expected.Root.Dev != id.Dev) {
				return "", &RemovalHold{"The checkout directory is missing or was replaced.", expected.Dir}
			}
		}
		return path, nil
	}
	return "", &RemovalHold{"Git has no current registration for this recorded directory.", expected.Dir}
}

// InspectTemporaryWorktree checks the same Git and filesystem state as removal,
// leaving refs and the directory intact. The desktop confirmation compares the
// returned directory identity even when an older registration only has a nonce.
func (p *Pool) InspectTemporaryWorktree(ctx context.Context, workspace string, expected Worktree) (*safefs.Identity, error) {
	r, err := p.Get(expected.Repo)
	if err != nil {
		return nil, err
	}
	l := p.lock(r.Name)
	l.Lock()
	defer l.Unlock()
	tl := p.treeLock(filepath.Join(workspace, expected.Dir))
	tl.RLock()
	defer tl.RUnlock()
	path, err := p.checkTemporaryWorktree(ctx, r, workspace, expected)
	if err != nil {
		return nil, err
	}
	id, err := rootIdentity(r.Path, path, expected.CleanupID)
	if err != nil {
		return nil, err
	}
	return id, checkRemovalContents(ctx, r, path, expected)
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
	// Managed file writes into this checkout (WithWorktree) finish first;
	// none starts until the removal is decided.
	tl := p.treeLock(filepath.Join(workspace, expected.Dir))
	tl.Lock()
	defer tl.Unlock()
	path, err := p.checkTemporaryWorktree(ctx, r, workspace, expected)
	if err != nil {
		return err
	}
	if err := checkRemovalContents(ctx, r, path, expected); err != nil {
		return err
	}
	// A remote-tracking ref can disappear on the next fetch after a squash
	// merge. Pin this generation's source before removing its last checkout.
	if !cleanupIDRE.MatchString(expected.CleanupID) {
		return fmt.Errorf("retain %s: invalid checkout ID", expected.Dir)
	}
	if err := preserveWorktreeHistory(ctx, r, path, expected); err != nil {
		return err
	}
	checkpoint := "refs/yesman/worktrees/" + expected.CleanupID
	if old, err := git(ctx, r.Path, "show-ref", "--verify", "--hash", checkpoint); err == nil {
		if old != expected.Head {
			return fmt.Errorf("retain %s: recovery ref changed", expected.Dir)
		}
	} else if _, err := git(ctx, r.Path, "update-ref", "--no-deref", checkpoint, expected.Head, strings.Repeat("0", len(expected.Head))); err != nil {
		return fmt.Errorf("retain %s: cannot preserve exact HEAD: %w", expected.Dir, err)
	}
	// Re-read the actual private Git marker and source immediately before
	// deleting, under the same lock used by every pool checkout/removal path.
	if _, err := p.checkTemporaryWorktree(ctx, r, workspace, expected); err != nil {
		return err
	}
	if pinned, err := git(ctx, r.Path, "show-ref", "--verify", "--hash", checkpoint); err != nil || pinned != expected.Head {
		return &RemovalHold{"The exact commit could not be saved in the recovery reference.", checkpoint}
	}
	// Check contents again after the recovery writes. For instance, a build
	// may have produced an ignored file while Git was preserving a reflog.
	if err := checkRemovalContents(ctx, r, path, expected); err != nil {
		return err
	}
	if _, err := p.checkTemporaryWorktree(ctx, r, workspace, expected); err != nil {
		return err
	}
	_, err = git(ctx, r.Path, "worktree", "remove", path)
	return err
}
