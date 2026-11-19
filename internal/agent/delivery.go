package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
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
	ID              string    `json:"id"`
	Session         string    `json:"session"`
	Revision        uint64    `json:"revision"`
	State           string    `json:"state"`
	Text            string    `json:"text"`
	Images          []Image   `json:"images,omitempty"` // legacy migration/local fixtures only; new receipts never store image bytes
	ImageCount      int       `json:"imageCount"`
	Preview         bool      `json:"preview,omitempty"`
	Error           string    `json:"error,omitempty"`
	Order           uint64    `json:"order"`
	AgentSince      time.Time `json:"agentSince,omitzero"`
	CanCancel       bool      `json:"canCancel,omitempty"` // view only; never persisted as authority
	CanRestore      bool      `json:"canRestore,omitempty"`
	CanRecover      bool      `json:"canRecover,omitempty"`     // original draft retained after uncertain delivery
	AwaitingRecord  bool      `json:"awaitingRecord,omitempty"` // native take precedes the session entry
	OriginalText    bool      `json:"originalText,omitempty"`
	OriginalPayload bool      `json:"originalPayload,omitempty"` // bounded volatile display access only
	NativePending   bool      `json:"nativePending,omitempty"`
	CanPromote      bool      `json:"canPromote,omitempty"` // live native membership/admission only
	// MergedInto names the still-pending submission this input was appended to;
	// pi takes both as one message. Merged lists, in order, the inputs appended to
	// this submission. Both are bounded private receipt links, never pi input.
	MergedInto string   `json:"mergedInto,omitempty"`
	Merged     []string `json:"merged,omitempty"`
	Preparing  bool     `json:"preparing,omitempty"` // view only: claimed by pi's idle preparation, not taken
	// Ack is how pi's answer accepted it (accepted, queued, steering or
	// handled); empty until pi answered, or when it refused. It stays when
	// the state later turns unconfirmed or interrupted.
	Ack string `json:"ack,omitempty"`
	// Origin marks a submission the app made for the user, a scheduled
	// task's run; nil for what a person typed.
	Origin         *Origin `json:"origin,omitempty"`
	queuePreparing bool
	coalesce       bool   // legacy capability 4 may append this composer input
	humanSteer     bool   // the app's human submission, eligible for the single steer slot
	humanStop      uint64 // Go Stop generation at registration
	recoverDraft   bool   // originals retained before dispatch to a managed runtime
	appendRoot     string // the occurrence an in-flight append targeted
	// hostContext is the trusted conversation context the app sent this
	// submission in (a topic's), host-only: pi keeps inputs of different
	// contexts in different occurrences. Never persisted or shown.
	hostContext         string
	queueRevision       uint64
	queueID, queueEpoch string
	boundSeq            uint64
	fingerprint         string
	created             int64
	expires             int64
	registered          uint64
}

// QueueStatus describes the whole native pi queue, not receipt membership.
// Known is false after process/session replacement until pi reports a snapshot.
type QueueStatus struct {
	Known            bool `json:"known"`
	Steering         int  `json:"steering"`
	FollowUp         int  `json:"followUp"`
	RecallSupported  bool `json:"recallSupported,omitempty"`
	PromoteSupported bool `json:"promoteSupported,omitempty"`
}

var deliveryID = regexp.MustCompile(`^[a-f0-9]{48}$`)

func pendingDelivery(state string) bool {
	return state == "sending" || state == "accepted" || state == "steering" || state == "queued"
}
func (a *agent) deliverySnapshot() []Delivery {
	receipts := make([]Delivery, 0, len(a.deliveries))
	for _, d := range a.deliveries {
		receipts = append(receipts, a.deliveryView(d))
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
	d.CanCancel, d.CanRestore, d.OriginalText, d.NativePending, d.CanPromote, d.OriginalPayload, d.Preparing = false, false, false, false, false, false, false
	d.CanRecover = false
	if d.State == "dispatched" && !d.AwaitingRecord || d.State == "failed" && !d.recoverDraft || d.State == "handled" || d.State == "interrupted" && !d.recoverDraft || d.State == "accepted" && !d.recoverDraft {
		d.queueID, d.queuePreparing = "", false
		m.forgetRecallLocked(a, d.ID)
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
	}{"delivery_update", a.deliveryView(d), removed, retained})
	return Event{Agent: a.info.ID, AgentSince: a.info.Since, Seq: a.seq, Data: raw}
}

