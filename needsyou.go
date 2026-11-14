package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// Needs you: what waits on the user across every yesman. The page builds it
// from the agents (a lead's own dialog, a worker's dialog its lead handed
// over) and from each yesman's handoffs, which State serves. The user closes
// a handoff here; its lead hears of it.

// ResolveHandoff closes a handoff for the user: resolution "done" (they did
// it) or "not-needed". hiredAt is the yesman the page showed it for, so a
// stale window never closes a newer yesman's handoff of the same name.
func (s *service) ResolveHandoff(yesman string, hiredAt time.Time, id, resolution string) (yesmen.Handoff, error) {
	if resolution != yesmen.HandoffDone && resolution != yesmen.HandoffNotNeeded {
		return yesmen.Handoff{}, fmt.Errorf("%q is not a way to close a handoff", resolution)
	}
	a := s.app
	var (
		h     yesmen.Handoff
		since time.Time
	)
	err := a.yesmen.Use(yesman, func(y yesmen.Yesman) error {
		if !y.HiredAt.Equal(hiredAt) {
			return errors.New("this yesman was replaced: refresh the window")
		}
		since = y.HiredAt
		var err error
		h, err = a.yesmen.CloseHandoff(y.Name, id, resolution, "user", "")
		return err
	})
	if err != nil {
		return yesmen.Handoff{}, err
	}
	a.needsYouChanged(yesman, nil)
	if a.agents != nil {
		msg := handoffNotice(h)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := a.agents.TellLeadAboutUser(ctx, yesman, since, msg); err != nil {
				log.Printf("yesman: tell %s's lead about %s: %v", yesman, h.ID, err)
			}
		}()
	}
	return h, nil
}

// ResetCallMemory starts a new memory of a yesman's tool calls, once the
// user confirmed it under Needs you: the fingerprints of the tool calls of
// handoffs no longer kept are forgotten, so that new tool calls register
// again, and its lead hears of it. Only the user's window calls it, never a
// lead's tool. hiredAt and the counts are what the page showed: once the
// yesman was replaced, or its memory reset or changed since, nothing is
// forgotten and the page shows it again.
func (s *service) ResetCallMemory(yesman string, hiredAt time.Time, epoch, used, forgettable int) (yesmen.CallMemory, error) {
	a := s.app
	var (
		m       yesmen.CallMemory
		cleared int
		since   time.Time
	)
	err := a.yesmen.Use(yesman, func(y yesmen.Yesman) error {
		if !y.HiredAt.Equal(hiredAt) {
			return errors.New("this yesman was replaced: refresh the window")
		}
		since = y.HiredAt
		var err error
		m, cleared, err = a.yesmen.ResetCallMemory(y.Name, hiredAt, epoch, used, forgettable)
		return err
	})
	if err != nil {
		return yesmen.CallMemory{}, err
	}
	a.needsYouChanged(yesman, nil)
	if a.agents != nil {
		msg := fmt.Sprintf("[yesman] The user started a new tool-call memory for Needs you: %d older tool-call records were forgotten, and add takes new tool calls again. Register again what was refused for the memory, if the user still needs to do it.", cleared)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := a.agents.TellLeadAboutUser(ctx, yesman, since, msg); err != nil {
				log.Printf("yesman: tell %s's lead about its new tool-call memory: %v", yesman, err)
			}
		}()
	}
	return m, nil
}

// handoffNotice is what the lead hears when the user closes a handoff.
func handoffNotice(h yesmen.Handoff) string {
	what := fmt.Sprintf("%s (%s) %q", h.ID, h.Type, h.Title)
	if h.Task != "" {
		what += ", for task " + h.Task
	}
	if h.Resolution == yesmen.HandoffNotNeeded {
		return fmt.Sprintf("[yesman] The user marked %s as not needed. Go on without it.", what)
	}
	switch h.Type {
	case yesmen.HandoffDecision, yesmen.HandoffInput:
		return fmt.Sprintf("[yesman] The user marked %s as done. Their answer, if they gave one, is in their messages to you.", what)
	default:
		return fmt.Sprintf("[yesman] The user marked %s as done. That is their own confirmation: check it worked before relying on it.", what)
	}
}

// needsYouChanged refreshes the window and the tray, and tells the user of a
// handoff a lead added.
func (a *app) needsYouChanged(yesman string, added *yesmen.Handoff) {
	a.countHandoffs(yesman)
	a.notifyChanged()
	if a.agents != nil {
		a.agentsChanged(a.agents.List())
	}
	if added != nil {
		a.notify(agent.ID(yesman, yesmen.LeadName), tr("%s needs you", a.yesmanLabel(yesman)), added.Title)
	}
}

// countHandoffs updates the open handoffs counted for the tray, so that
// agentsChanged never reads workspaces, and shows the count: on startup, the
// handoffs kept from before count at once, without notifying the user again
// or starting a lead. With no yesman named, all are read.
func (a *app) countHandoffs(names ...string) {
	if len(names) == 0 {
		ys, _ := a.yesmen.List()
		for _, y := range ys {
			names = append(names, y.Name)
		}
		a.smu.Lock()
		a.handoffs = map[string]int{}
		a.smu.Unlock()
	}
	for _, name := range names {
		n := 0
		if hs, err := a.yesmen.Handoffs(name); err == nil {
			n = len(hs.Open())
		}
		a.smu.Lock()
		if a.handoffs == nil {
			a.handoffs = map[string]int{}
		}
		if n == 0 {
			delete(a.handoffs, name)
		} else {
			a.handoffs[name] = n
		}
		a.waiting = a.agentWaiting + a.openHandoffsLocked()
		a.smu.Unlock()
	}
	a.showCounts()
}

// openHandoffsLocked is the number counted; a.smu must be held.
func (a *app) openHandoffsLocked() int {
	n := 0
	for _, c := range a.handoffs {
		n += c
	}
	return n
}
