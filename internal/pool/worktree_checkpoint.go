package pool

import (
	"context"
	"fmt"
)

// HasWorktreeInstance also checks for this nonce at a different registered
// path. A Git move must not look like a completed removal of the old path.
func (p *Pool) HasWorktreeInstance(ctx context.Context, repo, id string) (bool, error) {
	if !cleanupIDRE.MatchString(id) {
		return false, fmt.Errorf("invalid checkout ID")
	}
	r, err := p.RecordedRepo(ctx, repo)
	if err != nil {
		return false, err
	}
	l := p.lock(r.Name)
	if !l.TryLock() {
		return false, fmt.Errorf("repository is in use")
	}
	defer l.Unlock()
	wts, err := p.worktrees(ctx, r)
	if err != nil {
		return false, err
	}
	for _, w := range wts {
		if w.CleanupID == id {
			return true, nil
		}
	}
	return false, nil
}

// CheckWorktreeCheckpoint distinguishes a completed Git removal whose final
// registry write was interrupted from a checkout that simply went missing.
func (p *Pool) CheckWorktreeCheckpoint(ctx context.Context, wt Worktree) error {
	if !cleanupIDRE.MatchString(wt.CleanupID) {
		return fmt.Errorf("invalid checkout ID")
	}
	r, err := p.RecordedRepo(ctx, wt.Repo)
	if err != nil {
		return err
	}
	ref := "refs/yesman/worktrees/" + wt.CleanupID
	if _, err := git(ctx, r.Path, "symbolic-ref", "--quiet", ref); err == nil {
		return fmt.Errorf("symbolic recovery checkpoint for %s", wt.Dir)
	}
	head, err := git(ctx, r.Path, "rev-parse", "--verify", ref)
	if err != nil || head != wt.Head {
		return fmt.Errorf("missing or changed recovery checkpoint for %s", wt.Dir)
	}
	_, err = git(ctx, r.Path, "cat-file", "-e", head+"^{commit}")
	return err
}
