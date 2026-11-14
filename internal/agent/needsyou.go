package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// What the user sees under Needs you, from the agents: a lead's own dialog,
// and a worker's dialog its lead handed to the user with HandOver. A worker's
// other questions go to its lead, which answers them itself. (The lead's
// manual handoffs are kept by yesmen.Store.)

// dialogID is the request ID of an extension_ui_request, or "".
func dialogID(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var d struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &d)
	return d.ID
}

// HandOver hands the dialog a worker waits on to the user: it shows under
// Needs you until it is answered, cancelled, times out or the worker stops.
// The lead may still answer it itself. Handing it over again is harmless.
func (m *Manager) HandOver(id, requestID string) error {
	if requestID == "" {
		return errors.New("give the original question's requestId")
	}
	m.mu.Lock()
	a := m.agents[id]
	if a == nil || a.proc == nil || a.retiring || a.info.Role != RoleWorker || a.info.Status != Waiting {
		m.mu.Unlock()
		return fmt.Errorf("%s is not waiting on a question", id)
	}
	if dialogID(a.info.Dialog) != requestID {
		m.mu.Unlock()
		return fmt.Errorf("%s is no longer waiting on question %q", id, requestID)
	}
	changed := a.handedOver != requestID
	a.handedOver = requestID
	m.mu.Unlock()
	if changed {
		m.changed()
	}
	return nil
}

// expireDialog stops offering a dialog once pi has answered it itself, at
// its timeout, unless it was answered or replaced meanwhile.
func (m *Manager) expireDialog(a *agent, p *proc, requestID string) {
	m.mu.Lock()
	if a.proc != p || a.info.Status != Waiting || dialogID(a.info.Dialog) != requestID {
		m.mu.Unlock()
		return
	}
	a.info.Status, a.info.Dialog, a.info.UpdatedAt = Working, nil, time.Now()
	a.handedOver = ""
	m.mu.Unlock()
	m.changed()
}

// TellLeadAboutUser tells a yesman's lead what the user did about something
// it handed them, as a yesman notice after its current run. since is the
// workspace's HiredAt: a new yesman of the same name is never told.
func (m *Manager) TellLeadAboutUser(ctx context.Context, yesman string, since time.Time, msg string) error {
	lead := ID(yesman, yesmen.LeadName)
	m.unnudge(lead)
	return m.promptDelivery(ctx, lead, msg, "followUp", "", nil, &since, Origin{Source: OriginYesman, Kind: "needs-you"})
}
