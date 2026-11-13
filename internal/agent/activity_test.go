package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func TestDescribeTool(t *testing.T) {
	dir := t.TempDir()
	inside, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, "web", "app.js")})
	outside, _ := json.Marshal(map[string]string{"path": filepath.Join(filepath.Dir(dir), "notes.md")})
	for _, c := range []struct {
		name, args, want string
	}{
		{"read", string(inside), "Reading " + filepath.Join("web", "app.js")},
		{"read", string(outside), "Reading " + filepath.Join(filepath.Dir(dir), "notes.md")},
		{"read", `{"path":"src/main.go"}`, "Reading src/main.go"},
		{"bash", `{"command":"go test ./...\necho done"}`, "Running go test ./... …"},
		{"edit", ``, "Editing"},
		{"yesman_worker_start", `{"name":"e2e","task":"run the tests"}`, "Starting worker e2e"},
		{"yesman_board", `{}`, "Reading the board"},
		{"yesman_board", `{"add":[{"title":"x"}]}`, "Updating the board"},
		{"mcp__lark__send", `{"text":"hi"}`, "Using mcp__lark__send"},
		{"bash", `{"command":"` + strings.Repeat("x", 300) + `"}`, "Running " + strings.Repeat("x", activityLimit-len("Running ")-1) + "…"},
	} {
		var raw json.RawMessage
		if c.args != "" {
			raw = json.RawMessage(c.args)
		}
		if got := describeTool(c.name, raw, dir); got != c.want {
			t.Errorf("describeTool(%s, %s) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

func TestActivityFollowsTheRun(t *testing.T) {
	m, _ := newTestManager(t, 0)
	a := &agent{info: Info{ID: "alice/lead"}}
	step := func(kind, data string) Info {
		t.Helper()
		raw := json.RawMessage(data)
		m.follow(a, nil, kind, raw)
		m.mu.Lock()
		defer m.mu.Unlock()
		return a.info
	}
	if i := step("agent_start", `{"type":"agent_start"}`); i.RunStartedAt.IsZero() || i.Activity != waitingForModel {
		t.Fatal("a run started without a start time")
	}
	started := a.info.RunStartedAt
	step("message_start", `{"type":"message_start","message":{"role":"assistant"}}`)
	if i := step("message_update", `{"type":"message_update","assistantMessageEvent":{"type":"thinking_start"}}`); i.Activity != "Thinking" {
		t.Errorf("activity = %q", i.Activity)
	}
	step("tool_execution_start", `{"toolCallId":"a","toolName":"read","args":{"path":"README.md"}}`)
	if i := step("tool_execution_start", `{"toolCallId":"b","toolName":"bash","args":{"command":"make"}}`); i.Activity != "Running make (+1)" {
		t.Errorf("two tools: %q", i.Activity)
	}
	if i := step("tool_execution_end", `{"toolCallId":"b"}`); i.Activity != "Reading README.md" {
		t.Errorf("after one ended: %q", i.Activity)
	}
	if i := step("tool_execution_end", `{"toolCallId":"a"}`); i.Activity != waitingForModel {
		t.Errorf("after both ended: %q", i.Activity)
	}
	// A retry's agent_start belongs to the same run.
	if i := step("agent_start", `{"type":"agent_start"}`); !i.RunStartedAt.Equal(started) {
		t.Error("a retry restarted the clock")
	}
	if i := step("agent_settled", `{"type":"agent_settled"}`); i.Activity != "" || !i.RunStartedAt.IsZero() {
		t.Errorf("after the run: %+v", i)
	}
}

// promptsTo records the messages each agent's fake pi received.
func promptsTo(m *Manager) func(id string) []string {
	var mu sync.Mutex
	got := map[string][]string{}
	m.cfg.OnEvent = func(e Event) {
		var head struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Data, &head) == nil && head.Type == "test_prompt_received" {
			mu.Lock()
			got[e.Agent] = append(got[e.Agent], head.Message)
			mu.Unlock()
		}
	}
	return func(id string) []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got[id]...)
	}
}

func waitPrompt(t *testing.T, prompts func(string) []string, id, prefix string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range prompts(id) {
			if strings.HasPrefix(p, prefix) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never received %q; it received %q", id, prefix, prompts(id))
}

func TestLeadHearsOfAStoppedRun(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_ABORT_WORKERS=1")
	prompts := promptsTo(m)
	if _, err := m.StartWorker(context.Background(), "alice", "w1", "a task"); err != nil {
		t.Fatal(err)
	}
	waitPrompt(t, prompts, ID("alice", yesmen.LeadName), `[yesman] Worker "w1" was stopped by the user`)
	for _, p := range prompts(ID("alice", yesmen.LeadName)) {
		if strings.Contains(p, "finished a run") {
			t.Errorf("a stopped run was reported as finished: %q", p)
		}
	}
}

func TestLeadHearsOfAFailedWorker(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_CRASH_WORKERS=1")
	prompts := promptsTo(m)
	if _, err := m.StartWorker(context.Background(), "alice", "w1", "a task"); err != nil {
		t.Fatal(err)
	}
	waitPrompt(t, prompts, ID("alice", yesmen.LeadName), `[yesman] Worker "w1" stopped unexpectedly`)
	waitStatus(t, m, ID("alice", "w1"), Failed)
}

func TestPuttingAWorkerToSleepTellsNobody(t *testing.T) {
	m, _ := newTestManager(t, 0)
	prompts := promptsTo(m)
	if _, err := m.StartWorker(context.Background(), "alice", "w1", "a task"); err != nil {
		t.Fatal(err)
	}
	lead := ID("alice", yesmen.LeadName)
	waitPrompt(t, prompts, lead, `[yesman] Worker "w1" finished a run`)
	m.Stop(ID("alice", "w1"))
	time.Sleep(200 * time.Millisecond)
	if n := len(prompts(lead)); n != 1 {
		t.Errorf("the lead received %d messages: %q", n, prompts(lead))
	}
}
