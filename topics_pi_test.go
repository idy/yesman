package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/server"
	"github.com/idy/yesman/internal/yesmen"
)

// TestTopicsWithLocalPi runs the installed pi with yesman's production
// extension and a local deterministic provider (scripts/fixtures/
// topics-provider.ts), through the real Manager, server and service. No real
// model, settings, workspace or network is used.
//
//	YESMAN_TEST_TOPICS_PI=1 go test -run TestTopicsWithLocalPi -v .
//
// With YESMAN_TEST_RUNTIME set to the prepared pinned runtime (go run
// ./tools/pi-runtime DIR prints its root), it runs that runtime instead, as
// the app does by default: topic messages then go through pi's native queue,
// whose input identity ties each to its submission, live and after a restart.
func TestTopicsWithLocalPi(t *testing.T) {
	if os.Getenv("YESMAN_TEST_TOPICS_PI") == "" {
		t.Skip("set YESMAN_TEST_TOPICS_PI=1 to run pi with a local provider")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the pi wrapper is a shell script")
	}
	cwd, _ := os.Getwd()
	tmp := t.TempDir()
	audit, gate := filepath.Join(tmp, "audit.jsonl"), filepath.Join(tmp, "gate")
	wrapper, command, cfgDir, managed := localTopicsPi(t, cwd, tmp)

	store := yesmen.New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	y, err := store.Hire("alice")
	must(t, err)
	srv := &server.Server{Pool: pool.New(filepath.Join(tmp, "repos")), Yesmen: store}
	must(t, srv.Start())

	var mu sync.Mutex
	var events []agent.Event
	newManager := func() *agent.Manager {
		return agent.New(agent.Config{
			Pi: wrapper, Command: command, Extension: filepath.Join(cwd, "pi", "yesman.ts"), Yesmen: store,
			Env:     []string{"YESMAN_API=" + srv.URL(), "PI_CODING_AGENT_DIR=" + cfgDir, "TOPICS_AUDIT=" + audit, "TOPICS_GATE=" + gate},
			OnEvent: func(e agent.Event) { mu.Lock(); events = append(events, e); mu.Unlock() },
		})
	}
	agents := newManager()
	srv.Agents = agents
	a := &app{yesmen: store, agents: agents, onTopics: func(string) {}}
	svc := &service{app: a}
	lead := agent.ID("alice", yesmen.LeadName)
	ctx := context.Background()
	defer func() { a.agents.StopAll() }()

	settled := func(after int) int {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			for i := after; i < len(events); i++ {
				var e struct{ Type string }
				_ = json.Unmarshal(events[i].Data, &e)
				if events[i].Agent == lead && e.Type == "agent_settled" {
					mu.Unlock()
					return i + 1
				}
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the lead did not settle")
		return 0
	}
	userMessagesSeen := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, ev := range events {
			var e struct {
				Type    string
				Message struct{ Role string }
			}
			_ = json.Unmarshal(ev.Data, &e)
			if ev.Agent == lead && e.Type == "message_end" && e.Message.Role == "user" {
				n++
			}
		}
		return n
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	createdA, err := svc.CreateTopic("alice", y.HiredAt, yesmen.TopicInput{Title: "Alpha storage", Summary: "SUMMARY-BODY-SECRET"})
	must(t, err)
	createdB, err := svc.CreateTopic("alice", y.HiredAt, yesmen.TopicInput{Title: "Beta images"})
	must(t, err)
	tpA, tpB := createdA.Topic, createdB.Topic
	png := agent.Image{Type: "image", MimeType: "image/png", Data: "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg=="}
	id := func(tag string) string {
		return fmt.Sprintf("%012x", time.Now().UnixMilli()) + strings.Repeat(tag, 36/len(tag))
	}
	idA, idB, idMain := id("a1"), id("b2"), id("c3")

	// Phase 1, while the lead's first run is held: topic A starts it, then
	// topic B (with an image), the main composer and a worker report steer.
	rA, err := svc.SubmitTopic(ctx, "alice", y.HiredAt, tpA.ID, idA, "How should A be stored?", nil)
	must(t, err)
	if rA.Delivery.ID != idA || rA.Text != "[Topic tp1: Alpha storage]\n\nHow should A be stored?" {
		t.Fatalf("topic A submission %+v", rA)
	}
	waitFor("topic A's native user message", func() bool { return userMessagesSeen() >= 1 })
	rB, err := svc.SubmitTopic(ctx, "alice", y.HiredAt, tpB.ID, idB, "Can B take images?", []agent.Image{png})
	must(t, err)
	if rB.Delivery.State != "steering" {
		t.Fatalf("topic B during the run: %+v", rB.Delivery)
	}
	if d, err := svc.Submit(ctx, lead, idMain, "Main line: unrelated note", nil, y.HiredAt); err != nil || d.State != "steering" {
		t.Fatalf("main composer: %+v %v", d, err)
	}
	must(t, agents.Prompt(ctx, lead, "[yesman] Worker \"helper\" finished a run. Its final message:\n\nWorker report about Alpha storage."))
	// An uncertain retry with the same ID dispatches nothing new; the same ID
	// cannot be claimed by another topic.
	again, err := svc.SubmitTopic(ctx, "alice", y.HiredAt, tpA.ID, idA, "How should A be stored?", nil)
	if err != nil || again.Delivery.ID != idA || again.Text != rA.Text {
		t.Fatalf("retry %+v %v", again, err)
	}
	if _, err := svc.SubmitTopic(ctx, "alice", y.HiredAt, tpB.ID, idA, "How should A be stored?", nil); err == nil {
		t.Fatal("another topic claimed topic A's submission")
	}
	must(t, os.WriteFile(gate, []byte("open"), 0o644))
	mark := settled(0)

	// Phase 2: a stale lead tool change loses to the user's edit; a current one is saved.
	cur, _ := store.Topic("alice", y.HiredAt, tpA.ID)
	up, err := svc.UpdateTopic("alice", y.HiredAt, tpA.ID, yesmen.TopicPatch{Revision: cur.Revision, Summary: ptrTo("User edited summary")})
	if err != nil || up.Conflict {
		t.Fatalf("user edit %+v %v", up, err)
	}
	stale := fmt.Sprintf(`FIXTURE-TOOL {"action":"update","id":"tp1","revision":%d,"title":"Lead overwrite"}`, cur.Revision)
	_, err = svc.SubmitTopic(ctx, "alice", y.HiredAt, tpA.ID, id("d4"), stale, nil)
	must(t, err)
	mark = settled(mark)
	if got, _ := store.Topic("alice", y.HiredAt, tpA.ID); got.Title != "Alpha storage" || got.Summary != "User edited summary" || got.UpdatedBy != yesmen.TopicByUser {
		t.Fatalf("a stale lead change overwrote the user's edit: %+v", got)
	}
	current := fmt.Sprintf(`FIXTURE-TOOL {"action":"update","id":"tp1","revision":%d,"addQuestions":["Is A kept per workspace?"]}`, up.Topic.Revision)
	_, err = svc.SubmitTopic(ctx, "alice", y.HiredAt, tpA.ID, id("e5"), current, nil)
	must(t, err)
	mark = settled(mark)
	gotA, _ := store.Topic("alice", y.HiredAt, tpA.ID)
	if gotA.UpdatedBy != yesmen.TopicByLead || len(gotA.Questions) != 1 || gotA.Summary != "User edited summary" {
		t.Fatalf("lead tool change: %+v", gotA)
	}

	// Phase 3: the lead lists and reads topics with its real tool. The user
	// cites topic A's own submission as a source, and topic C's content is
	// never read: neither may reach the model through a tool result's content
	// or details.
	cite, err := svc.UpdateTopic("alice", y.HiredAt, tpA.ID, yesmen.TopicPatch{Revision: gotA.Revision, AddSources: []yesmen.TopicSource{{Kind: yesmen.SourceSubmission, Ref: idA}}})
	if err != nil || cite.Conflict {
		t.Fatalf("cite a submission %+v %v", cite, err)
	}
	_, err = svc.CreateTopic("alice", y.HiredAt, yesmen.TopicInput{Title: "Gamma unread", Summary: "LIST-ONLY-SUMMARY", Questions: []string{"LIST-ONLY-QUESTION"}, Conclusions: []string{"LIST-ONLY-CONCLUSION"}})
	must(t, err)
	idList, idRead := id("f6"), id("a7")
	_, err = svc.Submit(ctx, lead, idList, `FIXTURE-TOOL {"action":"list"}`, nil, y.HiredAt)
	must(t, err)
	mark = settled(mark)
	_, err = svc.Submit(ctx, lead, idRead, `FIXTURE-TOOL {"action":"read","id":"tp1"}`, nil, y.HiredAt)
	must(t, err)
	settled(mark)
	gotA, _ = store.Topic("alice", y.HiredAt, tpA.ID)

	var sentFrom map[string]map[string]bool
	checkHistory := func(m *agent.Manager, label string) {
		t.Helper()
		conv, err := m.Messages(ctx, lead)
		must(t, err)
		var msgs []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Yesman  *struct {
				Source     string `json:"source"`
				Submission string `json:"submission"`
			} `json:"yesman"`
		}
		must(t, json.Unmarshal(conv.Messages, &msgs))
		byTopic := map[string][]string{}
		images := map[string]int{}
		for _, msg := range msgs {
			if msg.Role != "user" {
				continue
			}
			text, n := contentText(msg.Content)
			key := "none"
			if m := regexp.MustCompile(`^\[Topic (tp\d+): [^\n]*\](\n\n|$)`).FindStringSubmatch(text); m != nil {
				key = m[1]
			}
			byTopic[key] = append(byTopic[key], text)
			images[key] += n
			// The app's recorded origin ties each topic message to a
			// submission of that topic, never of another or of the main line.
			// The native queue's input identity keeps it across a restart.
			if key != "none" && (label == "live" || managed) {
				if msg.Yesman == nil || msg.Yesman.Source != "user" || !sentFrom[key][msg.Yesman.Submission] {
					t.Fatalf("%s: %s message %q has origin %+v", label, key, text, msg.Yesman)
				}
			}
		}
		if len(byTopic["tp1"]) != 3 || len(byTopic["tp2"]) != 1 || images["tp2"] != 1 || images["tp1"] != 0 {
			t.Fatalf("%s: topic messages %q images %v", label, byTopic, images)
		}
		if byTopic["tp2"][0] != "[Topic tp2: Beta images]\n\nCan B take images?" {
			t.Fatalf("%s: topic B text %q", label, byTopic["tp2"][0])
		}
		for _, text := range byTopic["none"] {
			if strings.Contains(text, "[Topic ") {
				t.Fatalf("%s: unmarked message carries a topic: %q", label, text)
			}
		}
		if len(byTopic["none"]) != 4 {
			t.Fatalf("%s: main line and worker report: %q", label, byTopic["none"])
		}
		if managed {
			// The pinned runtime's native queue took every topic submission:
			// pi recorded its input under the submission's own ID.
			if !conv.Queue.RecallSupported {
				t.Fatalf("%s: the runtime has no native queue: %+v", label, conv.Queue)
			}
			states := map[string]string{}
			for _, d := range conv.Deliveries {
				states[d.ID] = d.State
			}
			for _, id := range []string{idA, idB, idMain} {
				if states[id] != "dispatched" {
					t.Fatalf("%s: submission %s is %q, not taken by pi's native queue: %v", label, id, states[id], states)
				}
			}
		}
	}
	sentFrom = map[string]map[string]bool{"tp1": {}, "tp2": {}}
	for _, id := range []string{tpA.ID, tpB.ID} {
		tp, _ := store.Topic("alice", y.HiredAt, id)
		for _, s := range tp.Sends {
			sentFrom[id][s.ID] = true
		}
	}
	checkHistory(agents, "live")

	// Topic records hold only their own submissions, never the main line or report.
	gotB, _ := store.Topic("alice", y.HiredAt, tpB.ID)
	if len(gotA.Sends) != 3 || gotA.Sends[0].ID != idA || len(gotB.Sends) != 1 || gotB.Sends[0].ID != idB || gotB.Sends[0].ImageCount != 1 {
		t.Fatalf("sends A %+v B %+v", gotA.Sends, gotB.Sends)
	}

	// What reached the model: visible lines only, no internal fields, and the
	// topic index without bodies in the lead's system prompt.
	data, err := os.ReadFile(audit)
	must(t, err)
	for _, leak := range []string{idA, idB, idMain, "yesmanDeliveryId", "agentSince", "fingerprint", "nextItem"} {
		if i := strings.Index(string(data), leak); i >= 0 {
			t.Fatalf("model request contains %q: …%s…", leak, string(data)[max(0, i-300):min(len(data), i+100)])
		}
	}
	// The first request's system prompt was built before any tool call, so
	// the original summary can only have reached the model through the context.
	if strings.Contains(string(data), "SUMMARY-BODY-SECRET") {
		t.Fatal("a topic's body was injected into the lead's context")
	}
	for _, leak := range []string{idList, idRead, "LIST-ONLY-SUMMARY", "LIST-ONLY-QUESTION", "LIST-ONLY-CONCLUSION", `"sends"`} {
		if i := strings.Index(string(data), leak); i >= 0 {
			t.Fatalf("model request contains %q: …%s…", leak, string(data)[max(0, i-300):min(len(data), i+100)])
		}
	}
	// The tool did run: the list named topic C and the read showed topic A's
	// content and that a message was sent from it.
	for _, want := range []string{"tp3 [open] Gamma unread", "Read one with action read", "User edited summary", "a message the user sent from this topic", "Messages the user sent from this topic: 3"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("model requests lack %q", want)
		}
	}
	if !strings.Contains(string(data), "## Topics") || !strings.Contains(string(data), "- tp1 Alpha storage") || !strings.Contains(string(data), "- tp2 Beta images") {
		t.Fatal("the lead's context lacks the topic index")
	}

	// After a whole restart the native history still carries the same lines.
	agents.StopAll()
	agents = newManager()
	srv.Agents, a.agents = agents, agents
	checkHistory(agents, "reloaded")
}

