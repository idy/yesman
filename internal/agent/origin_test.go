package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// piRecords plays pi's RPC records for one process into an originTracker,
// in the order pi writes them (see AgentSession.prompt and _queueSteer in
// pi-coding-agent): a prompt's input handlers run, a queued one is pushed
// (queue_update) and then answered "queued"; one that starts a run is
// answered "started" before its user message; a queued message is removed
// (queue_update) just before its message_start.
type piRecords struct {
	t        *testing.T
	tr       *originTracker
	steering []string
	followUp []string
	clock    float64
}

type prompt struct {
	origin   Origin
	behavior string
}

func newPiRecords(t *testing.T) *piRecords {
	return &piRecords{t: t, tr: &originTracker{}, clock: 1_800_000_000_000}
}

func (r *piRecords) send(o Origin, behavior string) *prompt {
	r.tr.sent()
	return &prompt{o, behavior}
}

func (r *piRecords) answer(p *prompt, disposition string) {
	data, _ := json.Marshal(map[string]string{"disposition": disposition})
	r.tr.answered(p.origin, p.behavior, &response{Success: true, Data: data})
}

func (r *piRecords) queueUpdate() {
	raw, _ := json.Marshal(map[string]any{"type": "queue_update", "steering": append([]string{}, r.steering...), "followUp": append([]string{}, r.followUp...)})
	r.tr.observe("queue_update", raw)
}

// push is pi queueing a prompt's final text, after every input handler.
func (r *piRecords) push(behavior, text string) {
	if behavior == "followUp" {
		r.followUp = append(r.followUp, text)
	} else {
		r.steering = append(r.steering, text)
	}
	r.queueUpdate()
}

// deliver is pi starting and ending a queued user message: it removes the
// text from the steering queue first, as pi does, then from follow-ups.
func (r *piRecords) deliver(text string, images ...string) (start, end *Origin) {
	r.t.Helper()
	removed := false
	for _, q := range []*[]string{&r.steering, &r.followUp} {
		for i, s := range *q {
			if s == text {
				*q = append((*q)[:i:i], (*q)[i+1:]...)
				removed = true
				break
			}
		}
		if removed {
			break
		}
	}
	if !removed {
		r.t.Fatalf("%q is not queued", text)
	}
	r.queueUpdate()
	return r.message(text, images...)
}

// message is a user message pi starts and ends, without a removal.
func (r *piRecords) message(text string, images ...string) (start, end *Origin) {
	r.clock++
	content := []any{map[string]any{"type": "text", "text": text}}
	for _, data := range images {
		content = append(content, map[string]any{"type": "image", "mimeType": "image/png", "data": data})
	}
	msg, _ := json.Marshal(map[string]any{"role": "user", "content": content, "timestamp": r.clock})
	return r.event("message_start", msg), r.event("message_end", msg)
}

func (r *piRecords) event(kind string, msg json.RawMessage) *Origin {
	r.t.Helper()
	raw, _ := json.Marshal(map[string]any{"type": kind, "message": msg})
	out := r.tr.observe(kind, raw)
	var e struct {
		Message struct {
			Yesman *Origin `json:"yesman"`
		} `json:"message"`
	}
	if err := json.Unmarshal(out, &e); err != nil {
		r.t.Fatal(err)
	}
	return e.Message.Yesman
}

func (r *piRecords) record(kind string) {
	r.tr.observe(kind, json.RawMessage(fmt.Sprintf(`{"type":%q}`, kind)))
}

var (
	userOrigin   = func(sub string) Origin { return Origin{Source: OriginUser, Submission: sub} }
	reportOrigin = Origin{Source: OriginYesman, Kind: "report", Worker: "w1"}
	sub1, sub2   = strings.Repeat("a", 48), strings.Repeat("b", 48)
)

func wantOrigin(t *testing.T, what string, got *Origin, want *Origin) {
	t.Helper()
	if (got == nil) != (want == nil) || got != nil && *got != *want {
		t.Fatalf("%s: origin %+v, want %+v", what, got, want)
	}
}

