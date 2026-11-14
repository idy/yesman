package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Delivery is a submission receipt, never a second queue. The installed pi
// protocol correlates command responses but not user messages or queue entries.
// Therefore no receipt claims that its message was consumed. Native transcript
// messages remain independent, including transformed inputs and identical text.
type Delivery struct {
	ID          string    `json:"id"`
	Session     string    `json:"session"`
	Revision    uint64    `json:"revision"`
	State       string    `json:"state"`
	Text        string    `json:"text"`
	Images      []Image   `json:"images,omitempty"` // legacy migration/local fixtures only; new receipts never store image bytes
	ImageCount  int       `json:"imageCount"`
	Preview     bool      `json:"preview,omitempty"`
	Error       string    `json:"error,omitempty"`
	Order       uint64    `json:"order"`
	AgentSince  time.Time `json:"agentSince,omitzero"`
	boundSeq    uint64
	fingerprint string
	created     int64
	expires     int64
	registered  uint64
}

// QueueStatus describes the whole native pi queue, not receipt membership.
// Known is false after process/session replacement until pi reports a snapshot.
type QueueStatus struct {
	Known    bool `json:"known"`
	Steering int  `json:"steering"`
	FollowUp int  `json:"followUp"`
}

var deliveryID = regexp.MustCompile(`^[a-f0-9]{48}$`)

func pendingDelivery(state string) bool {
	return state == "sending" || state == "accepted" || state == "steering" || state == "queued"
}
func (a *agent) deliverySnapshot() []Delivery {
	receipts := make([]Delivery, 0, len(a.deliveries))
	for _, d := range a.deliveries {
		receipts = append(receipts, d)
	}
	return receipts
}

// Revisions and snapshot sequence numbers protect the page from bridge
// notifications delivered late or out of order. State changes never resubmit.
func (m *Manager) deliveryLocked(a *agent, d Delivery) Event {
	if a.deliverySealed {
		return Event{}
	}
	previous := a.deliveries[d.ID]
	if !pendingDelivery(d.State) && (pendingDelivery(previous.State) || d.expires == 0) {
		d.expires = time.Now().Add(deliveryRetention).UnixMilli()
	}
	d.Revision++
	d.Error, _ = deliveryClip(d.Error, maxDeliveryError)
	d.Session, _ = deliveryClip(d.Session, 160)
	a.deliveries[d.ID] = d
	removed := m.pruneDeliveriesLocked(a)
	m.persistDeliveriesLocked(a)
	a.seq++
	retained := make([]string, 0, len(a.deliveries))
	for id := range a.deliveries {
		retained = append(retained, id)
	}
	raw, _ := json.Marshal(struct {
		Type     string   `json:"type"`
		Delivery Delivery `json:"delivery"`
		Removed  []string `json:"removed,omitempty"`
		Retained []string `json:"retained"`
	}{"delivery_update", d, removed, retained})
	return Event{Agent: a.info.ID, AgentSince: a.info.Since, Seq: a.seq, Data: raw}
}

