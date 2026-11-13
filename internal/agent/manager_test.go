package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// The test binary doubles as a fake pi in RPC mode when YESMAN_FAKE_PI is
// set: it answers every command, delays get_state by FAKE_PI_DELAY, runs a
// trivial turn for each prompt, and exits when its input closes. With
// FAKE_PI_TRAIL it starts a reply right after answering get_messages; with
// FAKE_PI_DEAF it never reads its input.
func TestMain(m *testing.M) {
	if os.Getenv("YESMAN_FAKE_PI") != "" {
		fakePi()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakePi() {
	if os.Getenv("FAKE_PI_DEAF") != "" {
		time.Sleep(time.Hour)
		return
	}
	delay, _ := time.ParseDuration(os.Getenv("FAKE_PI_DELAY"))
	models := []Model{{ID: "shared", Provider: "alpha", Name: "Reasoner", ContextWindow: 200000}, {ID: "shared", Provider: "beta", Name: "Plain"}}
	model, effort := models[0], "high"
	levels := func() []string {
		if model.Provider == "beta" {
			return []string{"off"}
		}
		return []string{"low", "high", "max"}
	}
	out := bufio.NewWriter(os.Stdout)
	write := func(v any) {
		data, _ := json.Marshal(v)
		out.Write(append(data, '\n'))
		out.Flush()
	}
	stateChecks := 0
	var heldPrompts, heldReports []string
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var cmd struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Message  string `json:"message"`
			Provider string `json:"provider"`
			ModelID  string `json:"modelId"`
			Level    string `json:"level"`
			// extension_ui_response
			Value     *string `json:"value"`
			Confirmed *bool   `json:"confirmed"`
			Cancelled bool    `json:"cancelled"`
		}
		if json.Unmarshal(sc.Bytes(), &cmd) != nil {
			continue
		}
		if cmd.Type == "extension_ui_response" {
			// pi does not answer a response: the run it held goes on.
			write(map[string]any{"type": "test_ui_answer", "id": cmd.ID, "value": cmd.Value, "confirmed": cmd.Confirmed, "cancelled": cmd.Cancelled})
			write(map[string]any{"type": "agent_settled", "aborted": false})
			continue
		}
		if cmd.Type == "get_state" {
			stateChecks++
			if stateChecks == 2 && os.Getenv("FAKE_PI_MODEL_CHECK_DELAY") != "" {
				write(map[string]any{"type": "test_model_check"})
				time.Sleep(300 * time.Millisecond)
			}
			time.Sleep(delay)
		}
		failPrompt := cmd.Type == "prompt" && (os.Getenv("FAKE_PI_FAIL_PROMPTS") != "" ||
			os.Getenv("FAKE_PI_FAIL_REPORTS") != "" && strings.HasPrefix(cmd.Message, "[yesman] Worker"))
		if failPrompt {
			write(map[string]any{"type": "response", "id": cmd.ID, "command": cmd.Type, "success": false, "error": "rejected"})
			continue
		}
		var data any = map[string]any{"messages": []any{}, "text": "done"}
		switch cmd.Type {
		case "prompt":
			write(map[string]any{"type": "test_prompt_received", "provider": model.Provider, "message": cmd.Message})
			if os.Getenv("FAKE_PI_HOLD_PROMPTS") != "" ||
				os.Getenv("FAKE_PI_HOLD_REPORTS") != "" && strings.HasPrefix(cmd.Message, "[yesman] Worker") {
				// Simulate async extension work before isStreaming becomes true.
				heldPrompts = append(heldPrompts, cmd.ID)
				continue
			}
		case "test_release_prompts":
			for _, id := range heldPrompts {
				if os.Getenv("FAKE_PI_HOLD_REPORTS") != "" {
					// Settle before acknowledging report delivery, so the worker
					// handoff must retry follow-through after its reservation ends.
					write(map[string]any{"type": "agent_start"})
					write(map[string]any{"type": "agent_settled", "aborted": false})
				}
				write(map[string]any{"type": "response", "id": id, "command": "prompt", "success": true})
			}
			heldPrompts = nil
		case "get_last_assistant_text":
			if os.Getenv("FAKE_PI_HOLD_REPORTS") != "" {
				heldReports = append(heldReports, cmd.ID)
				write(map[string]any{"type": "test_report_waiting"})
				continue
			}
		case "test_release_reports":
			for _, id := range heldReports {
				write(map[string]any{"type": "response", "id": id, "command": "get_last_assistant_text", "success": true, "data": map[string]any{"text": "done"}})
			}
			heldReports = nil
		case "get_state":
			data = modelState{Model: model, ThinkingLevel: effort, IsStreaming: os.Getenv("FAKE_PI_BUSY") != ""}
		case "get_session_stats":
			stats := map[string]any{
				"sessionFile":       "/private/pi/sessions/context-test.jsonl",
				"sessionId":         "private-context-session-id",
				"userMessages":      4,
				"assistantMessages": 6,
				"toolCalls":         3,
				"toolResults":       3,
				"totalMessages":     13,
				"tokens": map[string]any{
					"input": 600000, "output": 40000, "cacheRead": 200000, "cacheWrite": 10000, "total": 850000,
				},
				"cost":         1.25,
				"contextUsage": map[string]any{"tokens": 32000, "contextWindow": 200000, "percent": 16},
			}
			switch os.Getenv("FAKE_PI_SESSION_STATS") {
			case "unknown":
				stats["contextUsage"] = map[string]any{"tokens": nil, "contextWindow": 200000, "percent": nil}
			case "zero":
				stats["contextUsage"] = map[string]any{"tokens": 0, "contextWindow": 200000, "percent": 0}
			case "missing":
				delete(stats, "contextUsage")
			case "invalid":
				stats["tokens"] = "invalid token usage"
			case "error":
				write(map[string]any{"type": "response", "id": cmd.ID, "command": cmd.Type, "success": false, "error": "stats unavailable"})
				continue
			}
			data = stats
		case "get_available_models":
			data = map[string]any{"models": models}
		case "get_available_thinking_levels":
			data = map[string]any{"levels": levels()}
		case "set_model":
			found := false
			for _, choice := range models {
				if choice.Provider == cmd.Provider && choice.ID == cmd.ModelID {
					model = choice
					found = true
					break
				}
			}
			if !found {
				write(map[string]any{"type": "response", "id": cmd.ID, "command": cmd.Type, "success": false, "error": "Model not found"})
				continue
			}
			effort = levels()[0]
			write(map[string]any{"type": "thinking_level_changed", "level": effort})
			data = model
		case "set_thinking_level":
			effort = cmd.Level
			write(map[string]any{"type": "thinking_level_changed", "level": effort})
		}
		write(map[string]any{"type": "response", "id": cmd.ID, "command": cmd.Type, "success": true, "data": data})
		if cmd.Type == "get_messages" && os.Getenv("FAKE_PI_TRAIL") != "" {
			write(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant"}})
		}
		if cmd.Type == "prompt" {
			worker := os.Getenv("YESMAN_ROLE") == RoleWorker
			if worker && os.Getenv("FAKE_PI_CRASH_WORKERS") != "" {
				os.Exit(3)
			}
			write(map[string]any{"type": "agent_start"})
			if os.Getenv("FAKE_PI_SLOW_RUNS") != "" {
				// A real run takes a while: its prompt's answer is handled
				// well before it settles.
				time.Sleep(50 * time.Millisecond)
			}
			if worker && os.Getenv("FAKE_PI_ASK_WORKERS") != "" {
				// A tool's dialog holds the run until it is answered.
				write(map[string]any{"type": "extension_ui_request", "id": "q1", "method": "select", "title": "Allow rm -rf build?", "options": []string{"Allow", "Block"}, "timeout": 60000})
				continue
			}
			write(map[string]any{"type": "agent_settled", "aborted": worker && os.Getenv("FAKE_PI_ABORT_WORKERS") != ""})
		}
	}
}

func newTestManager(t *testing.T, delay time.Duration, env ...string) (*Manager, *yesmen.Store) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	store := yesmen.New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	if _, err := store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	m := New(Config{
		Pi:        exe,
		Extension: filepath.Join(tmp, "none.ts"),
		Env:       append([]string{"YESMAN_FAKE_PI=1", "FAKE_PI_DELAY=" + delay.String()}, env...),
		Yesmen:    store,
	})
	t.Cleanup(func() { stopAllSettled(m) })
	return m, store
}

