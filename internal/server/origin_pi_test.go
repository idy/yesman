package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

// TestOriginWithPiFixture runs the installed pi through the app's real
// Manager, API and extension, answered by a local pi-messages backend: no
// model, key or user settings. It checks that the window can tell, from the
// structure of pi's own history, the user's submission from the lead's task
// and message to a worker and from yesman's report of that worker:
//
//	YESMAN_TEST_PI_FIXTURE=1 go test ./internal/server -run OriginWithPiFixture -v
func TestOriginWithPiFixture(t *testing.T) {
	var sent atomic.Bool
	f := startPiFixture(t, func(body string) []any {
		if strings.Contains(body, "FIXTURE-SEND-WORKER") && sent.CompareAndSwap(false, true) {
			call := map[string]any{"type": "toolCall", "id": "fixture-send", "name": "yesman_worker_send", "arguments": map[string]any{"name": "w1", "message": "once more"}}
			return []any{map[string]any{"type": "toolcall_start", "contentIndex": 0, "id": "fixture-send", "toolName": "yesman_worker_send"}, map[string]any{"type": "toolcall_end", "contentIndex": 0, "toolCall": call}}
		}
		return []any{map[string]any{"type": "text_start", "contentIndex": 0}, map[string]any{"type": "text_end", "contentIndex": 0, "content": "fixture reply"}}
	})
	m, store := f.m, f.store
	if _, err := store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	lead, worker := agent.ID("alice", yesmen.LeadName), agent.ID("alice", "w1")
	ctx := context.Background()
	users, wait := f.users, f.wait

	// The user's submission, bound to its receipt.
	submission := fmt.Sprintf("%012x%036x", time.Now().UnixMilli(), 1)
	if _, err := m.Submit(ctx, lead, submission, "Please start w1", nil); err != nil {
		t.Fatal(err)
	}
	wait("the lead's first run", func() bool { return f.settled(lead) >= 1 })

	// The lead's task for a worker, then its report back to the lead.
	if _, err := m.StartWorker(ctx, "alice", "w1", "Fixture work", "Reply with one word."); err != nil {
		t.Fatal(err)
	}
	wait("the worker's report", func() bool { return len(users(lead)) >= 2 })
	wait("the lead's second run", func() bool { return f.settled(lead) >= 2 })

	got := users(lead)
	if len(got) != 2 || got[0].Yesman == nil || *got[0].Yesman != (agent.Origin{Source: agent.OriginUser, Submission: submission}) {
		t.Fatalf("the user's message records %+v", got)
	}
	if got[1].Yesman == nil || *got[1].Yesman != (agent.Origin{Source: agent.OriginYesman, Kind: "report", Worker: "w1"}) ||
		!strings.Contains(string(got[1].Content), `[yesman] Worker \"w1\" finished a run`) {
		t.Fatalf("the report records %+v: %s", got[1].Yesman, got[1].Content)
	}
	// The lead invokes its normal authenticated worker tool.
	if err := m.Prompt(ctx, lead, "FIXTURE-SEND-WORKER"); err != nil {
		t.Fatal(err)
	}
	wait("the worker's second message", func() bool { return len(users(worker)) >= 2 })
	gotWorker := users(worker)
	if gotWorker[0].Yesman == nil || *gotWorker[0].Yesman != (agent.Origin{Source: agent.OriginLead, Kind: "task"}) {
		t.Fatalf("the worker's task records %+v", gotWorker[0].Yesman)
	}
	if gotWorker[1].Yesman == nil || *gotWorker[1].Yesman != (agent.Origin{Source: agent.OriginLead, Kind: "message"}) {
		t.Fatalf("the lead's message records %+v", gotWorker[1].Yesman)
	}

	t.Logf("lead: %+v %+v; worker: %+v %+v", *got[0].Yesman, *got[1].Yesman, *gotWorker[0].Yesman, *gotWorker[1].Yesman)
	// After a restart the history still says who wrote what, and no request
	// to the model ever carried it.
	m.Stop(worker)
	if again := users(worker); len(again) < 2 || again[0].Yesman == nil || again[1].Yesman == nil {
		t.Fatalf("a restarted worker lost its origins: %+v", again)
	}
	if bodies := f.requests(); len(bodies) < 3 {
		t.Fatalf("%d model requests", len(bodies))
	}
	f.noOriginSentToModels()
}

