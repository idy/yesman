package pool

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/idy/yesman/internal/yesmen"
)

// DesktopDiscardPreview samples process use before starting any parallel Git
// reads in the worktrees. Removal still scans afresh under its checkout lease.
func DesktopDiscardPreview(ctx context.Context, workspace string) context.Context {
	root := realPath(workspace)
	busy := map[string]bool{}
	err := visitDesktopOpenFiles(ctx, func(name string) bool {
		name = strings.TrimSuffix(name, " (deleted)")
		if filepath.IsAbs(name) && within(name, root) {
			rel, err := filepath.Rel(root, name)
			if err == nil && rel != "." {
				dir, _, _ := strings.Cut(rel, string(filepath.Separator))
				busy[dir] = true
			}
		}
		return false
	})
	return context.WithValue(ctx, discardPreviewKey{}, discardPreviewUse{root, busy, err})
}

type discardPreviewKey struct{}

type discardPreviewUse struct {
	root string
	busy map[string]bool
	err  error
}

func WorkspaceGitDeletionChecks() yesmen.LocalGitDeletionChecks {
	checks := yesmen.LocalGitDeletionChecks{
		Idle: func(ctx context.Context, workspace string) error {
			return checkDesktopWorktreeUse(ctx, workspace, false)
		},
	}
	if runtime.GOOS == "windows" {
		checks.Entry = checkDiscardSharing
	}
	return checks
}

func checkDesktopWorktreeUse(ctx context.Context, path string, preview bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root := realPath(path)
	inUse := &RemovalHold{Reason: "A process is using this worktree.", Detail: path}
	if snapshot, ok := ctx.Value(discardPreviewKey{}).(discardPreviewUse); ok && preview && filepath.Dir(root) == snapshot.root {
		if snapshot.err == nil && snapshot.busy[filepath.Base(root)] {
			return inUse
		}
		return snapshot.err
	}
	var held bool
	err := visitDesktopOpenFiles(ctx, func(name string) bool {
		name = strings.TrimSuffix(name, " (deleted)")
		if filepath.IsAbs(name) && (name == root || within(name, root)) {
			held = true
			return true
		}
		return false
	})
	if held {
		return inUse
	}
	return err
}