// stopAllSettled stops every agent until none runs, starts or reports: a
// worker's report can start its lead just as a test ends, and Windows
// cannot remove the temporary folder while that process holds files in it.
func stopAllSettled(m *Manager) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		m.StopAll()
		m.mu.Lock()
		busy := false
		for _, a := range m.agents {
			busy = busy || a.proc != nil || a.starting != nil || a.reports > 0
		}
		m.mu.Unlock()
		if !busy || time.Now().After(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitStatus(t *testing.T, m *Manager, id string, want Status) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if i, err := m.Get(id); err == nil && i.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	i, _ := m.Get(id)
	t.Fatalf("%s is %s, want %s", id, i.Status, want)
}

func TestStopDuringStart(t *testing.T) {
	m, _ := newTestManager(t, 2*time.Second)
	id := ID("alice", yesmen.LeadName)
	errc := make(chan error, 1)
	go func() { errc <- m.Prompt(context.Background(), id, "hi") }()
	waitStatus(t, m, id, Starting)

	m.Stop(id)
	if p := m.running(id); p != nil {
		t.Fatal("a process still runs after Stop returned")
	}
	if i, _ := m.Get(id); i.Status != Stopped {
		t.Errorf("status after Stop = %s", i.Status)
	}
	if err := <-errc; !errors.Is(err, errStopped) {
		t.Errorf("the interrupted prompt returned %v", err)
	}
	m.mu.Lock()
	tokens := len(m.tokens)
	m.mu.Unlock()
	if tokens != 0 {
		t.Errorf("%d credentials outlive their processes", tokens)
	}
}

