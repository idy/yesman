package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

const maxRecallBytes = 128 << 20 // across all agents; full payloads never go to disk
const maxAgentRecallBytes = 32 << 20
const maxRecallRecords = 64

type RecallPayload struct {
	Text   string  `json:"text"`
	Images []Image `json:"images"`
}
type recallPayload struct {
	RecallPayload
	bytes int
}
type pendingItem struct {
	QueueID      string `json:"queueId"`
	SubmissionID string `json:"submissionId"`
	Kind         string `json:"kind"`
	State        string `json:"state,omitempty"`
	ItemRevision uint64 `json:"itemRevision"`
}
type pendingQueue struct {
	Capability        int           `json:"capability"`
	Epoch             string        `json:"epoch"`
	Revision          uint64        `json:"revision"`
	Items             []pendingItem `json:"items"`
	Changes           []pendingItem `json:"changes,omitempty"`
	ControlGeneration uint64        `json:"controlGeneration"`
	PromoteAllowed    bool          `json:"promoteAllowed"`
	PromotionBlocked  string        `json:"promotionBlocked,omitempty"`
	IdleResume        bool          `json:"idleResume"`
}

func (q pendingQueue) supportsPromotion() bool { return q.Capability >= 2 && q.Capability <= 4 }
func (q pendingQueue) supported() bool         { return q.Capability >= 1 && q.Capability <= 4 }

// supportsInputIdentity reports host-only input provenance (capability 3+).
func (q pendingQueue) supportsInputIdentity() bool { return q.Capability == 3 || q.Capability == 4 }

// supportsAppend reports the native same-occurrence append of capability 4.
// Never inferred from counts or from an older capability.
func (q pendingQueue) supportsAppend() bool { return q.Capability == 4 }

type RecallResult struct {
	Delivery Delivery      `json:"delivery"`
	Payload  RecallPayload `json:"payload"`
}

func (m *Manager) retainRecallLocked(a *agent, id, text string, images []Image) {
	if _, exists := a.recallPayloads[id]; exists {
		return
	}
	size := len(text)
	for _, image := range images {
		size += len(image.Data) + len(image.MimeType) + len(image.Type)
	}
	if size > maxAgentRecallBytes || a.recallBytes+size > maxAgentRecallBytes || m.recallBytes+size > maxRecallBytes || len(a.recallPayloads) >= maxRecallRecords {
		return
	}
	if a.recallPayloads == nil {
		a.recallPayloads = map[string]recallPayload{}
	}
	a.recallPayloads[id] = recallPayload{RecallPayload{text, slices.Clone(images)}, size}
	a.recallBytes += size
	m.recallBytes += size
}
func (m *Manager) forgetRecallLocked(a *agent, id string) {
	if payload, ok := a.recallPayloads[id]; ok {
		a.recallBytes -= payload.bytes
		m.recallBytes -= payload.bytes
		delete(a.recallPayloads, id)
	}
}
func (m *Manager) clearRecallLocked(a *agent) {
	for id := range a.recallPayloads {
		m.forgetRecallLocked(a, id)
	}
	a.nativeQueue = pendingQueue{}
}
func (a *agent) deliveryView(d Delivery) Delivery {
	payload, hasPayload := a.recallPayloads[d.ID]
	d.OriginalText = hasPayload && payload.Text != ""
	d.OriginalPayload = hasPayload && !a.retiring && !a.removing && !a.deliverySealed
	d.NativePending = d.queueID != "" && d.queueEpoch == a.nativeQueue.Epoch && a.nativeQueue.supported() && a.proc != nil && !a.retiring && !a.removing && !a.deliverySealed
	d.CanPromote = d.NativePending && d.State == "queued" && a.nativeQueue.supportsPromotion() && a.nativeQueue.PromoteAllowed
	d.CanCancel = hasPayload && d.NativePending
	d.CanRestore = hasPayload && d.State == "cancelled" && !a.retiring && !a.removing
	return d
}

