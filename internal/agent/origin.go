package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Origin says who wrote a user message. pi's user messages carry no source:
// the user's submissions, the lead's messages to its workers and yesman's own
// notices (worker reports and questions, stopped runs, open tasks) all reach
// pi as ordinary prompts, distinguishable only by their text.
//
// The app tells them apart from pi's own records of each prompt, in the order
// it reads them (originTracker), never from the text. pi's messages are left
// as they are: what the model, the session's messages and other extensions'
// context and provider hooks see is unchanged. The origin is kept beside
// them: yesman's extension records it in pi's session as a custom entry
// (originEntry), which pi never puts in a model's context, and the app adds it
// to the messages it serves the window, live and from history, under the
// message's "yesman" field.
type Origin struct {
	Source string `json:"source"`         // OriginUser, OriginLead or OriginYesman
	Kind   string `json:"kind,omitempty"` // lead: "task" or "message"; yesman: "report", "question", "stopped", "failed" or "tasks"
	// Worker names the worker a yesman notice is about.
	Worker string `json:"worker,omitempty"`
	// Submission is the receipt ID of a user's submission from the window.
	Submission string `json:"submission,omitempty"`
}

const (
	OriginUser   = "user"   // a person, from the window
	OriginLead   = "lead"   // a worker's lead: its task, or a message to it
	OriginYesman = "yesman" // yesman itself, telling a lead about its workers
)

// originEntry is the custom entry type under which yesman's extension records
// a user message's origin in pi's session, just before pi appends the message:
// {"timestamp": the message's, "origin": Origin}.
const originEntry = "yesman-origin"

const (
	steeringQueue = iota
	followUpQueue
)

// queued is a message on one of pi's queues, as queue_update showed it pushed.
type queued struct {
	text     string
	queue    int
	claimed  bool   // a prompt's answer said it was this push
	origin   Origin // the claiming prompt's
	poisoned bool   // it cannot be told apart from another push
}

// delivered is the user message pi has started and not yet ended.
type delivered struct {
	timestamp float64
	digest    [32]byte // of the message's JSON, as pi wrote it
	origin    *Origin
}

type originSlot struct {
	origin Origin
	known  bool
	mixed  bool // two messages with the same JSON disagree
}

// originTracker decides who wrote each user message of one pi process from
// pi's records, as the process's reader sees them, in order:
//
//   - A prompt sent while pi is running passes every input handler, is pushed
//     onto the steering or follow-up queue (queue_update shows the final text)
//     and is then answered "queued". Among the pushes since prompts were in
//     flight, an answer finds its own when exactly one push to its queue is
//     still unexplained.
//   - A prompt that starts a run is answered "started" before the run's first
//     user message.
//   - pi takes a queued message off its queue (another queue_update) just
//     before that message starts.
//
// Whatever these records cannot tell apart gets no origin, and the window
// shows it as a person's: two queued messages with the same final text from
// different writers, pushes another extension made, pushes no single answer
// explains. Hooks that rewrite, replace or swallow input change only which
// final text is pushed, so they cannot move an origin to another message.
type originTracker struct {
	mu        sync.Mutex
	native    bool         // capability fixed for this process; native inputs never use text heuristics
	inflight  int          // prompts sent and not yet answered
	lost      bool         // a queue changed in a way not followed; nothing is claimed until no prompt is in flight
	queues    [2][]*queued // pi's queues, mirrored from queue_update
	unclaimed []*queued    // pushed while prompts were in flight, not yet explained by an answer
	removed   *queued      // taken off a queue for the user message starting next
	started   *Origin      // the prompt that started the run, for its first user message
	current   *delivered   // the user message between its start and end

	// index holds the origins of user messages by the digest of their JSON,
	// for history: those decided here, and those pi's session records.
	index   map[[32]byte]originSlot
	scanned string     // the pi session whose entries the index holds
	scanMu  sync.Mutex // held while reading a session's entries
}

const maxOriginIndex = 20000

// sent counts a prompt about to be written to pi.
func (t *originTracker) sent() {
	t.mu.Lock()
	t.inflight++
	t.mu.Unlock()
}

