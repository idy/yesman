package pool

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
)

// DesktopDiscardPreview shares one process listing across a single preview.
// Removal obtains its own fresh listing while holding the checkout lease.
func DesktopDiscardPreview(ctx context.Context, workspace string) context.Context {
	root := realPath(workspace)
	check := sync.OnceValues(func() (map[string]bool, error) {
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
		return busy, err
	})
	return context.WithValue(ctx, discardPreviewKey{}, discardPreviewUse{root, check})
}

type discardPreviewKey struct{}

type discardPreviewUse struct {
	root string
	open func() (map[string]bool, error)
}

func checkDesktopWorktreeUse(ctx context.Context, path string, preview bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root := realPath(path)
	inUse := &RemovalHold{Reason: "A process is using this worktree.", Detail: path}
	if snapshot, ok := ctx.Value(discardPreviewKey{}).(discardPreviewUse); ok && preview && filepath.Dir(root) == snapshot.root {
		busy, err := snapshot.open()
		if err == nil && busy[filepath.Base(root)] {
			return inUse
		}
		return err
	}
	var held bool
	err := visitDesktopOpenFiles(ctx, func(name string) bool {
		name = strings.TrimSuffix(name, " (deleted)")
		held = filepath.IsAbs(name) && (name == root || within(name, root))
		return held
	})
	if held {
		return inUse
	}
	return err
}