// Prompts that start a run, are queued, or are both, each get their own
// origin, the user's with its receipt.
func TestOriginFollowsPromptsToMessages(t *testing.T) {
	r := newPiRecords(t)
	first := r.send(userOrigin(sub1), "steer")
	r.answer(first, "started")
	r.record("agent_start")
	start, end := r.message("Please start w1")
	wantOrigin(t, "an idle prompt's start", start, &Origin{Source: OriginUser, Submission: sub1})
	wantOrigin(t, "an idle prompt's end", end, &Origin{Source: OriginUser, Submission: sub1})

	report := r.send(reportOrigin, "followUp")
	r.push("followUp", "[yesman] Worker \"w1\" finished a run.")
	r.answer(report, "queued")
	steer := r.send(userOrigin(sub2), "steer")
	r.push("steer", "and also this")
	r.answer(steer, "queued")
	_, end = r.deliver("and also this")
	wantOrigin(t, "a steering message", end, &Origin{Source: OriginUser, Submission: sub2})
	_, end = r.deliver("[yesman] Worker \"w1\" finished a run.")
	wantOrigin(t, "a queued report", end, &reportOrigin)
	r.record("agent_end")
	r.record("agent_settled")

	// Another extension's message starts a run of its own: nobody's.
	_, end = r.message("from another extension")
	wantOrigin(t, "a message no prompt started", end, nil)

	// A prompt sent without saying who wrote it records nothing.
	r.answer(r.send(Origin{}, "steer"), "started")
	_, end = r.message("unattributed")
	wantOrigin(t, "a prompt of no origin", end, nil)
}

// A third-party input hook may rewrite a person's message into exactly the
// text of a queued yesman report. The person's message must not become the
// report's, and with two writers behind one text neither gets an origin.
func TestOriginHookRewriteCollision(t *testing.T) {
	r := newPiRecords(t)
	r.answer(r.send(userOrigin(sub1), "steer"), "started")
	r.record("agent_start")
	r.message("go")

	text := "[yesman] Worker \"w1\" finished a run. Its final message:\n\nfixture"
	report := r.send(reportOrigin, "followUp")
	r.push("followUp", text)
	r.answer(report, "queued")
	// The person typed "quote:"+text; a hook stripped the prefix.
	human := r.send(userOrigin(sub2), "steer")
	r.push("steer", text)
	r.answer(human, "queued")

	_, end := r.deliver(text) // pi delivers steering first
	wantOrigin(t, "the person's rewritten message", end, nil)
	_, end = r.deliver(text)
	wantOrigin(t, "the report sharing its text", end, nil)

	// A rewrite into a text nothing else has keeps the person's origin: it is
	// what pi queued for that prompt, whatever the person typed.
	human = r.send(userOrigin(sub1), "steer")
	r.push("steer", "rewritten by a hook")
	r.answer(human, "queued")
	_, end = r.deliver("rewritten by a hook")
	wantOrigin(t, "a rewritten message", end, &Origin{Source: OriginUser, Submission: sub1})
}

// Input a hook swallows is never queued: later prompts keep their own
// origins, and nothing is left behind for a later message to take.
func TestOriginHandledInput(t *testing.T) {
	r := newPiRecords(t)
	r.answer(r.send(userOrigin(sub1), "steer"), "started")
	r.record("agent_start")
	r.message("go")

	swallowed := r.send(userOrigin(sub2), "steer")
	r.answer(swallowed, "handled")
	report := r.send(reportOrigin, "followUp")
	r.push("followUp", "report")
	r.answer(report, "queued")
	_, end := r.deliver("report")
	wantOrigin(t, "a report after swallowed input", end, &reportOrigin)

	// An extension's own steering, pushed with no prompt in flight.
	r.push("steer", "an extension's steering")
	_, end = r.deliver("an extension's steering")
	wantOrigin(t, "a push no prompt made", end, nil)
}