// piFixture is the installed pi run by the app's real Manager, API and
// extension, with isolated settings and sessions and a local pi-messages
// backend: no model, key or user settings.
type piFixture struct {
	t     *testing.T
	tmp   string
	srv   *Server
	m     *agent.Manager
	store *yesmen.Store

	mu      sync.Mutex
	bodies  []string
	settles map[string]int
	events  map[string][]json.RawMessage
	// hook, when set, also gets every event, as the app's other listeners do.
	hook func(agent.Event)
}

// startPiFixture starts the app. reply answers a model request with pi-messages
// events (nil: one text, "fixture reply"); extensions are other extensions
// pi loads from its settings, beside yesman's.
func startPiFixture(t *testing.T, reply func(body string) []any, extensions ...string) *piFixture {
	t.Helper()
	if os.Getenv("YESMAN_TEST_PI_FIXTURE") == "" {
		t.Skip("set YESMAN_TEST_PI_FIXTURE=1 to run the installed pi with a local backend")
	}
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	return startPiFixtureWith(t, piPath, nil, reply, extensions...)
}

// startManagedPiFixture is startPiFixture with the pinned runtime the app
// runs by default, prepared as CI prepares it (go run ./tools/pi-runtime
// <cache>, its root in YESMAN_TEST_RUNTIME), instead of an installed pi.
func startManagedPiFixture(t *testing.T, reply func(body string) []any) *piFixture {
	t.Helper()
	root := os.Getenv("YESMAN_TEST_RUNTIME")
	if root == "" {
		t.Skip("set YESMAN_TEST_RUNTIME to a runtime prepared with go run ./tools/pi-runtime <cache>")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(root, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "cli.js")
	if _, err := os.Stat(cli); err != nil {
		t.Fatalf("YESMAN_TEST_RUNTIME is not a prepared runtime: %v", err)
	}
	return startPiFixtureWith(t, node, func(context.Context) (string, []string, error) { return node, []string{cli}, nil }, reply)
}

func startPiFixtureWith(t *testing.T, piPath string, command func(context.Context) (string, []string, error), reply func(body string) []any, extensions ...string) *piFixture {
	t.Helper()
	ext, err := filepath.Abs("../../pi/yesman.ts")
	if err != nil {
		t.Fatal(err)
	}
	f := &piFixture{t: t, tmp: t.TempDir(), settles: map[string]int{}, events: map[string][]json.RawMessage{}}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body bytes.Buffer
		_, _ = body.ReadFrom(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body.String())
		f.mu.Unlock()
		events := []any{
			map[string]any{"type": "text_start", "contentIndex": 0},
			map[string]any{"type": "text_end", "contentIndex": 0, "content": "fixture reply"},
		}
		reason := "stop"
		if reply != nil {
			if events = reply(body.String()); events == nil {
				return // the request was cancelled while held
			}
			for _, e := range events {
				if e.(map[string]any)["type"] == "toolcall_end" {
					reason = "toolUse"
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v any) { b, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", b) }
		usage := map[string]any{"input": 1, "output": 1, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 2,
			"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}}
		send(map[string]any{"type": "start"})
		for _, e := range events {
			send(e)
		}
		send(map[string]any{"type": "done", "reason": reason, "usage": usage})
	}))
	t.Cleanup(backend.Close)

	config := filepath.Join(f.tmp, "pi")
	if err := os.MkdirAll(config, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, v any) {
		data, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(config, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if extensions == nil {
		extensions = []string{}
	}
	write("settings.json", map[string]any{
		"packages": []any{}, "extensions": extensions, "skills": []any{}, "promptTemplates": []any{}, "themes": []any{},
		"compaction": map[string]any{"enabled": false}, "retry": map[string]any{"enabled": false},
		"defaultProjectTrust": "no", "defaultProvider": "fixture", "defaultModel": "m",
	})
	write("models.json", map[string]any{"providers": map[string]any{"fixture": map[string]any{
		"baseUrl": backend.URL, "api": "pi-messages", "apiKey": "fixture",
		"models": []any{map[string]any{"id": "m", "name": "Fixture", "input": []string{"text", "image"},
			"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}, "contextWindow": 100000, "maxTokens": 1000}},
	}}})

	f.store = yesmen.New(filepath.Join(f.tmp, "yesmen"), filepath.Join(f.tmp, "archive"))
	f.srv = &Server{Pool: pool.New(filepath.Join(f.tmp, "repos")), Yesmen: f.store}
	if err := f.srv.Start(); err != nil {
		t.Fatal(err)
	}
	f.m = agent.New(agent.Config{
		Pi:        piPath,
		Command:   command,
		Extension: ext,
		Env: []string{"YESMAN_API=" + f.srv.URL(), "YESMAN_HOME=" + f.tmp, "PI_CODING_AGENT_DIR=" + config,
			"FIXTURE_TRACE=" + filepath.Join(f.tmp, "trace.jsonl"), "FIXTURE_IMAGE=" + fixtureImage},
		Yesmen: f.store,
		OnEvent: func(e agent.Event) {
			var head struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(e.Data, &head)
			f.mu.Lock()
			f.events[e.Agent] = append(f.events[e.Agent], e.Data)
			if head.Type == "agent_settled" {
				f.settles[e.Agent]++
			}
			hook := f.hook
			f.mu.Unlock()
			if hook != nil {
				hook(e)
			}
		},
	})
	f.srv.Agents = f.m
	t.Cleanup(f.m.StopAll)
	return f
}

func (f *piFixture) settled(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settles[id]
}

func (f *piFixture) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

// recorded is the agent's events so far, as the window got them.
func (f *piFixture) recorded(id string) []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]json.RawMessage(nil), f.events[id]...)
}

func (f *piFixture) wait(what string, ok func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			f.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Yesman     *agent.Origin   `json:"yesman"`
	ToolName   string          `json:"toolName"`
	ToolCallID string          `json:"toolCallId"`
	IsError    bool            `json:"isError"`
}

func (f *piFixture) messages(id string) []piMessage {
	f.t.Helper()
	conv, err := f.m.Messages(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	var all []piMessage
	if err := json.Unmarshal(conv.Messages, &all); err != nil {
		f.t.Fatal(err)
	}
	return all
}

func (f *piFixture) users(id string) []piMessage {
	f.t.Helper()
	var out []piMessage
	for _, msg := range f.messages(id) {
		if msg.Role == "user" {
			out = append(out, msg)
		}
	}
	return out
}

// noOriginSentToModels checks no model request carried an origin, as a field
// or as text another extension made of the messages.
func (f *piFixture) noOriginSentToModels() {
	f.t.Helper()
	for _, b := range f.requests() {
		for _, field := range []string{`"yesman":{`, `\"yesman\":{`} {
			if i := strings.Index(b, field); i >= 0 {
				f.t.Errorf("a model request carried an origin: …%.300s", b[i:])
				return
			}
		}
	}
}

// Two small PNGs: what the user attached, and what a hook replaces it with.
const (
	userImage    = "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg=="
	fixtureImage = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
)

// fixtureHooks is a third-party extension with ordinary, supported hooks: an
// input hook that rewrites (text, or text and image) or swallows some input, a context hook that sends
// the model a serialized copy of the user messages it is given, and a tool
// whose results a message_end hook replaces, failing one of them.
const fixtureHooks = `import { appendFileSync } from "node:fs";
export default function (pi) {
	pi.on("input", (event) => {
		if (event.text.startsWith("quote:")) return { action: "transform", text: event.text.slice(6) };
		if (event.text.startsWith("drop:")) return { action: "handled" };
		// Replaces the text and the image, keeping the number of images.
		if (event.text.startsWith("img:")) return { action: "transform", text: event.text.slice(4), images: [{ type: "image", mimeType: "image/png", data: process.env.FIXTURE_IMAGE }] };
	});
	pi.on("context", (event) => {
		const users = event.messages.filter((m) => m.role === "user");
		appendFileSync(process.env.FIXTURE_TRACE, JSON.stringify({ hook: "context", fields: event.messages.some((m) => m && typeof m === "object" && "yesman" in m) }) + "\n");
		return { messages: [...event.messages, { role: "user", content: [{ type: "text", text: "Serialized history: " + JSON.stringify(users) }], timestamp: Date.now() }] };
	});
	pi.registerTool({
		name: "fixture_tool", label: "Fixture tool", description: "Returns synthetic data",
		parameters: { type: "object", properties: {} },
		async execute(_id, _params, _signal, onUpdate) {
			onUpdate?.({ content: [{ type: "text", text: "partial output" }], details: {} });
			return { content: [{ type: "text", text: "before message_end" }], details: {} };
		},
	});
	pi.on("message_end", (event) => {
		const m = event.message;
		if (m.role !== "toolResult" || m.toolName !== "fixture_tool") return;
		if (m.toolCallId === "fixture-fail")
			return { message: { ...m, isError: true, content: [{ type: "text", text: "FINAL refused by a hook" }] } };
		return { message: { ...m, content: [{ type: "text", text: "FINAL canonical result" }] } };
	});
}
`

// TestOriginCollisionWithPiFixture is the counterexample to binding origins by
// text: while the lead is busy, yesman queues a worker's report, and the
// user's steering is rewritten by a third-party input hook into exactly the
// report's text. The user's message must not become yesman's; with two
// writers behind one text, neither gets an origin. A context hook that sends
// the model the user messages it sees must never see an origin, and history
// after a restart says the same as live. It also records, for the browser
// check, pi's events for a tool whose result a message_end hook replaces:
//
//	YESMAN_TEST_PI_FIXTURE=1 [YESMAN_TEST_TOOL_EVENTS=out.json] go test ./internal/server -run OriginCollisionWithPiFixture -v
func TestOriginCollisionWithPiFixture(t *testing.T) {
	hold, held := make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	reply := func(body string) []any {
		text := func(s string) []any {
			return []any{map[string]any{"type": "text_start", "contentIndex": 0}, map[string]any{"type": "text_end", "contentIndex": 0, "content": s}}
		}
		if strings.Contains(body, "TOOL-PLEASE") && !strings.Contains(body, "FINAL canonical result") {
			var events []any
			for i, id := range []string{"fixture-call", "fixture-fail"} {
				call := map[string]any{"type": "toolCall", "id": id, "name": "fixture_tool", "arguments": map[string]any{}}
				events = append(events,
					map[string]any{"type": "toolcall_start", "contentIndex": i, "id": id, "toolName": "fixture_tool"},
					map[string]any{"type": "toolcall_end", "contentIndex": i, "toolCall": call})
			}
			return events
		}
		if strings.Contains(body, "HOLD-ME") {
			first := false
			once.Do(func() { first = true })
			if first {
				held <- struct{}{}
				<-hold
			}
		}
		return text("fixture reply")
	}
	hooks := filepath.Join(t.TempDir(), "fixture-hooks.ts")
	if err := os.WriteFile(hooks, []byte(fixtureHooks), 0o600); err != nil {
		t.Fatal(err)
	}
	f := startPiFixture(t, reply, hooks)
	release := sync.OnceFunc(func() { close(hold) })
	t.Cleanup(release) // before the backend waits for its held request
	m := f.m
	if _, err := f.store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	lead := agent.ID("alice", yesmen.LeadName)
	ctx := context.Background()
	sub := func(n int) string { return fmt.Sprintf("%012x%036x", time.Now().UnixMilli(), n) }
	queued := func(field string) []string {
		var last []string
		for _, raw := range f.recorded(lead) {
			var e map[string]json.RawMessage
			if json.Unmarshal(raw, &e) == nil && string(e["type"]) == `"queue_update"` {
				last = nil
				_ = json.Unmarshal(e[field], &last)
			}
		}
		return last
	}

	first := sub(1)
	if _, err := m.Submit(ctx, lead, first, "HOLD-ME and please start w1", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-held:
	case <-time.After(90 * time.Second):
		t.Fatal("the lead's first request never came")
	}
	// The worker reports while the lead is still answering: the report waits
	// in the lead's follow-up queue.
	if _, err := m.StartWorker(ctx, "alice", "w1", "Fixture work", "Reply with one word."); err != nil {
		t.Fatal(err)
	}
	f.wait("the report queued", func() bool { return len(queued("followUp")) == 1 })
	report := queued("followUp")[0]
	if !strings.HasPrefix(report, `[yesman] Worker "w1" finished a run.`) {
		t.Fatalf("queued %q", report)
	}
	// The user steers with a quote of it; the hook makes it the same text.
	steer := sub(2)
	if _, err := m.Submit(ctx, lead, steer, "quote:"+report, nil); err != nil {
		t.Fatal(err)
	}
	f.wait("the steering queued", func() bool { s := queued("steering"); return len(s) == 1 && s[0] == report })
	// Then steers with an image a hook replaces by another, one for one: it
	// is still the user's, bound to its receipt.
	pictured := sub(6)
	if _, err := m.Submit(ctx, lead, pictured, "img:see this", []agent.Image{{Type: "image", MimeType: "image/png", Data: userImage}}); err != nil {
		t.Fatal(err)
	}
	f.wait("the image queued", func() bool { return len(queued("steering")) == 2 })
	release()
	f.wait("all delivered", func() bool { return len(f.users(lead)) == 4 && f.settled(lead) >= 1 })

	check := func(when string) {
		t.Helper()
		got := f.users(lead)
		if len(got) < 4 {
			t.Fatalf("%s: %d user messages", when, len(got))
		}
		if got[0].Yesman == nil || *got[0].Yesman != (agent.Origin{Source: agent.OriginUser, Submission: first}) {
			t.Fatalf("%s: the first message records %+v", when, got[0].Yesman)
		}
		// pi delivers steering first: the person's rewritten message, their
		// image, then the report.
		for _, i := range []int{1, 3} {
			msg := got[i]
			if !strings.Contains(string(msg.Content), `finished a run.`) {
				t.Errorf("%s: message %d is %s", when, i, msg.Content)
			}
			// Two writers behind one text: the person's must never be
			// yesman's, and neither is known.
			if msg.Yesman != nil {
				t.Errorf("%s: message %d of two writers with one text records %+v", when, i, *msg.Yesman)
			}
		}
		img := got[2]
		if !strings.Contains(string(img.Content), `"see this"`) || !strings.Contains(string(img.Content), `"image"`) || strings.Contains(string(img.Content), userImage) {
			t.Errorf("%s: the image message is %.300s", when, img.Content)
		}
		if img.Yesman == nil || *img.Yesman != (agent.Origin{Source: agent.OriginUser, Submission: pictured}) {
			t.Errorf("%s: the message whose image a hook replaced records %+v", when, img.Yesman)
		}
	}
	check("live")

	// Swallowed input leaves nothing behind; the next message is the user's.
	if _, err := m.Submit(ctx, lead, sub(3), "drop:this never reaches the model", nil); err != nil {
		t.Fatal(err)
	}
	after := sub(4)
	settled := f.settled(lead)
	if _, err := m.Submit(ctx, lead, after, "after a swallowed input", nil); err != nil {
		t.Fatal(err)
	}
	f.wait("the next run", func() bool { return f.settled(lead) > settled && len(f.users(lead)) == 5 })
	if got := f.users(lead)[4]; got.Yesman == nil || *got.Yesman != (agent.Origin{Source: agent.OriginUser, Submission: after}) {
		t.Fatalf("the message after swallowed input records %+v", got.Yesman)
	}

	// Tools whose results a message_end hook replaces, failing one.
	settled = f.settled(lead)
	from := len(f.recorded(lead))
	if _, err := m.Submit(ctx, lead, sub(5), "TOOL-PLEASE", nil); err != nil {
		t.Fatal(err)
	}
	f.wait("the tool run", func() bool { return f.settled(lead) > settled })
	results := map[string]piMessage{}
	for _, msg := range f.messages(lead) {
		if msg.Role == "toolResult" && msg.ToolName == "fixture_tool" {
			results[msg.ToolCallID] = msg
		}
	}
	if r := results["fixture-call"]; r.IsError || !strings.Contains(string(r.Content), "FINAL canonical result") {
		t.Fatalf("the replaced result is %+v", r)
	}
	if r := results["fixture-fail"]; !r.IsError || !strings.Contains(string(r.Content), "FINAL refused by a hook") {
		t.Fatalf("the failed result is %+v", r)
	}
	if out := os.Getenv("YESMAN_TEST_TOOL_EVENTS"); out != "" {
		events := f.recorded(lead)[from:]
		data, _ := json.MarshalIndent(map[string]any{"events": events, "history": f.messages(lead)}, "", " ")
		if err := os.WriteFile(out, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// History after a restart says the same, read from pi's session.
	m.Stop(lead)
	check("after a restart")
	if got := f.users(lead)[4]; got.Yesman == nil || *got.Yesman != (agent.Origin{Source: agent.OriginUser, Submission: after}) {
		t.Fatalf("after a restart the message after swallowed input records %+v", got.Yesman)
	}

	// No context hook saw an origin, and no model was sent one.
	trace, err := os.ReadFile(filepath.Join(f.tmp, "trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(trace)), "\n")
	if len(lines) < 4 {
		t.Fatalf("the context hook ran %d times", len(lines))
	}
	for _, line := range lines {
		if strings.Contains(line, `"fields":true`) {
			t.Errorf("a context hook saw an origin field: %s", line)
			break
		}
	}
	f.noOriginSentToModels()
	t.Logf("%d context hook calls, %d model requests", len(lines), len(f.requests()))
}
