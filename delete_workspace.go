package main

import (
	"context"
	"errors"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

// Only the window has this bridge. The agents' loopback API has no deletion
// action. Preparing a confirmation or opening the archive list is read-only.
func (s *service) PrepareWorkspaceDeletion(name string, hiredAt time.Time) (yesmen.DeletionTarget, error) {
	t, err := s.app.yesmen.PrepareDeletion(name, hiredAt)
	return t, workspaceDeletionError(err)
}

func (s *service) StoredWorkspaces() ([]yesmen.DeletionTarget, error) {
	return s.app.yesmen.StoredWorkspaces()
}

func (s *service) DeleteWorkspace(ctx context.Context, target yesmen.DeletionTarget) error {
	a := s.app
	if a.schedules != nil {
		finish, err := a.schedules.BeginStorageChange()
		if err != nil {
			return err
		}
		defer finish()
	}
	detached, err := a.yesmen.Delete(ctx, target, a.pool.CheckWorkspaceDeletion, func(y yesmen.Yesman) error {
		stop, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := a.agents.StopYesmanContext(stop, y.Name); err != nil {
			return err
		}
		if a.schedules != nil {
			a.schedules.RetireYesman(y.Name, y.HiredAt)
		}
		return nil
	})
	if detached {
		a.agents.ForgetYesman(target.Entry, target.HiredAt)
		a.countHandoffs(target.Entry)
		a.agentsChanged(a.agents.List())
	}
	if detached || err == nil {
		a.notifyChanged()
	}
	return workspaceDeletionError(err)
}

func workspaceDeletionError(err error) error {
	if err == nil || i18n.Locale() != "zh-Hans" {
		return err
	}
	var issue *yesmen.DeletionIssue
	var tree *pool.RegisteredWorktreeError
	switch {
	case errors.As(err, &issue):
		return errors.New(issue.Text(tr))
	case errors.As(err, &tree):
		return errors.New(tr("%s has a registered worktree at %s. Finish its managed cleanup or move the checkout with Git first.", tree.Repo, tree.Path))
	default:
		return errors.New(tr(err.Error()))
	}
}