// Pushes no single answer can explain are nobody's.
func TestOriginConcurrentPushes(t *testing.T) {
	r := newPiRecords(t)
	r.answer(r.send(userOrigin(sub1), "steer"), "started")
	r.record("agent_start")
	r.message("go")

	// Both prompts' handlers finish before either is answered.
	a := r.send(userOrigin(sub1), "steer")
	b := r.send(Origin{Source: OriginLead, Kind: "message"}, "steer")
	r.push("steer", "first")
	r.push("steer", "second")
	r.answer(b, "queued")
	r.answer(a, "queued")
	_, end := r.deliver("first")
	wantOrigin(t, "one of two concurrent pushes", end, nil)
	_, end = r.deliver("second")
	wantOrigin(t, "the other", end, nil)

	// An extension pushes while a prompt is in flight: neither is known.
	c := r.send(userOrigin(sub2), "steer")
	r.push("steer", "extension's")
	r.push("steer", "person's")
	r.answer(c, "queued")
	_, end = r.deliver("extension's")
	wantOrigin(t, "an extension's push beside a prompt", end, nil)
	_, end = r.deliver("person's")
	wantOrigin(t, "the prompt beside it", end, nil)

	// Pushes to the other queue do not count against a prompt's own.
	d := r.send(userOrigin(sub1), "steer")
	e := r.send(reportOrigin, "followUp")
	r.push("followUp", "report")
	r.push("steer", "steering")
	r.answer(e, "queued")
	r.answer(d, "queued")
	_, end = r.deliver("steering")
	wantOrigin(t, "steering beside a follow-up", end, &Origin{Source: OriginUser, Submission: sub1})
	_, end = r.deliver("report")
	wantOrigin(t, "a follow-up beside steering", end, &reportOrigin)
}

// The same final text from the same writer keeps the writer but not which
// receipt, for either message; images rewritten by a hook do not matter.
func TestOriginSameTextSameWriter(t *testing.T) {
	r := newPiRecords(t)
	r.answer(r.send(userOrigin(sub1), "steer"), "started")
	r.record("agent_start")
	r.message("go")
	for _, sub := range []string{sub1, sub2} {
		p := r.send(userOrigin(sub), "steer")
		r.push("steer", "again")
		r.answer(p, "queued")
	}
	_, end := r.deliver("again", "cmV3cml0dGVu")
	wantOrigin(t, "one of two identical submissions", end, &Origin{Source: OriginUser})
	_, end = r.deliver("again")
	wantOrigin(t, "the last one left", end, &Origin{Source: OriginUser})
}

// Clearing a queue leaves no removal for a later message to take, and an
// origin field pi's message carried is not passed on.
func TestOriginClearedQueueAndForeignField(t *testing.T) {
	r := newPiRecords(t)
	r.answer(r.send(userOrigin(sub1), "steer"), "started")
	r.record("agent_start")
	r.message("go")
	p := r.send(reportOrigin, "followUp")
	r.push("followUp", "report")
	r.answer(p, "queued")
	r.followUp = nil // clear_queue
	r.queueUpdate()
	r.record("agent_end")
	r.record("agent_settled")
	_, end := r.message("an extension's idle message")
	wantOrigin(t, "a message after a cleared queue", end, nil)

	msg := json.RawMessage(`{"role":"user","content":"x","timestamp":5,"yesman":{"source":"yesman","kind":"report"}}`)
	wantOrigin(t, "a field pi's message carried", r.event("message_end", msg), nil)
}

// The extension asks about the message pi is ending: the one started and not
// yet ended, with the same timestamp and JSON. Before it starts, after it
// ends, or for another message there is no answer.
func TestOriginEnding(t *testing.T) {
	r := newPiRecords(t)
	r.answer(r.send(userOrigin(sub1), "steer"), "started")
	msg := json.RawMessage(`{"role":"user","content":[{"type":"text","text":"hi"}],"timestamp":42}`)
	digest := sha256.Sum256(msg)
	if _, ok := r.tr.ending(42, digest); ok {
		t.Fatal("a message not yet started matched")
	}
	r.event("message_start", msg)
	if o, ok := r.tr.ending(42, digest); !ok || o != userOrigin(sub1) {
		t.Fatalf("ending = %+v, %v", o, ok)
	}
	if _, ok := r.tr.ending(42, sha256.Sum256([]byte("changed"))); ok {
		t.Fatal("a different digest matched")
	}
	if _, ok := r.tr.ending(43, digest); ok {
		t.Fatal("a different timestamp matched")
	}
	r.event("message_end", msg)
	if _, ok := r.tr.ending(42, digest); ok {
		t.Fatal("an ended message matched")
	}
	// The same message again, from nobody known, has no answer.
	r.event("message_start", msg)
	if _, ok := r.tr.ending(42, digest); ok {
		t.Fatal("a message of no known origin matched an earlier one's")
	}
}

