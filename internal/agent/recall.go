package agent

import (
	"cmp"
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
	// Members lists, for the page only, the inputs a whole occurrence's
	// payload joins, primary first. It is never sent to pi or a provider.
	Members []string `json:"members,omitempty"`
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
	// Capability 4: later human inputs appended into this same occurrence, in
	// order, and whether pi's idle preparation has claimed (not taken) it.
	Segments  []string `json:"segments,omitempty"`
	Preparing bool     `json:"preparing,omitempty"`
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
	HumanSteer        bool          `json:"humanSteer"`    // strict single-slot human steering protocol
	AppendAllowed     bool          `json:"appendAllowed"` // a hint; pi decides each append atomically
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
	a.recallPayloads[id] = recallPayload{RecallPayload{Text: text, Images: slices.Clone(images)}, size}
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
	if d.MergedInto != "" {
		// Part of another occurrence: only that message is actionable.
		d.OriginalText, d.OriginalPayload, d.NativePending, d.CanPromote, d.CanCancel, d.CanRestore, d.Preparing = false, false, false, false, false, false, false
		return d
	}
	aggregate, complete := a.aggregatePayloadLocked(d)
	if len(d.Merged) > 0 {
		payload, hasPayload = recallPayload{RecallPayload: aggregate}, complete
	}
	d.OriginalText = hasPayload && payload.Text != ""
	d.OriginalPayload = hasPayload && !a.retiring && !a.removing && !a.deliverySealed
	d.NativePending = d.queueID != "" && d.queueEpoch == a.nativeQueue.Epoch && a.nativeQueue.supported() && a.proc != nil && !a.retiring && !a.removing && !a.deliverySealed
	d.Preparing = d.NativePending && d.queuePreparing
	d.CanPromote = d.NativePending && d.State == "queued" && !d.queuePreparing && a.nativeQueue.supportsPromotion() && a.nativeQueue.PromoteAllowed
	d.CanCancel = hasPayload && d.NativePending
	d.CanRestore = hasPayload && d.State == "cancelled" && !a.retiring && !a.removing
	d.CanRecover = d.recoverDraft && d.OriginalPayload && recoverableDelivery(d.State) && !d.NativePending
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
			if d.queueID != "" || d.MergedInto != "" && pendingDelivery(d.State) {
				d.queueID, d.queuePreparing = "", false
				d.State = "interrupted"
				events = append(events, m.deliveryLocked(a, d))
			}
		}
	}
	previous := a.nativeQueue
	a.nativeQueue = q
	a.queue = pendingStatus(q)
	present := map[string]pendingItem{}
	parents := map[string]string{} // appended input -> the occurrence's primary input
	for _, item := range q.Items {
		if item.QueueID != "" && deliveryID.MatchString(item.SubmissionID) && (item.Kind == "steer" || item.Kind == "followUp") {
			// A duplicate host identity is ambiguous. Never authorize either occurrence.
			if _, exists := present[item.SubmissionID]; exists {
				present[item.SubmissionID] = pendingItem{}
			} else {
				present[item.SubmissionID] = item
			}
			for _, segment := range item.Segments {
				if _, exists := parents[segment]; exists || !deliveryID.MatchString(segment) {
					parents[segment] = ""
				} else {
					parents[segment] = item.SubmissionID
				}
			}
		}
	}
	for segment := range parents {
		if _, primary := present[segment]; primary {
			parents[segment] = ""
		}
	}
	changes := map[string]pendingItem{}
	for _, item := range q.Changes {
		changes[item.QueueID] = item
	}
	session := p.deliverySession()
	ids := make([]string, 0, len(a.deliveries))
	for id, d := range a.deliveries {
		if d.Session == session && d.AgentSince.Equal(a.info.Since) {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(x, y string) int { return cmp.Compare(a.deliveries[x].Order, a.deliveries[y].Order) })
	known := func(list []string) []string {
		var out []string
		for _, id := range list {
			if d, ok := a.deliveries[id]; ok && d.Session == session && d.AgentSince.Equal(a.info.Since) && !slices.Contains(out, id) && len(out) < maxMergedInputs {
				out = append(out, id)
			}
		}
		return out
	}
	// Occurrences first, so that their inputs can follow them.
	ended := map[string]string{} // primary input -> its final state
	for _, id := range ids {
		d := a.deliveries[id]
		if item, ok := present[d.ID]; ok && item.QueueID != "" && pendingDelivery(d.State) && d.MergedInto == "" {
			state := "steering"
			if item.Kind == "followUp" {
				state = "queued"
			}
			merged := known(item.Segments)
			if d.queueID == item.QueueID && d.queueEpoch == q.Epoch && d.State == state && d.queueRevision == item.ItemRevision && d.queuePreparing == item.Preparing &&
				slices.Equal(d.Merged, merged) && previous.ControlGeneration == q.ControlGeneration && previous.PromoteAllowed == q.PromoteAllowed {
				continue
			}
			d.queueID, d.queueEpoch, d.State = item.QueueID, q.Epoch, state
			d.queueRevision, d.queuePreparing = item.ItemRevision, item.Preparing
			if !slices.Equal(d.Merged, merged) {
				m.setMergedLocked(a, &d, merged)
			}
			events = append(events, m.deliveryLocked(a, d))
		} else if d.queueID != "" {
			change := changes[d.queueID]
			d.queueID, d.queuePreparing = "", false
			// pi reports the final members of an occurrence it took or cancelled.
			final := slices.Clone(d.Merged)
			for _, segment := range known(change.Segments) {
				if !slices.Contains(final, segment) && len(final) < maxMergedInputs {
					final = append(final, segment)
				}
			}
			if len(final) != len(d.Merged) {
				m.setMergedLocked(a, &d, final)
			}
			switch change.State {
			case "taken":
				d.State, d.AwaitingRecord = "dispatched", d.recoverDraft
			case "cancelled":
				d.State = "cancelled"
			default:
				d.State = "unconfirmed"
				if !d.recoverDraft {
					m.forgetRecallLocked(a, d.ID)
				}
			}
			ended[d.ID] = d.State
			events = append(events, m.deliveryLocked(a, d))
		}
	}
	// Appended inputs never become occurrences of their own: they follow the
	// one they joined, including when it is taken, cancelled or lost.
	for _, id := range ids {
		d := a.deliveries[id]
		if d.MergedInto == "" && !pendingDelivery(d.State) {
			continue
		}
		parent := parents[d.ID]
		if parent == "" {
			parent = d.MergedInto
		}
		if parent == "" {
			for primary := range ended {
				if slices.Contains(a.deliveries[primary].Merged, d.ID) {
					parent = primary
				}
			}
		}
		root, ok := a.deliveries[parent]
		if !ok || parent == d.ID || root.MergedInto != "" || root.Session != session || !root.AgentSince.Equal(a.info.Since) {
			continue
		}
		if _, live := parents[d.ID]; !live && pendingDelivery(root.State) && root.queueID != "" {
			continue // still pending, but pi no longer lists it in that occurrence
		}
		events = append(events, m.mergeChildLocked(a, root, d)...)
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
// For inputs appended into one occurrence it cancels that whole occurrence at
// its current revision and returns the complete combined message.
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
	d, ok := a.rootDeliveryLocked(id)
	_, hasPayload := a.aggregatePayloadLocked(d)
	if !ok || !hasPayload {
		m.mu.Unlock()
		return RecallResult{}, errors.New("the full original message is no longer available for editing")
	}
	id = d.ID
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
	final := a.deliveries[id]
	payload, ok := a.aggregatePayloadLocked(final)
	if !ok {
		return RecallResult{}, errors.New("this recalled message was already restored")
	}
	return RecallResult{a.deliveryView(final), payload}, nil
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
	root, _ := a.rootDeliveryLocked(id)
	payload, ok := a.aggregatePayloadLocked(root)
	if !ok {
		return RecallPayload{}, errors.New("the full original message is no longer available for editing")
	}
	if len(root.Merged) > 0 {
		payload.Members = root.members()
	}
	return payload, nil
}

func (m *Manager) SubmissionText(agentID, id string, since time.Time) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[agentID]
	if a == nil || !a.info.Since.Equal(since) || a.retiring || a.removing {
		return "", errStopped
	}
	root, _ := a.rootDeliveryLocked(id)
	payload, ok := a.aggregatePayloadLocked(root)
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
	d, ok := a.rootDeliveryLocked(id)
	if !ok || d.State != "cancelled" && !a.deliveryView(d).CanRecover {
		m.mu.Unlock()
		return errors.New("no retained draft to acknowledge")
	}
	for _, member := range d.members() {
		m.forgetRecallLocked(a, member)
	}
	event := m.deliveryLocked(a, d)
	m.mu.Unlock()
	m.cfg.OnEvent(event)
	return nil
}