// Submit registers a stable client ID before dispatch. Repeating an uncertain
// bridge call returns its existing receipt and never sends the prompt twice.
func (m *Manager) Submit(ctx context.Context, id, messageID, text string, images []Image, expectedSince ...time.Time) (Delivery, error) {
	return m.submit(ctx, id, messageID, text, images, "steer", expectedSince...)
}
func (m *Manager) submit(ctx context.Context, id, messageID, text string, images []Image, behavior string, expectedSince ...time.Time) (Delivery, error) {
	if err := ctx.Err(); err != nil {
		return Delivery{}, err
	}
	if !deliveryID.MatchString(messageID) {
		return Delivery{}, errors.New("invalid message ID")
	}
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return Delivery{}, errors.New("empty message")
	}
	if err := validateImages(images); err != nil {
		return Delivery{}, err
	}
	info, err := m.Get(id)
	if err != nil {
		return Delivery{}, err
	}
	if len(expectedSince) > 0 && !info.Since.Equal(expectedSince[0]) {
		return Delivery{}, errors.New("the agent was replaced; reopen its conversation before sending")
	}
	y, err := m.cfg.Yesmen.Get(info.Yesman)
	if err != nil {
		return Delivery{}, err
	}
	fingerprint := deliveryFingerprint(text, images)
	active, release, err := m.cfg.Yesmen.BeginUse(info.Yesman)
	if err != nil {
		return Delivery{}, err
	}
	defer release()
	if !active.HiredAt.Equal(y.HiredAt) {
		return Delivery{}, errStopped
	}
	y = active
	currentInfo, err := m.Get(id)
	if err != nil {
		return Delivery{}, err
	}
	if !currentInfo.Since.Equal(info.Since) {
		return Delivery{}, errStopped
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Delivery{}, errStopped
	}
	if m.worktreeCleanup[id] {
		m.mu.Unlock()
		return Delivery{}, errWorktreeCleanup
	}
	a, err := m.workspaceAgentLocked(y, info)
	if err != nil {
		m.mu.Unlock()
		return Delivery{}, err
	}
	m.mu.Unlock()
	if err = m.ensureDeliveries(a, filepath.Join(y.Path, ".yesman", "sessions", info.Name, "delivery.json")); err != nil {
		return Delivery{}, err
	}
	m.mu.Lock()
	if m.worktreeCleanup[id] {
		m.mu.Unlock()
		return Delivery{}, errWorktreeCleanup
	}
	if m.agents[id] != a || a.retiring {
		m.mu.Unlock()
		return Delivery{}, errors.New("the agent was replaced; reopen its conversation before sending")
	}
	if d, ok := a.deliveries[messageID]; ok {
		if !d.AgentSince.Equal(info.Since) || d.fingerprint != fingerprint {
			m.mu.Unlock()
			return Delivery{}, errors.New("this message ID belongs to another submission")
		}
		writer, registered := a.deliveryWriter, d.registered
		waitRegistration := d.State == "sending" && d.Session == ""
		m.mu.Unlock()
		if waitRegistration {
			if err = writer.wait(ctx, registered, m.shutdown); err != nil {
				if ctx.Err() != nil {
					return Delivery{}, ctx.Err()
				}
				return m.failInitialDelivery(a, messageID, err), nil
			}
			m.mu.Lock()
			d = a.deliveries[messageID]
			m.mu.Unlock()
		}
		return d, nil
	}
	if m.closed {
		m.mu.Unlock()
		return Delivery{}, errStopped
	}
	if err = newDeliveryIDAllowed(messageID, a.deliveryFloor, time.Now()); err != nil {
		m.mu.Unlock()
		return Delivery{}, err
	}
	pending := 0
	for _, receipt := range a.deliveries {
		if pendingDelivery(receipt.State) {
			pending++
		}
	}
	if pending >= maxPendingDeliveries {
		m.mu.Unlock()
		return Delivery{}, errors.New("too many pending submissions; wait for the current run before sending more")
	}
	preview, truncated := deliveryClip(text, maxDeliveryPreview)
	a.deliveryOrder++
	d := Delivery{ID: messageID, State: "sending", Text: preview, ImageCount: len(images), Preview: truncated, Order: a.deliveryOrder, AgentSince: info.Since, fingerprint: fingerprint, created: deliveryTimestamp(messageID)}
	a.deliveries[messageID] = d
	ev := m.deliveryLocked(a, d)
	registered := a.deliveryVersion
	d = a.deliveries[messageID]
	d.registered = registered
	a.deliveries[messageID] = d
	m.mu.Unlock()
	m.cfg.OnEvent(ev)
	if err = a.deliveryWriter.wait(context.Background(), registered, m.shutdown); err != nil {
		return m.failInitialDelivery(a, messageID, err), nil
	}
	release()
	m.mu.Lock()
	if m.agents[id] == a && !a.retiring {
		a.nudged = false
		a.inbox++
	}
	m.mu.Unlock()
	// Only the private receipt ID crosses this Go call. The pi command and
	// user text remain byte-for-byte the same as the original submission path.
	err = m.promptDelivery(context.Background(), id, text, behavior, messageID, a, &y.HiredAt, images...)
	m.mu.Lock()
	current := a.deliveries[messageID]
	if !a.deliverySealed && err != nil && (current.State == "sending" || current.State == "interrupted" && current.Session == "") {
		current.State, current.Error = "unconfirmed", err.Error()
		// Before binding a process there was no opportunity to dispatch RPC.
		if current.Session == "" {
			current.State = "failed"
		}
		ev = m.deliveryLocked(a, current)
	} else {
		ev = Event{}
	}
	d = a.deliveries[messageID]
	m.mu.Unlock()
	if ev.Seq != 0 {
		m.cfg.OnEvent(ev)
	}
	return d, nil
}

func (m *Manager) failInitialDelivery(a *agent, id string, err error) Delivery {
	m.mu.Lock()
	d := a.deliveries[id]
	var ev Event
	if !a.deliverySealed && d.State == "sending" && d.Session == "" {
		d.State, d.Error = "failed", err.Error()
		ev = m.deliveryLocked(a, d)
		d = a.deliveries[id]
	}
	m.mu.Unlock()
	if ev.Seq != 0 {
		m.cfg.OnEvent(ev)
	}
	return d
}