// answered settles a prompt sent with origin o: r is pi's answer, or nil if
// none will come.
func (t *originTracker) answered(o Origin, behavior string, r *response) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inflight--
	if r != nil && r.Success {
		var data struct {
			Disposition string `json:"disposition"`
		}
		_ = json.Unmarshal(r.Data, &data)
		switch data.Disposition {
		case "queued":
			q := steeringQueue
			if behavior == "followUp" {
				q = followUpQueue
			}
			var pushes []*queued
			for _, e := range t.unclaimed {
				if e.queue == q {
					pushes = append(pushes, e)
				}
			}
			for _, e := range pushes {
				if len(pushes) == 1 && !t.lost && !e.poisoned {
					e.claimed, e.origin = true, o
				} else {
					e.poisoned = true
				}
			}
			t.unclaimed = slices.DeleteFunc(t.unclaimed, func(e *queued) bool { return e.queue == q })
		case "started":
			t.started, t.removed = &o, nil
		}
	}
	if t.inflight <= 0 {
		// Pushes no prompt in flight can explain are another extension's.
		t.inflight, t.lost = 0, false
		for _, e := range t.unclaimed {
			e.poisoned = true
		}
		t.unclaimed = nil
	}
}

// observe follows one record of the process, returning it as the window
// should get it: user messages with their origin, if known, and never with an
// origin field that did not come from here.
func (t *originTracker) observe(kind string, raw json.RawMessage) json.RawMessage {
	if t.native {
		if kind == "message_start" || kind == "message_end" {
			return projectNativeEvent(raw)
		}
		return raw
	}
	switch kind {
	case "queue_update":
		var e struct {
			Steering []string `json:"steering"`
			FollowUp []string `json:"followUp"`
		}
		if json.Unmarshal(raw, &e) == nil {
			t.mu.Lock()
			t.queueUpdate([2][]string{e.Steering, e.FollowUp})
			t.mu.Unlock()
		}
	case "agent_start", "agent_end", "agent_settled":
		// Queued messages start inside a run, each right after its removal;
		// a removal left over is from clearing a queue.
		t.mu.Lock()
		t.removed = nil
		if kind != "agent_start" {
			t.started = nil
		}
		t.mu.Unlock()
	case "message_start", "message_end":
		return t.message(kind, raw)
	}
	return raw
}

// queueUpdate follows pi's queues: each update pushes or removes one message,
// or clears them.
func (t *originTracker) queueUpdate(lists [2][]string) {
	for q, now := range lists {
		old := t.queues[q]
		switch {
		case sameTexts(old, now):
		case len(now) == len(old)+1 && sameTexts(old, now[:len(old)]):
			e := &queued{text: now[len(old)], queue: q}
			if t.inflight > 0 && !t.lost {
				t.unclaimed = append(t.unclaimed, e)
			} else {
				e.poisoned = true
			}
			t.queues[q] = append(slices.Clip(old), e)
		case len(now) == len(old)-1 && removedAt(old, now) >= 0:
			i := removedAt(old, now)
			t.removed = old[i]
			t.queues[q] = slices.Delete(slices.Clone(old), i, i+1)
		default:
			// Cleared, or changed in a way not followed: what remains is
			// not known.
			if len(now) > 0 {
				t.lost = true
			}
			t.removed = nil
			fresh := make([]*queued, len(now))
			for i, text := range now {
				fresh[i] = &queued{text: text, queue: q, poisoned: true}
			}
			t.queues[q] = fresh
		}
	}
}

func sameTexts(a []*queued, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].text != b[i] {
			return false
		}
	}
	return true
}

// removedAt is the index of old's message missing from now, the first of
// equal ones as pi removes it, or -1.
func removedAt(old []*queued, now []string) int {
	i := 0
	for i < len(now) && old[i].text == now[i] {
		i++
	}
	if !sameTexts(old[i+1:], now[i:]) {
		return -1
	}
	return i
}

func sameWriter(a, b Origin) bool {
	return a.Source == b.Source && a.Kind == b.Kind && a.Worker == b.Worker
}

// message decides the origin of a user message as it starts, and gives it to
// the window with both its start and its end.
func (t *originTracker) message(kind string, raw json.RawMessage) json.RawMessage {
	var e struct {
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return raw
	}
	var head struct {
		Role      string          `json:"role"`
		Timestamp float64         `json:"timestamp"`
		Content   json.RawMessage `json:"content"`
	}
	if json.Unmarshal(e.Message, &head) != nil || head.Role != "user" {
		return raw
	}
	digest := sha256.Sum256(e.Message)
	t.mu.Lock()
	var o *Origin
	if kind == "message_start" {
		switch {
		case t.removed != nil:
			o = t.deliveredLocked(t.removed, firstText(head.Content))
		case t.started != nil:
			o = t.started
		}
		if o != nil && o.Source == "" {
			o = nil // sent without saying who wrote it
		}
		t.removed, t.started = nil, nil
		t.current = &delivered{timestamp: head.Timestamp, digest: digest, origin: o}
	} else {
		if t.current != nil {
			o = t.current.origin
		}
		t.current = nil
		t.rememberLocked(digest, o)
	}
	t.mu.Unlock()
	return withOrigin(raw, e.Message, o)
}

