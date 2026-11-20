package pool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
			dir, id, err := openInstanceRoot(r.Path, path, expected.CleanupID)
			if err != nil {
				return "", err
			}
			dir.Close()
			if !sameCleanupRoot(*expected.Root, id) {
				return "", &RemovalHold{"The checkout directory is missing or was replaced.", expected.Dir}
			}
		}
		return path, nil
	}
	return "", &RemovalHold{"Git has no current registration for this recorded directory.", expected.Dir}
}

// CheckoutObservation spans the preview and its one-use confirmation. With no
// birth time, holding the original directory open prevents inode reuse. These
// short-lived observations are never persisted or used as managed write grants.
type CheckoutObservation struct {
	Identity safefs.Identity
	pin      *safefs.Dir
}

func (o *CheckoutObservation) Close() {
	if o != nil && o.pin != nil {
		o.pin.Close()
		o.pin = nil
	}
}

func sameCleanupRoot(expected, actual safefs.Identity) bool {
	if expected.Ino == 0 || actual.Ino != expected.Ino {
		return false
	}
	if expected.HasBirth { // a recorded root never falls back to inode-only
		return expected.Usable() && actual.Usable() && expected.Birth == actual.Birth && (expected.Dev == 0 || expected.Dev == actual.Dev)
	}
	return !actual.Usable() && expected.Dev != 0 && expected.Dev == actual.Dev
}

// InspectTemporaryWorktree leaves refs and the directory intact. The caller
// closes the observation at confirmation, cancellation, replacement or expiry.
func (p *Pool) InspectTemporaryWorktree(ctx context.Context, workspace string, expected Worktree) (*CheckoutObservation, error) {
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
	dir, id, err := openInstanceRoot(r.Path, path, expected.CleanupID)
	if err != nil {
		return nil, err
	}
	o := &CheckoutObservation{Identity: id, pin: dir}
	if id.Ino == 0 || (!id.Usable() && id.Dev == 0) || (expected.Root != nil && !sameCleanupRoot(*expected.Root, id)) {
		o.Close()
		return nil, &RemovalHold{"The checkout directory is missing or was replaced.", expected.Dir}
	}
	if err := checkRemovalContents(ctx, r, path, expected); err != nil {
		o.Close()
		return nil, err
	}
	if _, err := readRecoverableHistory(ctx, r, path, expected); err != nil {
		o.Close()
		return nil, err
	}
	if id.Usable() {
		o.Close()
	} else {
		o.Identity.HasBirth, o.Identity.Birth = false, 0
	}
	return o, nil
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
	history, err := readRecoverableHistory(ctx, r, path, expected)
	if err != nil {
		return err
	}
	if err := preserveWorktreeHistory(ctx, r, expected, history); err != nil {
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
	latest, err := privateHistory(r.Path, path, expected)
	if err != nil {
		return err
	}
	if !slices.Equal(latest, history) {
		return &RemovalHold{"Git history changed. Review this worktree again.", expected.Dir}
	}
	_, err = git(ctx, r.Path, "worktree", "remove", path)
	return err
}
