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

// errCallerGone is returned when the agent a request authenticated as is no
// longer the one running: stopped, restarted, retired or replaced.
var errCallerGone = errors.New("the agent that sent this request was stopped or replaced; nothing was changed")

// CallerOperation runs fn as a change the agent holding token asked for, while
// that agent is still the one the request authenticated as: the same running
// process and credential, the same generation (want.Since), not stopping or
// retiring. A request whose agent was stopped, restarted, removed or replaced
// after it authenticated fails without running fn. Stop waits for an
// operation already admitted, so nothing commits for a process after Stop
// returns. fn also runs within the process's change admission, as a
// BeginChange does: every path that retires the credential (Stop, the
// process exiting, a failed start, quitting) waits for it and admits no
// later one.
//
// Callers hold their workspace lease (Store.Use) around it, so Archive, which
// stops agents under the exclusive lease, never waits on an admitted
// operation that waits on it. fn must be short and must not stop agents or
// wait on their runs.
func (m *Manager) CallerOperation(token string, want Info, fn func() error) error {
	m.mu.Lock()
	a := m.agents[want.ID]
	var p *proc
	if token != "" && a != nil && a.token == token {
		p = a.tokenProc
	}
	m.mu.Unlock()
	if p == nil {
		return errCallerGone
	}
	// The gate before mu, as BeginChange takes it: retiring the credential
	// holds the gate exclusively outside mu.
	p.changes.RLock()
	m.mu.Lock()
	ok := m.tokens[token] == want.ID && a.token == token && a.tokenProc == p && a.proc == p && !p.changesClosed &&
		!a.stopping && !a.retiring && !a.removing && !m.closed && a.info.Since.Equal(want.Since)
	if ok {
		select {
		case <-p.done:
			ok = false // exited; watch has not cleared it yet
		default:
		}
	}
	if !ok {
		m.mu.Unlock()
		p.changes.RUnlock()
		return errCallerGone
	}
	a.calls++
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		a.calls--
		if a.calls == 0 {
			m.callsDone.Broadcast()
		}
		m.mu.Unlock()
		p.changes.RUnlock()
	}()
	return fn()
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
	err := m.promptDelivery(ctx, lead, msg, "followUp", "", nil, &since, Origin{Source: OriginYesman, Kind: "needs-you"})
	if errors.Is(err, errLeadPaused) && m.deferForLead(yesman, since, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = m.TellLeadAboutUser(ctx, yesman, since, msg)
	}) {
		return nil // kept for when the user writes to the lead they stopped
	}
	return err
}