// SendHuman gives older desktop Send callers the same native slot policy as
// the composer. Callers needing retry safety provide their own ID via Submit.
func (m *Manager) SendHuman(ctx context.Context, id, text string, images []Image) error {
	var nonce [18]byte
	rand.Read(nonce[:])
	d, err := m.Submit(ctx, id, fmt.Sprintf("%012x%x", time.Now().UnixMilli(), nonce), text, images)
	if err != nil {
		return err
	}
	if d.Error != "" {
		return errors.New(d.Error)
	}
	if d.State == "interrupted" || d.State == "unconfirmed" {
		return errors.New("Delivery could not be confirmed. Check the conversation before sending again.")
	}
	return nil
}

// Submit registers a stable client ID before dispatch. Repeating an uncertain
// bridge call returns its existing receipt and never sends the prompt twice.
func (m *Manager) Submit(ctx context.Context, id, messageID, text string, images []Image, expectedSince ...time.Time) (Delivery, error) {
	d, err := m.submitDelivery(ctx, id, messageID, text, images, "steer", submission{humanSteer: true}, expectedSince...)
	return m.submitView(id, d), err
}

// SubmitCoalescing opts into legacy append on managed runtimes predating the
// single-slot human steering protocol. On current runtimes all human sends
// use the same policy and the native queue decides atomically at admission.
func (m *Manager) SubmitCoalescing(ctx context.Context, id, messageID, text string, images []Image, expectedSince ...time.Time) (Delivery, error) {
	d, err := m.submitDelivery(ctx, id, messageID, text, images, "steer", submission{coalesce: true, humanSteer: true}, expectedSince...)
	return m.submitView(id, d), err
}

// SubmitInContext is Submit for a message the user sends within a trusted
// conversation context, such as a topic. The context is host-only: pi records
// it with the input's identity and never lets inputs of different contexts
// share an occurrence; the text and pi's command are otherwise unchanged.
func (m *Manager) SubmitInContext(ctx context.Context, id, messageID, text string, images []Image, hostContext string, expectedSince ...time.Time) (Delivery, error) {
	if !hostContextRE.MatchString(hostContext) {
		return Delivery{}, errors.New("invalid conversation context")
	}
	d, err := m.submitDelivery(ctx, id, messageID, text, images, "steer", submission{context: hostContext, humanSteer: true}, expectedSince...)
	return m.submitView(id, d), err
}

func (m *Manager) submitView(id string, d Delivery) Delivery {
	m.mu.Lock()
	if a := m.agents[id]; a != nil && a.info.Since.Equal(d.AgentSince) {
		if current, ok := a.deliveries[d.ID]; ok {
			d = current
		}
		d = a.deliveryView(d)
	}
	m.mu.Unlock()
	return d
}
func (m *Manager) submit(ctx context.Context, id, messageID, text string, images []Image, behavior string, origin *Origin, expectedSince ...time.Time) (Delivery, error) {
	return m.submitDelivery(ctx, id, messageID, text, images, behavior, submission{origin: origin}, expectedSince...)
}

// submission is what a caller states about a submission beyond its content.
type submission struct {
	origin     *Origin // a scheduled run's; nil for what a person typed
	coalesce   bool    // legacy capability 4 append
	humanSteer bool    // single-slot human admission when advertised by Pi
	context    string  // a trusted host-only conversation context, such as a topic's
}

