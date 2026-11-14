package agent

import (
	"context"
	"errors"
	"path/filepath"
	"time"
)

// Origin says where a submission came from when the user did not type it,
// such as a scheduled task. It is side metadata of the receipt only: pi's
// command and the message text are exactly what the sender gave, and no
// hook or model sees it.
type Origin struct {
	Kind string `json:"kind"` // "schedule"
	Plan string `json:"plan,omitempty"`
	Run  string `json:"run,omitempty"`
}

// OriginSchedule marks submissions of scheduled tasks.
const OriginSchedule = "schedule"

func (o *Origin) valid() bool {
	return o == nil || len(o.Kind) <= 32 && len(o.Plan) <= 16 && len(o.Run) <= 120
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

// SubmitFollowUp submits a message the app sends for the user, such as a
// scheduled task, through the same receipt path as Submit: registered
// durably before dispatch, idempotent under its ID. It is a follow-up, so an
// agent answering something finishes that first; it is never steered into
// a run or aborted for it.
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
