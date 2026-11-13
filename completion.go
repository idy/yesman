package main

import (
	"sync"
	"time"

	"github.com/idy/yesman/internal/agent"
)

// completionNotices waits for an uninterrupted quiet interval before saying
// that a yesman finished. Work resuming invalidates the old timer, including
// a callback that already started and is waiting for the mutex.
type completionNotices struct {
	mu     sync.Mutex
	states map[string]*completionState
}

type completionState struct {
	worked   bool
	revision uint64
	timer    *time.Timer
}

// update follows the whole snapshot. Merely starting a process to read its
// history is not work; at least one agent must have worked or asked a
// question before the yesman can finish.
func (c *completionNotices) update(list []agent.Info, notify func(agent.Info)) {
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
		if _, ok := groups[name]; !ok {
			if s.timer != nil {
				s.timer.Stop()
			}
			delete(c.states, name)
		}
	}
	for name, g := range groups {
		s := c.states[name]
		if s == nil {
			s = &completionState{}
			c.states[name] = s
		}
		s.worked = s.worked || g.worked
		if g.busy || g.lead.ID == "" || g.lead.Status != agent.Idle {
			s.revision++
			if s.timer != nil {
				s.timer.Stop()
				s.timer = nil
			}
			continue
		}
		if !s.worked || s.timer != nil {
			continue
		}
		revision, lead := s.revision, g.lead
		s.timer = time.AfterFunc(settleGrace, func() {
			c.mu.Lock()
			if c.states[name] != s || s.revision != revision {
				c.mu.Unlock()
				return
			}
			s.timer, s.worked = nil, false
			c.mu.Unlock()
			notify(lead)
		})
	}
}