func TestRestartAfterStop(t *testing.T) {
	m, _ := newTestManager(t, 0)
	id := ID("alice", yesmen.LeadName)
	ctx := context.Background()
	for i := range 3 {
		if err := m.Prompt(ctx, id, fmt.Sprint("turn ", i)); err != nil {
			t.Fatalf("prompt %d: %v", i, err)
		}
		m.Stop(id)
	}
	if err := m.Prompt(ctx, id, "again"); err != nil {
		t.Fatal(err)
	}
}

func TestCredentials(t *testing.T) {
	// Keep the worker running until its credentials have been checked:
	// a finished worker reports and closes, revoking its credential.
	m, store := newTestManager(t, 0, "FAKE_PI_ASK_WORKERS=1")
	if err := store.AddWorker("alice", yesmen.Worker{Name: "w1", Task: "t", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lead, worker := ID("alice", yesmen.LeadName), ID("alice", "w1")
	for _, id := range []string{lead, worker} {
		if err := m.Prompt(ctx, id, "hi"); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	leadToken, workerToken := m.agents[lead].token, m.agents[worker].token
	m.mu.Unlock()
	if leadToken == "" || leadToken == workerToken {
		t.Fatalf("agents share or lack credentials: %q %q", leadToken, workerToken)
	}
	if i, err := m.Authenticate(workerToken); err != nil || i.ID != worker || i.Role != RoleWorker {
		t.Errorf("worker credential = %+v, %v", i, err)
	}
	if i, err := m.Authenticate(leadToken); err != nil || i.ID != lead {
		t.Errorf("lead credential = %+v, %v", i, err)
	}
	if _, err := m.Authenticate("forged"); err == nil {
		t.Error("an unknown credential authenticated")
	}
	m.Stop(worker)
	if _, err := m.Authenticate(workerToken); err == nil {
		t.Error("a stopped worker's credential still authenticates")
	}
	if i, _ := m.Get(worker); !strings.HasPrefix(i.ID, "alice/") || i.Since.IsZero() {
		t.Errorf("worker identity %+v", i)
	}
}

func TestFailedWorkerStartLeavesNoRecord(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_FAIL_PROMPTS=1")
	if _, err := m.StartWorker(context.Background(), "alice", "w1", "", "a task"); err == nil {
		t.Fatal("a worker whose task was rejected started")
	}
	if ws, _ := store.Workers("alice"); len(ws) != 0 {
		t.Errorf("workers after a failed start: %+v", ws)
	}
}

func TestUndeliveredReportIsShown(t *testing.T) {
	reportAttempts, reportBackoff = 2, time.Millisecond
	t.Cleanup(func() { reportAttempts, reportBackoff = 3, 2*time.Second })
	m, _ := newTestManager(t, 0, "FAKE_PI_FAIL_REPORTS=1")
	if _, err := m.StartWorker(context.Background(), "alice", "w1", "", "a task"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if i, _ := m.Get(ID("alice", "w1")); strings.Contains(i.Detail, "did not reach the lead") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	i, _ := m.Get(ID("alice", "w1"))
	t.Fatalf("worker detail = %q", i.Detail)
}

// Stopping must not wait for OnChange, which in the app waits for the UI
// thread, and the UI thread stops every agent when the app quits.
func TestStopDoesNotWaitForOnChange(t *testing.T) {
	m, _ := newTestManager(t, 0)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	m.cfg.OnChange = func([]Info) { <-block }
	id := ID("alice", yesmen.LeadName)
	if err := m.Prompt(context.Background(), id, "hi"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		m.StopAll()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StopAll waited for a blocked OnChange")
	}
}

// Messages returns the reply in progress, which pi's history lacks, with
// the number of the last event it reflects.
func TestMessagesIncludesReplyInProgress(t *testing.T) {
	m, _ := newTestManager(t, 0)
	id := ID("alice", yesmen.LeadName)
	settled := make(chan struct{}, 1)
	m.cfg.OnEvent = func(e Event) {
		var record struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(e.Data, &record) == nil && record.Type == "agent_settled" {
			select {
			case settled <- struct{}{}:
			default:
			}
		}
	}
	if err := m.Prompt(context.Background(), id, "hi"); err != nil {
		t.Fatal(err)
	}
	// Wait for the actual settle event: other fake records also advance seq,
	// and a late settle would clear the reply simulated below.
	select {
	case <-settled:
	case <-time.After(10 * time.Second):
		t.Fatal("the fake's run never settled")
	}
	m.mu.Lock()
	a := m.agents[id]
	m.mu.Unlock()
	start := m.number(a, "message_start", json.RawMessage(`{"type":"message_start","message":{"role":"assistant","timestamp":1}}`))
	m.number(a, "message_update", json.RawMessage(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"he"}}`))
	conv, err := m.Messages(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(conv.Live) != 2 || conv.Live[0].Seq != start.Seq || conv.Seq != start.Seq+1 {
		t.Errorf("live = %+v, seq = %d (start %d)", conv.Live, conv.Seq, start.Seq)
	}
	m.number(a, "message_end", json.RawMessage(`{"type":"message_end","message":{"role":"assistant","timestamp":1}}`))
	if conv, _ := m.Messages(context.Background(), id); len(conv.Live) != 0 {
		t.Errorf("a finished reply is still live: %+v", conv.Live)
	}

	// A reply cut off by its process exiting is not live after a restart.
	m.number(a, "message_start", json.RawMessage(`{"type":"message_start","message":{"role":"assistant","timestamp":2}}`))
	m.Stop(id)
	if conv, err := m.Messages(context.Background(), id); err != nil || len(conv.Live) != 0 {
		t.Errorf("after a restart: live = %+v, %v", conv.Live, err)
	}
}

// Messages reflects the events pi emitted before its answer, not those the
// reader handles after it while Messages waits for the lock.
func TestMessagesWatermarkIsTheAnswer(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_TRAIL=1")
	// Hold the reader after it numbers the reply that follows the answer.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // runs before StopAll
	m.cfg.OnEvent = func(Event) { <-release }
	for range 20 {
		m.mu.Lock()
		a := m.agents[ID("alice", yesmen.LeadName)]
		before, live := uint64(0), 0
		if a != nil {
			before, live = a.seq, len(a.turn)
		}
		m.mu.Unlock()
		conv, err := m.Messages(context.Background(), ID("alice", yesmen.LeadName))
		if err != nil {
			t.Fatal(err)
		}
		if conv.Seq != before || len(conv.Live) != live {
			t.Fatalf("seq = %d, live = %d; want %d and %d", conv.Seq, len(conv.Live), before, live)
		}
		release <- struct{}{} // let the reader finish the trailing event
	}
}

// Stopping must not wait for a write that pi, no longer reading, blocks.
func TestStopInterruptsBlockedSend(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "YESMAN_FAKE_PI=1", "FAKE_PI_DEAF=1")
	p, err := startProc(spec{Path: exe, Env: env}, func(*proc, string, json.RawMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() { sent <- p.send(map[string]string{"pad": strings.Repeat("x", 8<<20)}) }()
	deadline := time.Now().Add(10 * time.Second)
	for p.wmu.TryLock() { // until the send holds it
		p.wmu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the send never started")
		}
		time.Sleep(time.Millisecond)
	}
	stopped := make(chan struct{})
	go func() {
		p.stop(50 * time.Millisecond)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("stop waited for a blocked write")
	}
	select {
	case err := <-sent:
		if err == nil {
			t.Error("the interrupted write succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the blocked write never ended")
	}
}

func TestSleepIfIdle(t *testing.T) {
	m, _ := newTestManager(t, 0)
	id := ID("alice", yesmen.LeadName)
	ctx := context.Background()
	if err := m.Prompt(ctx, id, "hi"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, id, Idle)
	i, _ := m.Get(id)
	if i.StartedAt.IsZero() {
		t.Fatal("a running agent has no start time")
	}
	// Started after the change it is asked about: left running.
	if m.SleepIfIdle(id, i.StartedAt) {
		t.Error("slept an agent that started after the change")
	}
	if m.SleepIfIdle("alice/nobody", time.Now()) {
		t.Error("slept an agent that does not exist")
	}
	if !m.SleepIfIdle(id, time.Now()) {
		t.Fatal("did not sleep an idle agent")
	}
	if i, _ := m.Get(id); i.Status != Stopped || m.running(id) != nil {
		t.Fatalf("after sleeping: %s", i.Status)
	}
	if m.SleepIfIdle(id, time.Now()) {
		t.Error("slept an agent that was asleep")
	}
}

func TestSleepIfIdleLeavesPromptsAlone(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_HOLD_PROMPTS=1")
	id := ID("alice", yesmen.LeadName)
	errc := make(chan error, 1)
	go func() { errc <- m.Prompt(context.Background(), id, "hi") }()
	// The prompt is registered and pi has not answered it: the agent may
	// still read Idle, but a message is on its way.
	deadline := time.Now().Add(10 * time.Second)
	for {
		m.mu.Lock()
		a := m.agents[id]
		m.mu.Unlock()
		if a != nil {
			a.modelMu.Lock()
			n := a.prompts
			a.modelMu.Unlock()
			if n > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the prompt never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if m.SleepIfIdle(id, time.Now().Add(time.Hour)) {
		t.Fatal("slept an agent with a message on its way")
	}
	p := m.running(id)
	if _, err := p.call(context.Background(), map[string]any{"type": "test_release_prompts"}); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestPromptRestartsAnAgentSleptMeanwhile(t *testing.T) {
	m, _ := newTestManager(t, 0)
	id := ID("alice", yesmen.LeadName)
	ctx := context.Background()
	if err := m.Prompt(ctx, id, "hi"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, id, Idle)
	m.mu.Lock()
	a := m.agents[id]
	before := a.proc
	m.mu.Unlock()
	// Hold the lock SleepIfIdle holds; a message sent now waits for it.
	a.modelMu.Lock()
	errc := make(chan error, 1)
	go func() { errc <- m.Prompt(ctx, id, "again") }()
	time.Sleep(200 * time.Millisecond)
	m.Stop(id)
	a.modelMu.Unlock()
	if err := <-errc; err != nil {
		t.Fatalf("the message sent while the agent was put to sleep: %v", err)
	}
	if p := m.running(id); p == nil || p == before {
		t.Error("the agent did not start again for the message")
	}
}
