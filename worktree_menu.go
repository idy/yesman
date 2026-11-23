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

// PrepareWorktreeCleanup reserves a one-use check ticket before the frontend
// sends the slower Check call through the concurrently dispatched bridge.
func (s *service) PrepareWorktreeCleanup(name string, since time.Time, row *pool.Worktree) (string, error) {
	return s.app.desktopWorktrees.Prepare(name, since, row)
}

// CheckWorktreeCleanup lets the dialog cancel an in-flight check before a
// confirmation token exists.
func (s *service) CheckWorktreeCleanup(ctx context.Context, name string, since time.Time, row *pool.Worktree, keep []string, request string) (server.WorktreeCleanupPreview, error) {
	return s.app.desktopWorktrees.PreviewRequest(ctx, name, since, row, keep, request)
}

func (s *service) CancelWorktreeCleanup(token string) { s.app.desktopWorktrees.Cancel(token) }

func (s *service) ConfirmWorktreeCleanup(ctx context.Context, name string, since time.Time, token string, keep []string) ([]server.WorktreeCleanupRow, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return s.app.desktopWorktrees.Confirm(ctx, name, since, token, keep)
}