func (m *Manager) bindDelivery(a *agent, p *proc, id string) error {
	if id == "" {
		return nil
	}
	info, err := m.Get(a.info.ID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := a.deliveries[id]
	if !ok || !d.AgentSince.Equal(info.Since) {
		return errors.New("the agent was replaced; reopen its conversation before sending")
	}
	if a.proc != p || a.retiring || d.State != "sending" {
		return errStopped
	}
	d.Session, d.boundSeq = p.deliverySession(), a.seq
	a.deliveries[id] = d
	return nil
}

// Only command responses identify individual submissions. queued confirms
// acceptance for that delivery mode, not continued queue membership. A prior
// empty queue/settle is retained as uncertainty, never as a consumed receipt.
func (m *Manager) deliveryResponse(a *agent, p *proc, id, behavior string, r response) {
	if id == "" {
		return
	}
	m.mu.Lock()
	d, ok := a.deliveries[id]
	if !ok || a.retiring || a.proc != p || d.Session != p.deliverySession() || d.State != "sending" {
		m.mu.Unlock()
		return
	}
	var data struct {
		Disposition string `json:"disposition"`
	}
	_ = json.Unmarshal(r.Data, &data)
	switch {
	case !r.Success:
		d.State, d.Error = "failed", r.Error
	case data.Disposition == "handled":
		d.State = "handled"
	case a.deliverySettled > d.boundSeq:
		d.State = "unconfirmed"
	case data.Disposition == "queued":
		if a.queue.Known && a.queue.Steering+a.queue.FollowUp == 0 {
			d.State = "unconfirmed"
		} else if behavior == "followUp" {
			d.State = "queued"
		} else {
			d.State = "steering"
		}
	default:
		d.State = "accepted"
	}
	ev := m.deliveryLocked(a, d)
	m.mu.Unlock()
	m.cfg.OnEvent(ev)
}

// observeDelivery never matches user content to receipts. queue_update is a
// complete native snapshot and agent_settled is only a run boundary. Neither
// carries enough identity to report consumption of an individual message.
func (m *Manager) observeDelivery(a *agent, p *proc, kind string, raw json.RawMessage) {
	if kind != "queue_update" && kind != "agent_settled" {
		return
	}
	m.mu.Lock()
	if a.proc != p || a.retiring {
		m.mu.Unlock()
		return
	}
	if kind == "queue_update" {
		var e struct {
			Steering []string `json:"steering"`
			FollowUp []string `json:"followUp"`
		}
		if json.Unmarshal(raw, &e) != nil {
			m.mu.Unlock()
			return
		}
		a.queue = QueueStatus{Known: true, Steering: len(e.Steering), FollowUp: len(e.FollowUp)}
	} else {
		a.deliverySettled = a.seq + 1
	}
	var events []Event
	for _, d := range a.deliveries {
		uncertain := kind == "agent_settled" && (d.State == "accepted" || d.State == "steering" || d.State == "queued") ||
			kind == "queue_update" && a.queue.Steering+a.queue.FollowUp == 0 && (d.State == "steering" || d.State == "queued")
		if d.Session == p.deliverySession() && uncertain {
			d.State = "unconfirmed"
			events = append(events, m.deliveryLocked(a, d))
		}
	}
	m.mu.Unlock()
	for _, ev := range events {
		m.cfg.OnEvent(ev)
	}
}
func (m *Manager) interruptDeliveriesLocked(a *agent) []Event {
	if a.deliverySealed {
		return nil
	}
	a.queue = QueueStatus{}
	a.seq++
	raw, _ := json.Marshal(struct {
		Type  string      `json:"type"`
		Queue QueueStatus `json:"queue"`
	}{"delivery_queue", a.queue})
	events := []Event{{Agent: a.info.ID, AgentSince: a.info.Since, Seq: a.seq, Data: raw}}
	for _, d := range a.deliveries {
		if d.Session != "" && pendingDelivery(d.State) {
			d.State, d.Error = "interrupted", ""
			events = append(events, m.deliveryLocked(a, d))
		}
	}
	return events
}

// Messages also checks get_state on reconnect, so a session replaced by an
// extension cannot leave old native queue counts attached to its replacement.
func (m *Manager) deliverySessionChanged(a *agent, p *proc, session string) {
	m.mu.Lock()
	if a.proc != p || a.retiring {
		m.mu.Unlock()
		return
	}
	old := p.piSession.Load().(string)
	p.piSession.Store(session)
	var events []Event
	if old != "" && old != session {
		events = m.interruptDeliveriesLocked(a)
	}
	m.mu.Unlock()
	for _, ev := range events {
		m.cfg.OnEvent(ev)
	}
}
