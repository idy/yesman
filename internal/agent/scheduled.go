package agent

import (
	"context"
	"errors"
	"path/filepath"
	"time"
)

// receiptOrigin checks the origin a receipt keeps: none, for what a person
// typed, or a scheduled run's.
func receiptOrigin(o *Origin) bool {
	return o == nil || o.Source == OriginSchedule && originPlanRE.MatchString(o.Plan) && originRunRE.MatchString(o.Run)
}

// DeliveryLookup says what is known of a submission ID.
type DeliveryLookup int

const (
	// DeliveryFound: its receipt is retained.
	DeliveryFound DeliveryLookup = iota
	// DeliveryAbsent: it was never registered. Its ID is newer than every
	// expired or evicted receipt and than the retention period, so a receipt
	// registered under it would still be here: submitting it now would be its
	// first dispatch.
	DeliveryAbsent
	// DeliveryUnknown: it is not retained, and may have been registered and
	// dispatched before its receipt expired or was evicted. It must not be
	// sent again.
	DeliveryUnknown
)

// SubmitFollowUp submits a message the app sends for the user, a scheduled
// task's run, through the same receipt path as Submit: registered durably
// before dispatch, idempotent under its ID. It is a follow-up, so an agent
// answering something finishes that first; it is never steered into a run or
// aborted for it. Its origin (Source OriginSchedule, with the plan and run)
// is kept in the receipt and, like every origin, beside the message: pi's
// command and the text are exactly what the scheduler gave.
func (m *Manager) SubmitFollowUp(ctx context.Context, id, messageID, text string, origin Origin, expectedSince time.Time) (Delivery, error) {
	return m.submit(ctx, id, messageID, text, nil, "followUp", &origin, expectedSince)
}

// LookupDelivery reads the receipt of a submission ID without sending
// anything, loading the agent's receipts when they are not loaded yet.
func (m *Manager) LookupDelivery(ctx context.Context, id, messageID string, expectedSince time.Time) (Delivery, DeliveryLookup, error) {
	if err := ctx.Err(); err != nil {
		return Delivery{}, DeliveryUnknown, err
	}
	if !deliveryID.MatchString(messageID) {
		return Delivery{}, DeliveryUnknown, errors.New("invalid message ID")
	}
	info, err := m.Get(id)
	if err != nil {
		return Delivery{}, DeliveryUnknown, err
	}
	if !expectedSince.IsZero() && !info.Since.Equal(expectedSince) {
		return Delivery{}, DeliveryUnknown, errors.New("the agent was replaced")
	}
	y, release, err := m.cfg.Yesmen.BeginUse(info.Yesman)
	if err != nil {
		return Delivery{}, DeliveryUnknown, err
	}
	defer release()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Delivery{}, DeliveryUnknown, errStopped
	}
	a, err := m.workspaceAgentLocked(y, info)
	m.mu.Unlock()
	if err != nil {
		return Delivery{}, DeliveryUnknown, err
	}
	if err := m.ensureDeliveries(a, filepath.Join(y.Path, ".yesman", "sessions", info.Name, "delivery.json")); err != nil {
		return Delivery{}, DeliveryUnknown, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agents[id] != a || a.retiring {
		return Delivery{}, DeliveryUnknown, errStopped
	}
	if d, ok := a.deliveries[messageID]; ok {
		if !d.AgentSince.Equal(info.Since) {
			return Delivery{}, DeliveryUnknown, errors.New("this message ID belongs to another submission")
		}
		return d, DeliveryFound, nil
	}
	if newDeliveryIDAllowed(messageID, a.deliveryFloor, time.Now()) == nil {
		return Delivery{}, DeliveryAbsent, nil
	}
	return Delivery{}, DeliveryUnknown, nil
}

// CredentialCurrent reports whether token still belongs to the running agent
// id it authenticated as: false once that agent was stopped, restarted or
// dismissed, or the manager quit. A request that authenticated, then waited
// for its body, checks again with it before it changes anything. It reads
// only memory.
func (m *Manager) CredentialCurrent(token, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if token == "" || m.closed || m.tokens[token] != id {
		return false
	}
	a := m.agents[id]
	return a != nil && a.token == token && !a.retiring
}
