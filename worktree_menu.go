package main

import (
	"context"
	"time"

	"github.com/egoist/mygo"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/server"
)

// WorktreeMenu returns an action for the worktree row or the section heading.
// Like the other sidebar menus it uses the caller's native window.
func (s *service) WorktreeMenu(ctx context.Context, row bool, x, y int) (string, error) {
	return popup(ctx, x, y, func(item func(string, string, ...func(*mygo.MenuItem)) *mygo.MenuItem) []*mygo.MenuItem {
		return worktreeMenuItems(row, item)
	})
}

func worktreeMenuItems(row bool, item func(string, string, ...func(*mygo.MenuItem)) *mygo.MenuItem) []*mygo.MenuItem {
	if !row {
		return []*mygo.MenuItem{item("cleanup", tr("Clean up worktrees…"))}
	}
	return []*mygo.MenuItem{item("reveal", revealLabel()), mygo.Separator(), item("remove", tr("Remove Worktree…"))}
}

func (s *service) PreviewWorktreeCleanup(ctx context.Context, name string, since time.Time, row *pool.Worktree, keep []string) (server.WorktreeCleanupPreview, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return s.app.desktopWorktrees.Preview(ctx, name, since, row, keep)
}

func (s *service) CancelWorktreeCleanup(token string) { s.app.desktopWorktrees.Cancel(token) }

func (s *service) ConfirmWorktreeCleanup(ctx context.Context, name string, since time.Time, token string, keep []string) ([]server.WorktreeCleanupRow, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return s.app.desktopWorktrees.Confirm(ctx, name, since, token, keep)
}
