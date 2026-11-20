package pool

import (
	"context"
	"slices"
	"strings"
)

// Git removes the checkout's private HEAD reflog with the directory. A reset
// or a trip through detached HEAD can leave a commit reachable only from that
// log. Pin those commits too, under a separate namespace so the main checkpoint
// always names the exact HEAD the user saw. A refusal can leave recovery refs.
func preserveWorktreeHistory(ctx context.Context, repo Repo, path string, wt Worktree) error {
	log, err := git(ctx, path, "reflog", "show", "--format=%H", "HEAD")
	if err != nil {
		return err
	}
	seen := map[string]bool{wt.Head: true}
	var heads []string
	for oid := range strings.FieldsSeq(log) {
		if !seen[oid] {
			seen[oid] = true
			heads = append(heads, oid)
		}
	}
	// Keep each Git command within the platform's argument limit. Usually
	// all previous heads are ancestors of HEAD and nothing is written here.
	for batch := range slices.Chunk(heads, 64) {
		args := append([]string{"rev-list"}, batch...)
		args = append(args, "--not", wt.Head, "--branches", "--tags", "--glob=refs/yesman/*")
		unanchored, err := git(ctx, repo.Path, args...)
		if err != nil {
			return err
		}
		for oid := range strings.FieldsSeq(unanchored) {
			if !slices.Contains(batch, oid) {
				continue
			}
			ref := "refs/yesman/worktree-history/" + wt.CleanupID + "/" + oid
			if _, err := git(ctx, repo.Path, "symbolic-ref", "--quiet", ref); err == nil {
				return &RemovalHold{"The recovery reference points elsewhere.", ref}
			}
			if _, err := git(ctx, repo.Path, "update-ref", "--no-deref", ref, oid, strings.Repeat("0", len(oid))); err != nil {
				return err
			}
		}
	}
	return nil
}
