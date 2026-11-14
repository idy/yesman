package agent

// Delivery is a receipt, not a second scheduler. pi alone owns its queues.
// Unconsumed receipts survive page reloads; after an app restart they are
// interrupted, since pi's in-memory queues do not survive its process.
import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Delivery struct {
	ID         string    `json:"id"`
	Session    string    `json:"session"`
	Revision   uint64    `json:"revision"`
	State      string    `json:"state"`
	Text       string    `json:"text"`
	Images     []Image   `json:"images,omitempty"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  int64     `json:"createdAt"`
	Order      uint64    `json:"order"`
	AgentSince time.Time `json:"agentSince,omitzero"`
}

var deliveryID = regexp.MustCompile(`^[a-f0-9]{48}$`)
var deliveryTag = regexp.MustCompile(`\n\n<!--yesman-delivery:([a-f0-9]{48})-->`)

func deliveryText(text, id string) string { return text + "\n\n<!--yesman-delivery:" + id + "-->" }
func taggedDelivery(text string) string {
	parts := deliveryTag.FindAllStringSubmatch(text, -1)
	if len(parts) > 0 {
		return parts[len(parts)-1][1]
	}
	return ""
}
func cleanDeliveryText(text string) string {
	matches := deliveryTag.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	last := matches[len(matches)-1]
	return text[:last[0]] + text[last[1]:]
}

func (a *agent) loadDeliveries() {
	if a.deliveries != nil {
		return
	}
	a.deliveries = map[string]Delivery{}
	data, err := os.ReadFile(a.deliveryFile())
	if err != nil {
		return
	}
	var receipts []Delivery
	if json.Unmarshal(data, &receipts) != nil {
		return
	}
	for _, d := range receipts {
		if !deliveryID.MatchString(d.ID) {
			continue
		}
		if d.State != "failed" && d.State != "interrupted" && d.State != "consumed" && d.State != "handled" {
			d.State, d.Error = "interrupted", ""
			d.Revision++
		}
		a.deliveries[d.ID] = d
	}
}
func (a *agent) deliveryFile() string {
	return filepath.Join(a.dir, ".yesman", "sessions", a.info.Name, "delivery.json")
}
func (a *agent) saveDeliveries() error {
	receipts := a.deliverySnapshot()
	for i := range receipts {
		if receipts[i].State == "consumed" {
			receipts[i].Text, receipts[i].Images = "", nil
		}
	}
	data, err := json.Marshal(receipts)
	if err != nil {
		return err
	}
	path := a.deliveryFile()
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err = os.WriteFile(path+".tmp", data, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
func (a *agent) deliverySnapshot() []Delivery {
	receipts := make([]Delivery, 0, len(a.deliveries))
	for _, d := range a.deliveries {
		receipts = append(receipts, d)
	}
	return receipts
}

// deliveryLocked advances a receipt and numbers its event atomically with
// snapshots. Revisions protect the page against delayed bridge notifications.
func (m *Manager) deliveryLocked(a *agent, d Delivery) Event {
	d.Revision++
	a.deliveries[d.ID] = d
	_ = a.saveDeliveries() // the initial durable registration is checked by Submit
	a.seq++
	raw, _ := json.Marshal(struct {
		Type     string   `json:"type"`
		Delivery Delivery `json:"delivery"`
	}{"delivery_update", d})
	return Event{Agent: a.info.ID, Seq: a.seq, Data: raw}
}

// Submit registers a stable, client-generated ID before dispatch. Repeating an
// uncertain bridge request returns the same receipt and never submits it twice.
// Slash commands retain pi's native command dispatch and use PromptImages.
func (m *Manager) Submit(ctx context.Context, id, messageID, text string, images []Image, expectedSince ...time.Time) (Delivery, error) {
	if err := ctx.Err(); err != nil {
		return Delivery{}, err
	}
	if !deliveryID.MatchString(messageID) {
		return Delivery{}, errors.New("invalid message ID")
	}
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		return Delivery{}, errors.New("commands use the command submission path")
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
	m.mu.Lock()
	a := m.agents[id]
	if a == nil {
		a = &agent{info: info, dir: y.Path}
		m.agents[id] = a
	}
	a.loadDeliveries()
	if d, ok := a.deliveries[messageID]; ok {
		m.mu.Unlock()
		return d, nil
	}
	if m.closed {
		m.mu.Unlock()
		return Delivery{}, errStopped
	}
	d := Delivery{ID: messageID, State: "sending", Text: text, Images: append([]Image(nil), images...), CreatedAt: time.Now().UnixMilli(), Order: uint64(len(a.deliveries) + 1), AgentSince: info.Since}
	a.deliveries[messageID] = d
	if err = a.saveDeliveries(); err != nil {
		delete(a.deliveries, messageID)
		m.mu.Unlock()
		return Delivery{}, err
	}
	ev := m.deliveryLocked(a, d)
	m.mu.Unlock()
	m.cfg.OnEvent(ev)
	m.unnudge(id)
	// The RPC remains owned by the manager if the page disconnects.
	err = m.prompt(context.Background(), id, deliveryText(text, messageID), "steer", images...)
	m.mu.Lock()
	current, exists := a.deliveries[messageID]
	if exists && current.State == "sending" {
		if err != nil {
			current.State, current.Error = "failed", err.Error()
		} else {
			current.State = "accepted"
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

func (m *Manager) bindDelivery(a *agent, p *proc, id string) error {
	if id == "" {
		return nil
	}
	if !p.deliveryReady.Load() {
		return errors.New("yesman's message delivery extension did not load; restart the agent before retrying")
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
	if a.proc != p {
		return errStopped
	}
	d.Session = p.deliverySession()
	a.deliveries[id] = d
	return nil
}

// observeDelivery runs on the pi reader. Queue membership confirms a pending
// delivery; only an actual user message confirms consumption. Queue removal
// alone (clear_queue, abort, extensions) never means the model read a message.
func (m *Manager) observeDelivery(a *agent, p *proc, kind string, raw json.RawMessage) json.RawMessage {
	var e struct {
		Steering []string                   `json:"steering"`
		FollowUp []string                   `json:"followUp"`
		Message  map[string]json.RawMessage `json:"message"`
	}
	if kind == "agent_settled" {
		m.mu.Lock()
		var events []Event
		if a.proc == p {
			for _, d := range a.deliveries {
				if d.Session == p.deliverySession() && d.State == "accepted" {
					d.State = "unconfirmed"
					events = append(events, m.deliveryLocked(a, d))
				}
			}
		}
		m.mu.Unlock()
		for _, ev := range events {
			m.cfg.OnEvent(ev)
		}
		return raw
	}
	if kind != "queue_update" && kind != "message_start" && kind != "message_end" {
		return raw
	}
	if json.Unmarshal(raw, &e) != nil {
		return raw
	}
	m.mu.Lock()
	if a.proc != p {
		m.mu.Unlock()
		return raw
	}
	var events []Event
	if kind == "queue_update" {
		present := map[string]bool{}
		for state, queue := range map[string][]string{"steering": e.Steering, "queued": e.FollowUp} {
			for _, text := range queue {
				id := taggedDelivery(text)
				present[id] = true
				if d, ok := a.deliveries[id]; ok && d.Session == p.deliverySession() && (d.State == "sending" || d.State == "accepted" || d.State == "steering" || d.State == "queued" || d.State == "unconfirmed") && d.State != state {
					d.State = state
					events = append(events, m.deliveryLocked(a, d))
				}
			}
		}
		for _, d := range a.deliveries {
			if d.Session == p.deliverySession() && !present[d.ID] && (d.State == "steering" || d.State == "queued") {
				d.State = "unconfirmed"
				events = append(events, m.deliveryLocked(a, d))
			}
		}
	} else {
		var role, id string
		_ = json.Unmarshal(e.Message["role"], &role)
		_ = json.Unmarshal(e.Message["yesmanDeliveryId"], &id)
		hasMetadata := id != ""
		if role == "user" {
			var content any
			_ = json.Unmarshal(e.Message["content"], &content)
			clean := func(text string) string {
				if hasMetadata { return text }
				if found := taggedDelivery(text); found != "" {
					id = found
					return cleanDeliveryText(text)
				}
				return text
			}
			switch v := content.(type) {
			case string:
				content = clean(v)
			case []any:
				for _, item := range v {
					if b, ok := item.(map[string]any); ok && b["type"] == "text" {
						if text, ok := b["text"].(string); ok {
							b["text"] = clean(text)
						}
					}
				}
			}
			if d, ok := a.deliveries[id]; ok && d.Session == p.deliverySession() {
				if d.State != "consumed" {
					d.State, d.Error = "consumed", ""
					events = append(events, m.deliveryLocked(a, d))
				}
				e.Message["content"], _ = json.Marshal(content)
				e.Message["yesmanDeliveryId"], _ = json.Marshal(id)
				var record map[string]json.RawMessage
				_ = json.Unmarshal(raw, &record)
				record["message"], _ = json.Marshal(e.Message)
				raw, _ = json.Marshal(record)
				if kind == "message_end" {
					d = a.deliveries[id]
					d.Text, d.Images = "", nil
					a.deliveries[id] = d
				}
			}
		}
	}
	m.mu.Unlock()
	for _, ev := range events {
		m.cfg.OnEvent(ev)
	}
	return raw
}

func (m *Manager) interruptDeliveriesLocked(a *agent) []Event {
	var events []Event
	for _, d := range a.deliveries {
		if d.State == "sending" || d.State == "accepted" || d.State == "steering" || d.State == "queued" || d.State == "unconfirmed" {
			d.State, d.Error = "interrupted", ""
			events = append(events, m.deliveryLocked(a, d))
		}
	}
	return events
}

// deliveryCapability is an extension UI status record, not a pi RPC command.
// It confirms that the bundled transform loaded and identifies session changes
// within a process (for example an extension's /new command).
func (m *Manager) deliveryCapability(a *agent, p *proc, kind string, raw json.RawMessage) {
	if kind != "extension_ui_request" {
		return
	}
	var status struct {
		Method string `json:"method"`
		Key    string `json:"statusKey"`
		Value  string `json:"statusText"`
	}
	if json.Unmarshal(raw, &status) != nil || status.Method != "setStatus" || status.Key != "yesman.delivery" {
		return
	}
	var capability struct {
		Version int    `json:"version"`
		Session string `json:"session"`
	}
	if json.Unmarshal([]byte(status.Value), &capability) != nil || capability.Version != 1 || capability.Session == "" {
		return
	}
	m.mu.Lock()
	if a.proc != nil && a.proc != p {
		m.mu.Unlock()
		return
	}
	p.piSession.Store(capability.Session)
	p.deliveryReady.Store(true)
	var events []Event
	for _, d := range a.deliveries {
		if d.Session != "" && d.Session != p.deliverySession() && (d.State == "sending" || d.State == "accepted" || d.State == "steering" || d.State == "queued" || d.State == "unconfirmed") {
			d.State = "interrupted"
			events = append(events, m.deliveryLocked(a, d))
		}
	}
	m.mu.Unlock()
	for _, ev := range events {
		m.cfg.OnEvent(ev)
	}
}

// A command acknowledgement cannot override a later stage already observed
// on this reader (for example message_start before the prompt response).
func (m *Manager) deliveryResponse(a *agent, p *proc, id string, r response) {
	if id == "" {
		return
	}
	m.mu.Lock()
	d, ok := a.deliveries[id]
	if !ok || a.proc != p || d.Session != p.deliverySession() || d.State == "consumed" || d.State == "interrupted" {
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
	case d.State == "sending":
		d.State = "accepted"
	default:
		m.mu.Unlock()
		return
	}
	ev := m.deliveryLocked(a, d)
	m.mu.Unlock()
	m.cfg.OnEvent(ev)
}
