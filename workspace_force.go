package main

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

// Bound and short-lived. A late result from a dismissed dialog cannot replace
// another dialog's confirmation. Force consumes only the supplied token.
type workspaceForceConfirmations struct {
	mu      sync.Mutex
	pending map[string]workspaceForceConfirmation
	reviews map[string]context.CancelFunc
}

type workspaceForceConfirmation struct {
	plan    *yesmen.ForceDeletion
	expires time.Time
}

const workspaceForceTTL = 5 * time.Minute

func (c *workspaceForceConfirmations) issue(plan *yesmen.ForceDeletion) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for key, value := range c.pending {
		if !now.Before(value.expires) {
			delete(c.pending, key)
		}
	}
	if len(c.pending) >= 32 {
		return ""
	}
	if c.pending == nil {
		c.pending = make(map[string]workspaceForceConfirmation)
	}
	token := rand.Text()
	c.pending[token] = workspaceForceConfirmation{plan, now.Add(workspaceForceTTL)}
	return token
}

func (c *workspaceForceConfirmations) consume(token string) *yesmen.ForceDeletion {
	c.mu.Lock()
	defer c.mu.Unlock()
	value := c.pending[token]
	delete(c.pending, token)
	if time.Now().Before(value.expires) {
		return value.plan
	}
	return nil
}

type WorkspaceDeletionCheck struct {
	yesmen.CheckoutScan
	Token string `json:"token,omitempty"`
}

// WorkspaceCheckouts runs only on demand in the Worktrees panel. Its rows
// never pass through the State inventory, file grants or worktree cleanup.
func (s *service) WorkspaceCheckouts(ctx context.Context, name string, hiredAt time.Time) (yesmen.CheckoutScan, error) {
	target, err := s.app.yesmen.PrepareDeletion(name, hiredAt)
	if err != nil {
		return yesmen.CheckoutScan{}, workspaceDeletionError(err)
	}
	out, err := s.app.yesmen.InspectCheckouts(ctx, target)
	return out, workspaceDeletionError(err)
}

// These methods are exposed exclusively by the desktop bridge, alongside
// ordinary workspace deletion. No worker tool or loopback route uses them.
func (s *service) ReviewWorkspaceForceDeletion(ctx context.Context, target yesmen.DeletionTarget, request string) (WorkspaceDeletionCheck, error) {
	if len(request) != 36 {
		return WorkspaceDeletionCheck{}, errors.New("invalid workspace deletion review request")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := &s.app.forceDeletion
	c.mu.Lock()
	if c.reviews[request] != nil || len(c.reviews) >= 4 {
		c.mu.Unlock()
		return WorkspaceDeletionCheck{}, errors.New(tr("Too many deletion confirmations are open. Close one and try again."))
	}
	if c.reviews == nil {
		c.reviews = make(map[string]context.CancelFunc)
	}
	c.reviews[request] = cancel
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.reviews, request)
		c.mu.Unlock()
	}()
	return s.CheckWorkspaceForceDeletion(ctx, target)
}

func (s *service) CancelWorkspaceForceReview(request string) {
	c := &s.app.forceDeletion
	c.mu.Lock()
	defer c.mu.Unlock()
	if cancel := c.reviews[request]; cancel != nil {
		cancel()
	}
}

func (s *service) CheckWorkspaceForceDeletion(ctx context.Context, target yesmen.DeletionTarget) (WorkspaceDeletionCheck, error) {
	a := s.app
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	scan, plan, err := a.yesmen.PrepareForceDeletion(ctx, target, a.pool.CheckWorkspaceDeletion, a.pool.RelocatedPrimary, pool.WorkspaceGitDeletionChecks())
	out := WorkspaceDeletionCheck{CheckoutScan: scan}
	if err != nil {
		out.Problem = workspaceDeletionError(err).Error()
	} else if plan != nil && ctx.Err() == nil {
		out.Token = a.forceDeletion.issue(plan)
		if out.Token == "" {
			out.Problem = tr("Too many deletion confirmations are open. Close one and try again.")
		}
	} else if ctx.Err() != nil && out.Problem == "" {
		out.Problem = ctx.Err().Error()
	}
	return out, nil
}

func (s *service) CancelWorkspaceForceDeletion(token string) { s.app.forceDeletion.consume(token) }

func (s *service) ForceDeleteWorkspace(ctx context.Context, target yesmen.DeletionTarget, token string) error {
	plan := s.app.forceDeletion.consume(token)
	if plan == nil {
		return errors.New(tr("This Force Delete confirmation expired or was used. Check the workspace again."))
	}
	return s.deleteWorkspace(ctx, target, plan)
}
