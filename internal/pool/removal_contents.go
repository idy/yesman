package pool

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// RemovalHold is a reason the desktop can translate. A detail, such as a
// filename, stays in its original spelling.
type RemovalHold struct{ Reason, Detail string }

func (e *RemovalHold) Error() string {
	if e.Detail != "" {
		return e.Reason + " " + e.Detail
	}
	return e.Reason
}

func checkRemovalContents(ctx context.Context, repo Repo, path string, expected Worktree) error {
	common, err := git(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || realPath(common) != realPath(repo.Path) {
		return &RemovalHold{"The Git directory points to a different shared repository.", common}
	}
	top, err := git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil || realPath(top) != realPath(path) {
		return &RemovalHold{"Git reports a different checkout directory.", top}
	}
	// git worktree remove on its own can discard ignored files. Neither the
	// panel's diff nor settings that hide worktree changes are a clean check.
	status, err := git(ctx, path, "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if status != "" {
		return &RemovalHold{"There are uncommitted changes, untracked files or ignored files.", ""}
	}
	entries, err := git(ctx, path, "ls-files", "-v", "--stage", "-z")
	if err != nil {
		return err
	}
	// Git does not list special files or empty directories in porcelain
	// status. Walk without following links and account for every entry.
	known := map[string]bool{".": true, ".git": false}
	for entry := range strings.SplitSeq(entries, "\x00") {
		if entry == "" {
			continue
		}
		header, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 4 || fields[0] != "H" || fields[3] != "0" {
			return &RemovalHold{"Index flags or conflicts may hide local changes.", name}
		}
		switch fields[1] {
		case "100644", "100755", "120000":
		case "160000":
			return &RemovalHold{"The checkout contains a submodule.", name}
		default:
			return &RemovalHold{"The index contains an unknown file type.", name}
		}
		name = filepath.FromSlash(name)
		known[name] = false
		for parent := filepath.Dir(name); !known[parent]; parent = filepath.Dir(parent) {
			known[parent] = true
		}
	}
	err = filepath.WalkDir(path, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(path, file)
		if err != nil {
			return err
		}
		dir, found := known[rel]
		if !found || dir != entry.IsDir() || !(dir || entry.Type().IsRegular() || entry.Type() == os.ModeSymlink) {
			return &RemovalHold{"The directory contains an item outside the Git checkout.", rel}
		}
		return nil
	})
	if err != nil {
		return err
	}
	checkpoint := "refs/yesman/worktrees/" + expected.CleanupID
	if target, err := git(ctx, repo.Path, "symbolic-ref", "--quiet", checkpoint); err == nil {
		return &RemovalHold{"The recovery reference points elsewhere.", target}
	}
	if previous, err := git(ctx, repo.Path, "show-ref", "--verify", "--hash", checkpoint); err == nil && previous != expected.Head {
		return &RemovalHold{"The recovery reference already preserves another commit.", previous}
	}
	return nil
}
