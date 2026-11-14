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
	if os.Getenv("YESMAN_TEST_PI_FIXTURE") == "" {
		t.Skip("set YESMAN_TEST_PI_FIXTURE=1 to run the installed pi with a local backend")
	}
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	ext, err := filepath.Abs("../../pi/yesman.ts")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()

	var mu sync.Mutex
	var bodies []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body bytes.Buffer
		_, _ = body.ReadFrom(r.Body)
		mu.Lock()
		bodies = append(bodies, body.String())
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v any) { b, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", b) }
		usage := map[string]any{"input": 1, "output": 1, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 2,
			"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}}
		send(map[string]any{"type": "start"})
		send(map[string]any{"type": "text_start", "contentIndex": 0})
		send(map[string]any{"type": "text_end", "contentIndex": 0, "content": "fixture reply"})
		send(map[string]any{"type": "done", "reason": "stop", "usage": usage})
	}))
	defer backend.Close()

	config := filepath.Join(tmp, "pi")
	if err := os.MkdirAll(config, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, v any) {
		data, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(config, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("settings.json", map[string]any{
		"packages": []any{}, "extensions": []any{}, "skills": []any{}, "promptTemplates": []any{}, "themes": []any{},
		"compaction": map[string]any{"enabled": false}, "retry": map[string]any{"enabled": false},
		"defaultProjectTrust": "no", "defaultProvider": "fixture", "defaultModel": "m",
	})
	write("models.json", map[string]any{"providers": map[string]any{"fixture": map[string]any{
		"baseUrl": backend.URL, "api": "pi-messages", "apiKey": "fixture",
		"models": []any{map[string]any{"id": "m", "name": "Fixture", "input": []string{"text", "image"},
			"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}, "contextWindow": 100000, "maxTokens": 1000}},
	}}})

	store := yesmen.New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	srv := &Server{Pool: pool.New(filepath.Join(tmp, "repos")), Yesmen: store}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	settled := map[string]int{}
	m := agent.New(agent.Config{
		Pi:        piPath,
		Extension: ext,
		Env:       []string{"YESMAN_API=" + srv.URL(), "YESMAN_HOME=" + tmp, "PI_CODING_AGENT_DIR=" + config},
		Yesmen:    store,
		OnEvent: func(e agent.Event) {
			var head struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(e.Data, &head)
			if head.Type == "agent_settled" {
				mu.Lock()
				settled[e.Agent]++
				mu.Unlock()
			}
		},
	})
	srv.Agents = m
	defer m.StopAll()
	if _, err := store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	lead, worker := agent.ID("alice", yesmen.LeadName), agent.ID("alice", "w1")
	ctx := context.Background()

	type message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Yesman  *agent.Origin   `json:"yesman"`
	}
	users := func(id string) []message {
		t.Helper()
		conv, err := m.Messages(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var all []message
		if err := json.Unmarshal(conv.Messages, &all); err != nil {
			t.Fatal(err)
		}
		var out []message
		for _, msg := range all {
			if msg.Role == "user" {
				out = append(out, msg)
			}
		}
		return out
	}
	wait := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// The user's submission, bound to its receipt.
	submission := fmt.Sprintf("%012x%036x", time.Now().UnixMilli(), 1)
	if _, err := m.Submit(ctx, lead, submission, "Please start w1", nil); err != nil {
		t.Fatal(err)
	}
	wait("the lead's first run", func() bool { mu.Lock(); defer mu.Unlock(); return settled[lead] >= 1 })

	// The lead's task for a worker, then its report back to the lead.
	if _, err := m.StartWorker(ctx, "alice", "w1", "Fixture work", "Reply with one word."); err != nil {
		t.Fatal(err)
	}
	wait("the worker's report", func() bool { return len(users(lead)) >= 2 })
	wait("the lead's second run", func() bool { mu.Lock(); defer mu.Unlock(); return settled[lead] >= 2 })

	got := users(lead)
	if len(got) != 2 || got[0].Yesman == nil || *got[0].Yesman != (agent.Origin{Source: agent.OriginUser, Submission: submission}) {
		t.Fatalf("the user's message records %+v", got)
	}
	if got[1].Yesman == nil || *got[1].Yesman != (agent.Origin{Source: agent.OriginYesman, Kind: "report", Worker: "w1"}) ||
		!strings.Contains(string(got[1].Content), `[yesman] Worker \"w1\" finished a run`) {
		t.Fatalf("the report records %+v: %s", got[1].Yesman, got[1].Content)
	}
	// The lead messages its worker through the API, as its tool does.
	info, err := m.Get(lead)
	if err != nil {
		t.Fatal(err)
	}
	y, err := store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/workers/send", strings.NewReader(`{"name":"w1","message":"once more"}`))
	if _, err := srv.sendWorker(ctx, caller{agent: info, yesman: y}, req); err != nil {
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
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 3 {
		t.Fatalf("%d model requests", len(bodies))
	}
	for _, b := range bodies {
		if strings.Contains(b, `"yesman":{`) {
			t.Fatalf("a model request carried an origin: %s", b)
		}
	}
}
