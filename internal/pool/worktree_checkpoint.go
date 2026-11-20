package pool

import (
	"context"
	"fmt"
)

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