// hostContextRE bounds a host-only conversation context, as pi's
// pending-queue.js does (CONTEXT_KEY).
var hostContextRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,63}$`)

func (m *Manager) submitDelivery(ctx context.Context, id, messageID, text string, images []Image, behavior string, sub submission, expectedSince ...time.Time) (Delivery, error) {
	origin := sub.origin
	if err := ctx.Err(); err != nil {
		return Delivery{}, err
	}
	if !deliveryID.MatchString(messageID) {
		return Delivery{}, errors.New("invalid message ID")
	}
	if !receiptOrigin(origin) {
		return Delivery{}, errors.New("invalid submission origin")
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
	d := Delivery{ID: messageID, State: "sending", Text: preview, ImageCount: len(images), Preview: truncated, Order: a.deliveryOrder, AgentSince: info.Since, Origin: origin, fingerprint: fingerprint, created: deliveryTimestamp(messageID),
		coalesce: sub.coalesce && origin == nil && sub.context == "", humanSteer: sub.humanSteer && origin == nil, humanStop: a.humanStop, hostContext: sub.context}
	var prior <-chan struct{}
	var next chan struct{}
	if d.humanSteer {
		prior = a.humanTail
		next = make(chan struct{})
		a.humanTail = next
		defer close(next)
	}
	a.deliveries[messageID] = d
	// A new immutable input already owns this generation, even while its
	// first durable write/start is pending. A duplicate above owns no new work.
	a.givenWork = true
	// Only what a person typed can be recalled, edited or shown again in
	// full: a scheduled run's request is the plan's, never the user's draft.
	if origin == nil {
		m.retainRecallLocked(a, messageID, text, images)
	}
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
	if prior != nil {
		select {
		case <-prior:
		case <-m.shutdown:
			return m.failInitialDelivery(a, messageID, errStopped), nil
		}
	}
	m.mu.Lock()
	if d.humanSteer && a.humanStop != d.humanStop {
		m.mu.Unlock()
		return m.failInitialDelivery(a, messageID, errStopped), nil
	}
	if m.agents[id] == a && !a.retiring {
		a.inbox++
	}
	m.mu.Unlock()
	// The user writing to a lead they stopped lifts its pause; what was kept
	// for it follows their message. A scheduled run is not the user writing
	// now: it is sent as before, and leaves their Stop in place.
	flush := func() {}
	if origin == nil {
		flush = m.resumeByUser(id)
	}
	// Only the private receipt ID crosses this Go call. The pi command and
	// user text remain byte-for-byte the same as the original submission path.
	// Who wrote it is decided from pi's records of this prompt and kept
	// beside the message (originTracker); a scheduled run says so.
	written := Origin{Source: OriginUser, Submission: messageID}
	if origin != nil {
		written = Origin{Source: origin.Source, Plan: origin.Plan, Run: origin.Run, Submission: messageID}
	}
	err = m.promptDelivery(context.Background(), id, text, behavior, messageID, a, &y.HiredAt, written, images...)
	flush()
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
	d, ok := a.deliveries[id]
	if !ok || !d.AgentSince.Equal(info.Since) {
		m.mu.Unlock()
		return errors.New("the agent was replaced; reopen its conversation before sending")
	}
	if a.proc != p || a.retiring || d.State != "sending" {
		m.mu.Unlock()
		return errStopped
	}
	var revision uint64
	writer := a.deliveryWriter
	if d.humanSteer && a.nativeQueue.HumanSteer {
		if _, ok := a.recallPayloads[id]; !ok {
			m.mu.Unlock()
			return errors.New("There is no room to retain this draft. Clear a recovered draft or shorten this message.")
		}
		d.recoverDraft = true
		a.humanDraftStore = true
	}
	d.Session, d.boundSeq = p.deliverySession(), a.seq
	a.deliveries[id] = d
	if d.recoverDraft {
		revision = m.persistDeliveriesLocked(a)
	}
	m.mu.Unlock()
	if revision != 0 {
		if err := writer.wait(context.Background(), revision, m.shutdown); err != nil {
			m.deliveryResponse(a, p, id, "steer", response{Error: err.Error()})
			return err
		}
	}
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
	if !ok || a.retiring || a.proc != p || d.Session != p.deliverySession() {
		m.mu.Unlock()
		return
	}
	var data struct {
		Disposition string `json:"disposition"`
		Append      *struct {
			Outcome string `json:"outcome"`
			QueueID string `json:"queueId"`
		} `json:"append"`
	}
	_ = json.Unmarshal(r.Data, &data)
	// pi already holds or took this submission ID: nothing was queued again,
	// and this answer accepted nothing.
	duplicate := data.Disposition == "queued" && data.Append != nil && data.Append.Outcome == "duplicate"
	appended := r.Success && data.Disposition == "queued" && data.Append != nil &&
		(data.Append.Outcome == "appended" || data.Append.Outcome == "already_appended")
	ack := ""
	if r.Success && !duplicate {
		// pi's answer is the one fact of acceptance a receipt has; a later
		// settle or queue snapshot leaves it standing but proves nothing more.
		ack = "accepted"
		switch {
		case data.Disposition == "handled":
			ack = "handled"
		case data.Disposition == "queued" && behavior == "followUp":
			ack = "queued"
		case data.Disposition == "queued":
			ack = "steering"
		}
	}
	if d.State != "sending" {
		// pi's own queue reported this very submission before pi answered
		// the command (native queue identity, or the ordered appended event of
		// an input joined to an occurrence): the answer still records the
		// acceptance, beside the state its queue reported, which stays.
		if ack == "" || d.Ack != "" || !nativeIntake(d.State) {
			m.mu.Unlock()
			return
		}
		d.Ack = ack
		ev := m.deliveryLocked(a, d)
		m.mu.Unlock()
		m.cfg.OnEvent(ev)
		return
	}
	d.Ack = ack
	if appended {
		// The ordered appended/taken events normally reached this receipt first;
		// this covers only their absence. Never a second dispatch.
		if root, ok := m.appendedRootLocked(a, p, d, data.Append.QueueID); ok {
			events := m.mergeChildLocked(a, root, d)
			m.mu.Unlock()
			for _, ev := range events {
				m.cfg.OnEvent(ev)
			}
			return
		}
		d.State = "unconfirmed"
		ev := m.deliveryLocked(a, d)
		m.mu.Unlock()
		m.cfg.OnEvent(ev)
		return
	}
	switch {
	case !r.Success:
		d.State, d.Error = "failed", r.Error
	case duplicate:
		d.State = "unconfirmed"
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

// nativeIntake reports whether a receipt's state came from pi's own queue
// identifying it: in the queue, taken from it, or taken out of it.
func nativeIntake(state string) bool {
	return state == "queued" || state == "steering" || state == "dispatched" || state == "cancelled"
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
		var native struct {
			Pending pendingQueue `json:"pending"`
		}
		if json.Unmarshal(raw, &native) == nil && native.Pending.supported() {
			events := m.applyPendingLocked(a, p, native.Pending)
			m.mu.Unlock()
			for _, event := range events {
				m.cfg.OnEvent(event)
			}
			return
		}
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
		// An appended input follows its occurrence, not run boundaries.
		if d.Session == p.deliverySession() && uncertain && d.queueID == "" && d.MergedInto == "" {
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
	for id, d := range a.deliveries {
		if d.State != "cancelled" && !d.recoverDraft {
			m.forgetRecallLocked(a, id)
		}
		d.queueID, d.queuePreparing = "", false
		a.deliveries[id] = d
	}
	a.nativeQueue = pendingQueue{}
	a.seq++
	raw, _ := json.Marshal(struct {
		Type  string      `json:"type"`
		Queue QueueStatus `json:"queue"`
	}{"delivery_queue", a.queue})
	events := []Event{{Agent: a.info.ID, AgentSince: a.info.Since, Seq: a.seq, Data: raw}}
	for _, d := range a.deliveries {
		if d.Session != "" && (pendingDelivery(d.State) || d.AwaitingRecord) {
			d.State, d.Error, d.AwaitingRecord = "interrupted", "", false
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
