package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// A worker command belongs to one hire and one worker record. An authenticated
// lead keeps its process's cancellable operation lease. Retirement waits for
// that lease, so its commands can use the pinned hire directly during startup,
// delivery and cleanup, without reacquiring Store.Use while Delete drains them.
type workerCommand struct {
	m        *Manager
	hire     yesmen.Yesman
	name, id string
	since    time.Time
	target   *agent
	admitted bool
}

func (m *Manager) bindWorker(ctx context.Context, hire yesmen.Yesman, name string, since time.Time, admitted bool) (*workerCommand, error) {
	c := &workerCommand{m: m, hire: hire, name: name, id: ID(hire.Name, name), since: since, admitted: admitted}
	err := m.cfg.Yesmen.WithWorker(hire, name, func(w yesmen.Worker) error {
		if !w.StartedAt.Equal(since) {
			return ErrRetired
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed || ctx.Err() != nil {
			return ErrRetired
		}
		var err error
		c.target, err = m.workspaceAgentLocked(hire, Info{ID: c.id, Yesman: hire.Name, Name: name, Role: RoleWorker, Since: since})
		return err
	})
	return c, err
}

func (c *workerCommand) currentLocked(ctx context.Context) bool {
	a := c.target
	return ctx.Err() == nil && !c.m.closed && c.m.agents[c.id] == a && !a.removing && !a.retiring && a.workspaceSince.Equal(c.hire.HiredAt) && a.info.Since.Equal(c.since)
}

func (c *workerCommand) withHire(fn func(yesmen.Yesman) error) error {
	if c.admitted {
		return fn(c.hire)
	}
	return c.m.cfg.Yesmen.Use(c.hire.Name, func(y yesmen.Yesman) error {
		if !y.HiredAt.Equal(c.hire.HiredAt) {
			return ErrRetired
		}
		return fn(y)
	})
}

func (c *workerCommand) ensure(ctx context.Context) (*agent, *proc, error) {
	var a *agent
	var p *proc
	err := c.withHire(func(y yesmen.Yesman) error {
		var err error
		a, p, err = c.m.ensureInCtx(ctx, y, c.id, c.name, false, true, c, nil)
		return err
	})
	return a, p, err
}

func (c *workerCommand) info() (Info, error) {
	i, err := c.m.Get(c.id)
	if err == nil && !i.Since.Equal(c.since) {
		return Info{}, ErrRetired
	}
	return i, err
}

func (c *workerCommand) prompt(ctx context.Context, text, behavior, kind string) error {
	if kind == "message" {
		// A lead's message reserves the worker's inbox as soon as it is on
		// its way. A late report cannot close the worker during its startup.
		c.m.mu.Lock()
		if !c.currentLocked(ctx) {
			c.m.mu.Unlock()
			return ErrRetired
		}
		c.target.inbox++
		c.m.mu.Unlock()
	}
	track := &sendTrack{worker: c, bounded: true, admit: func() bool { return c.currentLocked(ctx) }, current: func() error {
		c.m.mu.Lock()
		defer c.m.mu.Unlock()
		if !c.currentLocked(ctx) {
			return ErrRetired
		}
		return nil
	}}
	return c.m.promptTracked(ctx, c.id, text, behavior, "", nil, &c.hire.HiredAt, Origin{Source: OriginLead, Kind: kind}, track)
}

func (c *workerCommand) runAs(ctx context.Context, as Choice) error {
	a, p, err := c.ensure(ctx)
	if err == nil {
		_, err = c.m.configureProcess(ctx, a, p, map[string]any{"type": "set_model", "provider": as.Provider, "modelId": as.Model})
	}
	if err == nil && as.Effort != "" {
		_, err = c.m.configureProcess(ctx, a, p, map[string]any{"type": "set_thinking_level", "level": as.Effort})
	}
	if err != nil {
		return fmt.Errorf("run as %s: %w", as, err)
	}
	return nil
}

func (c *workerCommand) remove() error {
	return c.withHire(func(yesmen.Yesman) error { return c.m.removeWorkerIn(c) })
}

// StartWorkerBy admits a lead after the caller's crew lookup has completed.
func (m *Manager) StartWorkerBy(ctx context.Context, token string, lead Info, hire yesmen.Yesman, name, title, task string, as Choice) (Info, error) {
	ctx, finish, err := m.beginLeadCommand(ctx, token, lead, hire)
	if err != nil {
		return Info{}, err
	}
	defer finish()
	return m.startWorkerRecord(ctx, hire, name, title, task, as, true)
}

func (m *Manager) beginLeadCommand(ctx context.Context, token string, lead Info, hire yesmen.Yesman) (context.Context, func(), error) {
	if lead.Role != RoleLead || lead.Name != yesmen.LeadName || lead.Yesman != hire.Name || lead.ID != ID(hire.Name, yesmen.LeadName) {
		return nil, nil, ErrRetired
	}
	return m.BeginOperation(ctx, token, lead, hire.HiredAt)
}

// manageWorkerBy binds a previously selected worker and its lead together.
// The caller's operation ends only after fn finishes with that exact target.
func (m *Manager) manageWorkerBy(ctx context.Context, token string, lead Info, hire yesmen.Yesman, worker Info, fn func(context.Context, *workerCommand) error) error {
	if worker.Role != RoleWorker || worker.Name == yesmen.LeadName || worker.Yesman != hire.Name || worker.ID != ID(hire.Name, worker.Name) {
		return ErrRetired
	}
	ctx, finish, err := m.beginLeadCommand(ctx, token, lead, hire)
	if err != nil {
		return err
	}
	defer finish()
	command, err := m.bindWorker(ctx, hire, worker.Name, worker.Since, true)
	if err != nil {
		return err
	}
	return fn(ctx, command)
}

func (m *Manager) SendWorkerBy(ctx context.Context, token string, lead Info, hire yesmen.Yesman, worker Info, text string) error {
	return m.manageWorkerBy(ctx, token, lead, hire, worker, func(ctx context.Context, c *workerCommand) error { return c.prompt(ctx, text, "steer", "message") })
}
func (m *Manager) RemoveWorkerBy(ctx context.Context, token string, lead Info, hire yesmen.Yesman, worker Info) error {
	return m.manageWorkerBy(ctx, token, lead, hire, worker, func(_ context.Context, c *workerCommand) error { return c.remove() })
}
