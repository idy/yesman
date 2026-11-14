package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// PromotionResult separates a committed priority change from the message's
// current state: pi may already have taken it by the time the reply arrives.
type PromotionResult struct {
	Delivery Delivery `json:"delivery"`
	Outcome  string   `json:"outcome"`
}

// PromoteSubmission moves one existing occurrence. It neither creates an intent
// nor sends payload, starts pi, aborts a run, or replays either queue. The live
// runtime authorizes active steering admission or its own serial idle run.
func (m *Manager) PromoteSubmission(ctx context.Context, agentID, id string, since time.Time) (PromotionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err := m.Get(agentID)
	if err != nil {
		return PromotionResult{}, err
	}
	if !info.Since.Equal(since) {
		return PromotionResult{}, errors.New("the agent was replaced; reopen its conversation")
	}
	y, release, err := m.cfg.Yesmen.BeginUse(info.Yesman)
	if err != nil {
		return PromotionResult{}, err
	}
	defer release()
	m.mu.Lock()
	a := m.agents[agentID]
	if a == nil || !m.recallOwnerLocked(a, since, y.HiredAt) {
		m.mu.Unlock()
		return PromotionResult{}, errStopped
	}
	d, exists := a.deliveries[id]
	p := a.proc
	if !exists || p == nil || a.nativeQueue.Capability < 2 || !a.deliveryView(d).NativePending || d.Session != p.deliverySession() {
		m.mu.Unlock()
		return PromotionResult{}, errors.New("this message has no supported native pending occurrence")
	}
	if d.State != "queued" && d.State != "steering" {
		m.mu.Unlock()
		return PromotionResult{}, errors.New("this message cannot be promoted")
	}
	if d.State == "queued" && !a.nativeQueue.PromoteAllowed {
		m.mu.Unlock()
		return PromotionResult{}, errors.New("pi cannot advance this queue in its current session state")
	}
	command := map[string]any{"type": "promote_queued_message", "epoch": d.queueEpoch, "queueId": d.queueID,
		"expectedRevision": d.queueRevision, "controlGeneration": a.nativeQueue.ControlGeneration}
	a.recalls++ // the same retirement/cleanup reservation as selective cancellation
	m.mu.Unlock()
	release()
	defer func() { m.mu.Lock(); a.recalls--; m.mu.Unlock() }()
	wait, stopWait := context.WithTimeout(ctx, 15*time.Second)
	response, err := p.callObserved(wait, command, func(r response) {
		if !r.Success {
			return
		}
		var q pendingQueue
		if json.Unmarshal(r.Data, &q) == nil {
			m.syncPending(a, p, q)
		}
	})
	stopWait()
	if err != nil {
		return PromotionResult{}, err
	}
	var result struct {
		Outcome string `json:"outcome"`
	}
	if err = json.Unmarshal(response.Data, &result); err != nil {
		return PromotionResult{}, err
	}
	if result.Outcome != "promoted" && result.Outcome != "already_steering" {
		return PromotionResult{}, fmt.Errorf("pi did not promote this occurrence: %s", result.Outcome)
	}
	active, commitRelease, err := m.cfg.Yesmen.BeginUse(info.Yesman)
	if err != nil {
		return PromotionResult{}, err
	}
	defer commitRelease()
	m.mu.Lock()
	current, exists := a.deliveries[id]
	if !exists || !active.HiredAt.Equal(y.HiredAt) || !m.recallOwnerLocked(a, since, y.HiredAt) || a.proc != p ||
		current.Session != d.Session || p.deliverySession() != d.Session || a.nativeQueue.Epoch != d.queueEpoch {
		m.mu.Unlock()
		return PromotionResult{}, errStopped
	}
	writer := a.deliveryWriter
	revision := m.persistDeliveriesLocked(a) // a retry also retries a failed receipt write
	m.mu.Unlock()
	commitRelease()
	if err = writer.wait(ctx, revision, m.shutdown); err != nil {
		return PromotionResult{}, err
	}
	active, finishRelease, err := m.cfg.Yesmen.BeginUse(info.Yesman)
	if err != nil {
		return PromotionResult{}, err
	}
	defer finishRelease()
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists = a.deliveries[id]
	if !exists || !active.HiredAt.Equal(y.HiredAt) || !m.recallOwnerLocked(a, since, y.HiredAt) || a.proc != p ||
		current.Session != d.Session || p.deliverySession() != d.Session || a.nativeQueue.Epoch != d.queueEpoch {
		return PromotionResult{}, errStopped
	}
	return PromotionResult{Delivery: a.deliveryView(current), Outcome: result.Outcome}, nil
}