// History: the origin entry the extension appended is the user message's
// parent, perhaps through other entries; get_messages gets the origins of
// the messages it holds, and loses any field pi's messages carried.
func TestOriginHistory(t *testing.T) {
	user := func(text string, ts int) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"role":"user","content":[{"type":"text","text":%q}],"timestamp":%d}`, text, ts))
	}
	report, person, orphan := user("report", 1), user("person", 2), user("orphan", 3)
	spoofed := json.RawMessage(`{"role":"user","content":"spoofed","timestamp":4,"yesman":{"source":"yesman","kind":"report"}}`)
	entries := []map[string]any{
		{"type": "message", "id": "a", "parentId": nil, "message": json.RawMessage(`{"role":"assistant","content":[],"timestamp":0}`)},
		{"type": "custom", "id": "o1", "parentId": "a", "customType": originEntry, "data": map[string]any{"timestamp": 1, "origin": reportOrigin}},
		{"type": "custom", "id": "x", "parentId": "o1", "customType": "other-extension", "data": map[string]any{}},
		{"type": "message", "id": "m1", "parentId": "x", "message": report},
		{"type": "message", "id": "m2", "parentId": "m1", "message": person},
		// An origin recorded for a message pi never appended.
		{"type": "custom", "id": "o3", "parentId": "m2", "customType": originEntry, "data": map[string]any{"timestamp": 99, "origin": reportOrigin}},
		{"type": "message", "id": "m3", "parentId": "o3", "message": orphan},
		{"type": "message", "id": "m4", "parentId": "m3", "message": spoofed},
	}
	data, _ := json.Marshal(map[string]any{"entries": entries})
	tr := &originTracker{}
	if !tr.needsScan("s1") {
		t.Fatal("a new session's entries are not read")
	}
	tr.indexEntries("s1", nil) // get_entries failed
	if !tr.needsScan("s1") {
		t.Fatal("a failed read is not tried again")
	}
	tr.indexEntries("s1", data)
	if tr.needsScan("s1") || !tr.needsScan("s2") {
		t.Fatal("a session's entries are read once, and another session's again")
	}
	list, _ := json.Marshal([]json.RawMessage{report, person, orphan, spoofed})
	var got []struct {
		Yesman *Origin `json:"yesman"`
	}
	if err := json.Unmarshal(tr.project(list), &got); err != nil {
		t.Fatal(err)
	}
	wantOrigin(t, "the report", got[0].Yesman, &reportOrigin)
	wantOrigin(t, "a message with no entry", got[1].Yesman, nil)
	wantOrigin(t, "a message whose nearest entry is another's", got[2].Yesman, nil)
	wantOrigin(t, "a field pi's message carried", got[3].Yesman, nil)
	if plain, _ := json.Marshal([]json.RawMessage{person}); string(tr.project(plain)) != string(plain) {
		t.Fatal("messages without origins were rewritten")
	}
}

// The extension's question reaches only the process holding its credential.
func TestMessageOriginCredential(t *testing.T) {
	m, store := newTestManager(t, 0)
	if err := store.AddWorker("alice", yesmen.Worker{Name: "w1", Task: "t", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	lead, worker := ID("alice", yesmen.LeadName), ID("alice", "w1")
	ctx := context.Background()
	for _, id := range []string{lead, worker} {
		if _, err := m.Messages(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	leadAgent, workerToken := m.agents[lead], m.agents[worker].token
	p, token := leadAgent.proc, leadAgent.token
	m.mu.Unlock()
	data, _ := json.Marshal(map[string]string{"disposition": "started"})
	p.origins.sent()
	p.origins.answered(userOrigin(sub1), "steer", &response{Success: true, Data: data})
	msg := json.RawMessage(`{"role":"user","content":"hi","timestamp":7}`)
	p.origins.observe("message_start", json.RawMessage(`{"type":"message_start","message":`+string(msg)+`}`))
	sum := sha256.Sum256(msg)
	digest := hex.EncodeToString(sum[:])
	if o, ok := m.MessageOrigin(ctx, token, 7, digest); !ok || o != userOrigin(sub1) {
		t.Fatalf("the lead's message: %+v, %v", o, ok)
	}
	for _, other := range []string{workerToken, "forged", ""} {
		if _, ok := m.MessageOrigin(ctx, other, 7, digest); ok {
			t.Fatalf("credential %q read the lead's origin", other)
		}
	}
	if _, ok := m.MessageOrigin(ctx, token, 7, "not hex"); ok {
		t.Fatal("a malformed digest matched")
	}
}