// applyPendingLocked runs on the ordered RPC reader. This identity comes from
// the queue owner, never from matching user text, image content or whole counts.
func (m *Manager) applyPendingLocked(a *agent, p *proc, q pendingQueue) []Event {
	if !q.supported() || q.Epoch == "" || m.agents[a.info.ID] != a || a.proc != p || a.retiring || a.removing || a.deliverySealed {
		return nil
	}
	if a.nativeQueue.Epoch == q.Epoch && q.Revision <= a.nativeQueue.Revision {
		return nil
	}
	var events []Event
	if a.nativeQueue.Epoch != "" && a.nativeQueue.Epoch != q.Epoch {
		for _, d := range a.deliveries {
			if d.queueID != "" {
				d.queueID = ""
				d.State = "interrupted"
				events = append(events, m.deliveryLocked(a, d))
			}
		}
	}
	previous := a.nativeQueue
	a.nativeQueue = q
	a.queue = pendingStatus(q)
	present := map[string]pendingItem{}
	for _, item := range q.Items {
		if item.QueueID != "" && deliveryID.MatchString(item.SubmissionID) && (item.Kind == "steer" || item.Kind == "followUp") {
			// A duplicate host identity is ambiguous. Never authorize either occurrence.
			if _, exists := present[item.SubmissionID]; exists {
				present[item.SubmissionID] = pendingItem{}
			} else {
				present[item.SubmissionID] = item
			}
		}
	}
	changes := map[string]pendingItem{}
	for _, item := range q.Changes {
		changes[item.QueueID] = item
	}
	for _, d := range a.deliveries {
		if d.Session != p.deliverySession() || !d.AgentSince.Equal(a.info.Since) {
			continue
		}
		if item, ok := present[d.ID]; ok && item.QueueID != "" && pendingDelivery(d.State) {
			state := "steering"
			if item.Kind == "followUp" {
				state = "queued"
			}
			if d.queueID == item.QueueID && d.queueEpoch == q.Epoch && d.State == state && d.queueRevision == item.ItemRevision && previous.ControlGeneration == q.ControlGeneration && previous.PromoteAllowed == q.PromoteAllowed {
				continue
			}
			d.queueID, d.queueEpoch, d.State = item.QueueID, q.Epoch, state
			d.queueRevision = item.ItemRevision
			events = append(events, m.deliveryLocked(a, d))
		} else if d.queueID != "" {
			change := changes[d.queueID]
			d.queueID = ""
			switch change.State {
			case "taken":
				d.State = "dispatched"
			case "cancelled":
				d.State = "cancelled"
			default:
				d.State = "unconfirmed"
				m.forgetRecallLocked(a, d.ID)
			}
			events = append(events, m.deliveryLocked(a, d))
		}
	}
	return events
}
func pendingStatus(q pendingQueue) QueueStatus {
	status := QueueStatus{Known: q.supported(), RecallSupported: q.supported(), PromoteSupported: q.supportsPromotion()}
	for _, item := range q.Items {
		if item.Kind == "steer" {
			status.Steering++
		} else if item.Kind == "followUp" {
			status.FollowUp++
		}
	}
	return status
}

func (m *Manager) syncPending(a *agent, p *proc, q pendingQueue) {
	m.mu.Lock()
	events := m.applyPendingLocked(a, p, q)
	m.mu.Unlock()
	for _, event := range events {
		m.cfg.OnEvent(event)
	}
}

