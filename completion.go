package main

import (
	"context"
	"sync"
	"time"

	"github.com/idy/yesman/internal/agent"
)

// completionNotices waits for uninterrupted quiet. The timer and any delivery
// it started share a lifetime, bounded by both the Lead's run and crew activity.
type completionNotices struct {
	mu     sync.Mutex
	states map[string]*completionState
}

type completionState struct {
	since  time.Time
	run    context.Context
	worked bool
	timer  *time.Timer
	cancel context.CancelFunc
}

func (s *completionState) stop() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

// update follows the whole snapshot. Starting a process to read its history
// is passive. A real settlement also identifies work when status updates are
// coalesced, including when the first snapshot is already idle.
func (c *completionNotices) update(list []agent.Info, notify func(context.Context, agent.Info)) {
	type workspace struct {
		lead   agent.Info
		busy   bool
		worked bool
	}
	groups := map[string]*workspace{}
	for _, i := range list {
		g := groups[i.Yesman]
		if g == nil {
			g = &workspace{}
			groups[i.Yesman] = g
		}
		if i.Role == agent.RoleLead {
			g.lead = i
		}
		switch i.Status {
		case agent.Working, agent.Waiting:
			g.worked, g.busy = true, true
		case agent.Starting:
			g.busy = true
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.states == nil {
		c.states = map[string]*completionState{}
	}
	for name, s := range c.states {
		if groups[name] == nil {
			s.stop()
			delete(c.states, name)
		}
	}
	for name, g := range groups {
		lead := g.lead
		run := lead.SettledRun()
		if lead.Status == agent.Idle && run.Err() != nil {
			continue // a snapshot of a run the Manager already retired
		}
		s := c.states[name]
		if s == nil || !s.since.Equal(lead.Since) {
			if s != nil {
				s.stop()
			}
			s = &completionState{since: lead.Since}
			c.states[name] = s
		}
		if lead.Status == agent.Idle && s.run != run && run != context.Background() {
			// A real settlement identifies new work, even if the intervening
			// busy snapshot was coalesced. A passive startup has no such run.
			s.stop()
			s.run, s.worked = run, true
		}
		s.worked = s.worked || g.worked
		if g.busy || lead.ID == "" || lead.Status != agent.Idle {
			s.stop()
			continue
		}
		if !s.worked || s.timer != nil {
			continue
		}
		ctx, cancel := context.WithCancel(run)
		s.cancel = cancel
		s.timer = time.AfterFunc(settleGrace, func() {
			defer cancel()
			c.mu.Lock()
			if ctx.Err() != nil {
				c.mu.Unlock()
				return
			}
			s.timer, s.worked = nil, false
			c.mu.Unlock()
			notify(ctx, lead)
		})
	}
}