// deliveredLocked is the origin of the message pi took off a queue, whose
// text starts with the text pushed. pi delivers in order but names the
// message it takes by its text, so any other queued message with the same text
// could be the one delivered: all of them must agree, and they lose their
// origins if they do not. Which receipt each was is not known either way.
func (t *originTracker) deliveredLocked(e *queued, text string) *Origin {
	same := []*queued{e}
	for _, q := range t.queues {
		for _, x := range q {
			if x.text == e.text {
				same = append(same, x)
			}
		}
	}
	agree := strings.HasPrefix(text, e.text)
	for _, x := range same {
		if !x.claimed || x.poisoned || !sameWriter(x.origin, e.origin) {
			agree = false
		}
	}
	if !agree {
		for _, x := range same[1:] {
			x.poisoned = true
		}
		return nil
	}
	o := e.origin
	if len(same) > 1 {
		o.Submission = ""
		for _, x := range same[1:] {
			x.origin.Submission = ""
		}
	}
	return &o
}

func firstText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(content, &blocks)
	for _, b := range blocks {
		if b.Type == "text" {
			return b.Text
		}
	}
	return ""
}

func (t *originTracker) rememberLocked(digest [32]byte, o *Origin) {
	if t.index == nil || len(t.index) >= maxOriginIndex {
		// Forgotten origins are read again from the session's entries.
		t.index, t.scanned = map[[32]byte]originSlot{}, ""
	}
	slot := originSlot{}
	if o != nil {
		slot.origin, slot.known = *o, true
	}
	if old, ok := t.index[digest]; ok && old != slot {
		slot = originSlot{mixed: true}
	}
	t.index[digest] = slot
}

// ending is the origin of the user message the reader has started and not yet
// ended, if it is the one with this timestamp and digest.
func (t *originTracker) ending(timestamp float64, digest [32]byte) (Origin, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.current
	if c == nil || c.origin == nil || c.timestamp != timestamp || c.digest != digest {
		return Origin{}, false
	}
	return *c.origin, true
}

// needsScan reports whether the session's entries must be read for its
// history's origins. Callers hold scanMu, so that a second reader waits for
// the first rather than serving history without them.
func (t *originTracker) needsScan(session string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return session != "" && t.scanned != session
}