// CancelSubmission atomically removes one native occurrence and returns the
// full original payload. Repeating it after a lost response returns the same
// cancelled intent until AckRecall releases its bounded in-memory payload.
// It never starts a stopped agent and never clears or rebuilds either queue.
func (m *Manager) CancelSubmission(ctx context.Context, agentID, id string, since time.Time) (RecallResult, error) {
	ctx, cancelOperation := context.WithTimeout(ctx, 30*time.Second)
	defer cancelOperation()
	info, err := m.Get(agentID)
	if err != nil {
		return RecallResult{}, err
	}
	if !info.Since.Equal(since) {
		return RecallResult{}, errors.New("the agent was replaced; reopen its conversation")
	}
	y, release, err := m.cfg.Yesmen.BeginUse(info.Yesman)
	if err != nil {
		return RecallResult{}, err
	}
	defer release()
	m.mu.Lock()
	a := m.agents[agentID]
	if a == nil || m.closed || a.retiring || a.removing || a.deliverySealed || !a.info.Since.Equal(since) || !a.workspaceSince.Equal(y.HiredAt) || m.worktreeCleanup[agentID] {
		m.mu.Unlock()
		return RecallResult{}, errStopped
	}
	d, ok := a.deliveries[id]
	payload, hasPayload := a.recallPayloads[id]
	if !ok || !hasPayload {
		m.mu.Unlock()
		return RecallResult{}, errors.New("the full original message is no longer available for editing")
	}
	p := a.proc
	if d.State != "cancelled" && !a.deliveryView(d).CanCancel {
		m.mu.Unlock()
		return RecallResult{}, errors.New("this message is no longer pending in pi")
	}
	a.recalls++
	cancelCommand := map[string]any{"type": "cancel_queued_message", "epoch": d.queueEpoch, "queueId": d.queueID}
	if a.nativeQueue.Capability >= 2 {
		cancelCommand["expectedRevision"] = d.queueRevision
	}
	m.mu.Unlock()
	// The reservation and captured generation/epoch survive without holding the
	// workspace lease. An unresponsive RPC must not hold Archive's writer lock
	// (and, behind it, all other users of the same workspace) hostage.
	release()
	defer func() { m.mu.Lock(); a.recalls--; m.mu.Unlock() }()
	if d.State != "cancelled" {
		// Bound the RPC even if its client disconnects. The ordered queue event can
		// establish cancellation independently of the reply; retry remains safe.
		wait, cancel := context.WithTimeout(ctx, 15*time.Second)
		response, callErr := p.callObserved(wait, cancelCommand, func(r response) {
			if !r.Success {
				return
			}
			var result pendingQueue
			if json.Unmarshal(r.Data, &result) == nil {
				m.syncPending(a, p, result)
			}
		})
		cancel()
		if callErr != nil {
			return RecallResult{}, callErr
		}
		var result struct {
			Outcome string `json:"outcome"`
		}
		if err = json.Unmarshal(response.Data, &result); err != nil {
			return RecallResult{}, err
		}
		switch result.Outcome {
		case "cancelled":
		case "stale_item":
			return RecallResult{}, errors.New("this queued message changed; refresh its status and try again")
		case "stale":
			return RecallResult{}, errors.New("the pi queue was replaced; refresh this conversation")
		case "not_pending":
			return RecallResult{}, errors.New("this message is no longer pending in pi; it was not recalled")
		default:
			return RecallResult{}, errors.New("cancellation could not be confirmed")
		}
	}
	active, commitRelease, err := m.cfg.Yesmen.BeginUse(info.Yesman)
	if err != nil {
		return RecallResult{}, err
	}
	defer commitRelease()
	m.mu.Lock()
	if !active.HiredAt.Equal(y.HiredAt) || !m.recallOwnerLocked(a, since, y.HiredAt) {
		m.mu.Unlock()
		return RecallResult{}, errStopped
	}
	current := a.deliveries[id]
	if current.State != "cancelled" || current.Session != d.Session || current.queueEpoch != d.queueEpoch || !current.AgentSince.Equal(since) {
		m.mu.Unlock()
		return RecallResult{}, errors.New("cancellation could not be confirmed")
	}
	writer := a.deliveryWriter
	revision := m.persistDeliveriesLocked(a) // retries can recover a prior disk-write failure
	m.mu.Unlock()
	commitRelease()
	// The writer owns this revision independently of our lease; retirement seals
	// and joins it. Reacquire a fresh lease before exposing a durable payload.
	if err = writer.wait(ctx, revision, m.shutdown); err != nil {
		return RecallResult{}, err
	}
	active, finishRelease, err := m.cfg.Yesmen.BeginUse(info.Yesman)
	if err != nil {
		return RecallResult{}, err
	}
	defer finishRelease()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !active.HiredAt.Equal(y.HiredAt) || !m.recallOwnerLocked(a, since, y.HiredAt) {
		return RecallResult{}, errStopped
	}
	if _, ok := a.recallPayloads[id]; !ok {
		return RecallResult{}, errors.New("this recalled message was already restored")
	}
	return RecallResult{a.deliveryView(a.deliveries[id]), RecallPayload{payload.Text, slices.Clone(payload.Images)}}, nil
}

// Called under m.mu, while a fresh Store lease pins the workspace generation.
func (m *Manager) recallOwnerLocked(a *agent, since, hiredAt time.Time) bool {
	return m.agents[a.info.ID] == a && !m.closed && !a.retiring && !a.removing && !a.deliverySealed &&
		a.info.Since.Equal(since) && a.workspaceSince.Equal(hiredAt) && !m.worktreeCleanup[a.info.ID]
}

// SubmissionPayload reads the existing bounded volatile original for display.
// It grants no queue authority and cannot restore a missing/retired payload.
func (m *Manager) SubmissionPayload(agentID, id string, since time.Time) (RecallPayload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[agentID]
	if a == nil || m.closed || !a.info.Since.Equal(since) || a.retiring || a.removing || a.deliverySealed || m.worktreeCleanup[agentID] {
		return RecallPayload{}, errStopped
	}
	payload, ok := a.recallPayloads[id]
	if !ok {
		return RecallPayload{}, errors.New("the full original message is no longer available for editing")
	}
	return RecallPayload{payload.Text, slices.Clone(payload.Images)}, nil
}

func (m *Manager) SubmissionText(agentID, id string, since time.Time) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[agentID]
	if a == nil || !a.info.Since.Equal(since) || a.retiring || a.removing {
		return "", errStopped
	}
	payload, ok := a.recallPayloads[id]
	if !ok {
		return "", errors.New("the full original message is no longer available for editing")
	}
	return payload.Text, nil
}

func (m *Manager) AckRecall(agentID, id string, since time.Time) error {
	m.mu.Lock()
	a := m.agents[agentID]
	if a == nil || !a.info.Since.Equal(since) || a.retiring || a.removing || a.deliverySealed {
		m.mu.Unlock()
		return errStopped
	}
	d, ok := a.deliveries[id]
	if !ok || d.State != "cancelled" {
		m.mu.Unlock()
		return errors.New("message was not recalled")
	}
	m.forgetRecallLocked(a, id)
	event := m.deliveryLocked(a, d)
	m.mu.Unlock()
	m.cfg.OnEvent(event)
	return nil
}