// localTopicsPi is the pi the topics checks run: the installed pi (or
// YESMAN_TEST_PI) through a wrapper that adds the local deterministic
// provider, or, with YESMAN_TEST_RUNTIME, the prepared pinned runtime as the
// app runs it by default, with the provider in its settings. It returns the
// executable, the Manager's command (nil for the wrapper), pi's own
// configuration directory and whether the runtime is the pinned one. It
// skips the test without a pi to run.
func localTopicsPi(t *testing.T, cwd, tmp string) (string, func(context.Context) (string, []string, error), string, bool) {
	t.Helper()
	cfgDir := filepath.Join(tmp, "pi-config")
	must(t, os.MkdirAll(cfgDir, 0o755))
	provider := filepath.Join(cwd, "scripts", "fixtures", "topics-provider.ts")
	if root := os.Getenv("YESMAN_TEST_RUNTIME"); root != "" {
		node, err := exec.LookPath("node")
		must(t, err)
		cli := filepath.Join(root, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "cli.js")
		if _, err := os.Stat(cli); err != nil {
			t.Fatalf("YESMAN_TEST_RUNTIME is not a prepared runtime: %v", err)
		}
		settings, _ := json.Marshal(map[string]any{"packages": []string{}, "extensions": []string{provider}, "skills": []string{}, "promptTemplates": []string{}, "themes": []string{},
			"compaction": map[string]bool{"enabled": false}, "retry": map[string]bool{"enabled": false}, "defaultProjectTrust": "no", "defaultProvider": "topics-test", "defaultModel": "m"})
		must(t, os.WriteFile(filepath.Join(cfgDir, "settings.json"), settings, 0o644))
		return node, func(context.Context) (string, []string, error) { return node, []string{cli}, nil }, cfgDir, true
	}
	piPath := os.Getenv("YESMAN_TEST_PI")
	if piPath == "" {
		var err error
		if piPath, err = exec.LookPath("pi"); err != nil {
			t.Skip("pi is not installed")
		}
	}
	must(t, os.WriteFile(filepath.Join(cfgDir, "settings.json"), []byte(`{"packages":[],"extensions":[],"skills":[],"promptTemplates":[],"themes":[],"compaction":{"enabled":false},"retry":{"enabled":false},"defaultProjectTrust":"no"}`), 0o644))
	wrapper := filepath.Join(tmp, "pi.sh")
	must(t, os.WriteFile(wrapper, []byte(fmt.Sprintf("#!/bin/sh\nexec %q \"$@\" -e %q --provider topics-test --model m\n", piPath, provider)), 0o755))
	return wrapper, nil, cfgDir, false
}

func contentText(raw json.RawMessage) (string, int) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, 0
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &blocks)
	var parts []string
	images := 0
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			images++
		}
	}
	return strings.Join(parts, "\n"), images
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func ptrTo(s string) *string { return &s }