// indexEntries records the origins pi's session holds, from get_entries: the
// custom entry the extension appended while pi ended a user message is that
// message's parent, or separated from it only by entries that are not
// messages.
func (t *originTracker) indexEntries(session string, data json.RawMessage) {
	var all struct {
		Entries []json.RawMessage `json:"entries"`
	}
	ok := json.Unmarshal(data, &all) == nil
	type entry struct {
		Type       string          `json:"type"`
		ID         string          `json:"id"`
		ParentID   string          `json:"parentId"`
		CustomType string          `json:"customType"`
		Data       json.RawMessage `json:"data"`
		Message    json.RawMessage `json:"message"`
	}
	entries := make([]entry, 0, len(all.Entries))
	byID := map[string]*entry{}
	for _, raw := range all.Entries {
		var e entry
		if json.Unmarshal(raw, &e) == nil {
			entries = append(entries, e)
		}
	}
	for i := range entries {
		byID[entries[i].ID] = &entries[i]
	}
	type found struct {
		digest [32]byte
		origin Origin
	}
	var origins []found
	for _, e := range entries {
		if e.Type != "message" {
			continue
		}
		var head struct {
			Role      string  `json:"role"`
			Timestamp float64 `json:"timestamp"`
		}
		if json.Unmarshal(e.Message, &head) != nil || head.Role != "user" {
			continue
		}
		id := e.ParentID
		for hops := 0; hops < 16 && id != ""; hops++ {
			parent := byID[id]
			if parent == nil || parent.Type == "message" {
				break
			}
			if parent.Type == "custom" && parent.CustomType == originEntry {
				var d struct {
					Timestamp float64         `json:"timestamp"`
					Origin    json.RawMessage `json:"origin"`
				}
				if json.Unmarshal(parent.Data, &d) == nil && d.Timestamp == head.Timestamp {
					if o, ok := validOrigin(d.Origin); ok {
						origins = append(origins, found{sha256.Sum256(e.Message), o})
					}
				}
				break
			}
			id = parent.ParentID
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, f := range origins {
		t.rememberLocked(f.digest, &f.origin)
	}
	if ok {
		t.scanned = session
	}
}

// project adds to the user messages of a get_messages answer the origins
// known for them, and removes any origin field pi's own messages carry.
func (t *originTracker) project(raw json.RawMessage) json.RawMessage {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return raw
	}
	changed := false
	t.mu.Lock()
	for i, m := range list {
		var head struct {
			Role   string          `json:"role"`
			Yesman json.RawMessage `json:"yesman"`
		}
		if json.Unmarshal(m, &head) != nil || head.Role != "user" {
			continue
		}
		var o *Origin
		if slot, ok := t.index[sha256.Sum256(m)]; ok && slot.known && !slot.mixed {
			o = &slot.origin
		}
		if o == nil && head.Yesman == nil {
			continue
		}
		list[i] = withField(m, o)
		changed = true
	}
	t.mu.Unlock()
	if !changed {
		return raw
	}
	var b bytes.Buffer
	b.WriteByte('[')
	for i, m := range list {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(m)
	}
	b.WriteByte(']')
	return b.Bytes()
}

// withOrigin is an event with its message's origin set to o, or removed.
func withOrigin(raw, message json.RawMessage, o *Origin) json.RawMessage {
	var head struct {
		Yesman json.RawMessage `json:"yesman"`
	}
	if o == nil && (json.Unmarshal(message, &head) != nil || head.Yesman == nil) {
		return raw
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return raw
	}
	fields["message"] = withField(message, o)
	out, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return out
}

// withField is a message with its "yesman" field set to o, or removed.
func withField(message json.RawMessage, o *Origin) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(message, &fields) != nil {
		return message
	}
	delete(fields, "yesman")
	if o != nil {
		fields["yesman"], _ = json.Marshal(o)
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return message
	}
	return out
}

var (
	originKindRE       = regexp.MustCompile(`^[a-z]{1,16}$`)
	originWorkerRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	originSubmissionRE = regexp.MustCompile(`^[a-f0-9]{48}$`)
)

// validOrigin reads an origin a session recorded, keeping only the bounded
// fields the window reads.
func validOrigin(raw json.RawMessage) (Origin, bool) {
	var o Origin
	if json.Unmarshal(raw, &o) != nil {
		return Origin{}, false
	}
	switch o.Source {
	case OriginUser, OriginLead, OriginYesman:
	default:
		return Origin{}, false
	}
	if !originKindRE.MatchString(o.Kind) {
		o.Kind = ""
	}
	if !originWorkerRE.MatchString(o.Worker) {
		o.Worker = ""
	}
	if !originSubmissionRE.MatchString(o.Submission) {
		o.Submission = ""
	}
	return o, true
}

// MessageOrigin answers yesman's extension, for the process holding token,
// with the origin of the user message pi is ending: the one with this
// timestamp and whose JSON has this SHA-256 digest. pi has written that
// message's start before the extension asks, and writes its end only after
// the answer, so once pi answers another command (a barrier: pi answers after
// writing every earlier record, and the reader handles them in order) the
// reader stands between the two. Whatever it then holds is that message, or
// the question was not about the message pi is ending, and has no answer.
func (m *Manager) MessageOrigin(ctx context.Context, token string, timestamp float64, digest string) (Origin, bool) {
	sum, err := hex.DecodeString(digest)
	if token == "" || err != nil || len(sum) != sha256.Size {
		return Origin{}, false
	}
	m.mu.Lock()
	var p *proc
	if id, ok := m.tokens[token]; ok {
		if a := m.agents[id]; a != nil && a.token == token {
			p = a.proc
		}
	}
	m.mu.Unlock()
	if p == nil {
		return Origin{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var o Origin
	var ok bool
	if _, err := p.callAt(ctx, map[string]any{"type": "get_state"}, func() { o, ok = p.origins.ending(timestamp, [32]byte(sum)) }); err != nil {
		return Origin{}, false
	}
	return o, ok
}
