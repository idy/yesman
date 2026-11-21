package pool

import (
	"context"
	"fmt"
	"maps"
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
	return p.WithWorktree(filepath.Join(workspace, expected.Dir), func() error {
		_, err := p.checkTemporaryWorktree(ctx, r, workspace, expected, nil)
		return err
	})
}

func (p *Pool) checkTemporaryWorktree(ctx context.Context, r Repo, workspace string, expected Worktree, pinned privateHistory) (string, error) {
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
	// Inspect this registration's inputs through verified handles before Git
	// reads them. Unexpected private state must survive the entire operation.
	history, _, err := inspectWorktreeHistory(ctx, r, path, expected)
	if err != nil {
		return "", fmt.Errorf("retain %s: %w", expected.Dir, err)
	}
	if pinned != nil && !maps.Equal(history, pinned) {
		return "", &RemovalHold{"Git history changed. Review this worktree again.", expected.Dir}
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
		if expected.observedTracking != nil {
			tracking, err := cleanupTrackingBranch(ctx, r, wt.Branch)
			if err != nil || tracking != *expected.observedTracking {
				return "", &RemovalHold{"The checkout changed. Refresh Worktrees and review it again.", expected.Dir}
			}
		}
		// Use the spelling from Git's own registration for its commands.
		// Windows Git may store a different path through a directory junction.
		return wt.Path, nil
	}
	return "", &RemovalHold{"Git has no current registration for this recorded directory.", expected.Dir}
}

// CheckoutObservation spans the preview and its one-use confirmation. With no
// birth time, holding the original directory open prevents inode reuse. These
// short-lived observations are never persisted or used as managed write grants.
type CheckoutObservation struct {
	Identity safefs.Identity
	Tracking string
	pin      *safefs.Dir
}

// Bind carries the locally observed directory and tracking branch through the
// confirmation. A different tracking ref needs its own open-PR verification.
func (o *CheckoutObservation) Bind(w Worktree) Worktree {
	w.Root = &o.Identity
	if !w.Detached {
		w.observedTracking = &o.Tracking
	}
	return w
}

func cleanupTrackingBranch(ctx context.Context, r Repo, branch string) (string, error) {
	ref := "refs/heads/" + branch
	out, err := git(ctx, r.Path, "for-each-ref", "--count=1", "--format=%(refname)%09%(upstream:remoteref)", ref)
	if err != nil {
		return "", err
	}
	if out == ref { // no tracking branch (git() trims the trailing tab)
		return "", nil
	}
	if tracking, ok := strings.CutPrefix(out, ref+"\trefs/heads/"); ok && tracking != "" {
		return tracking, nil
	}
	return "", &RemovalHold{Reason: "The tracking branch could not be verified.", Detail: branch}
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
	r, err := p.RecordedRepo(ctx, expected.Repo)
	if err != nil {
		return nil, err
	}
	l := p.lock(r.Name)
	l.Lock()
	defer l.Unlock()
	tl, err := p.treeLock(filepath.Join(workspace, expected.Dir))
	if err != nil {
		return nil, err
	}
	tl.RLock()
	defer tl.RUnlock()
	path, err := p.checkTemporaryWorktree(ctx, r, workspace, expected, nil)
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
	if !expected.Detached {
		o.Tracking, err = cleanupTrackingBranch(ctx, r, expected.Branch)
		if err != nil {
			o.Close()
			return nil, err
		}
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
	if err := checkRemovalContents(ctx, r, path, expected); err != nil {
		return err
	}
	pinned, err := preserveWorktreeHistory(ctx, r, path, expected)
	if err != nil {
		return fmt.Errorf("retain %s: %w", expected.Dir, err)
	}
	if err := checkRemovalContents(ctx, r, path, expected); err != nil {
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
