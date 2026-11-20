package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// Runs the public CLI with a prepared managed Pi and a deterministic, local
// pi-messages server. The first worker's provider rejects its request. The
// Lead reads the diagnostic, starts a replacement and delegates a check of
// the bytes it wrote. Hold the replacement across a Lead settlement.
func TestRunCLIWithManagedPi(t *testing.T) {
	managed := os.Getenv("YESMAN_TEST_RUNTIME")
	if managed == "" || runtime.GOOS == "windows" {
		t.Skip("requires YESMAN_TEST_RUNTIME and the Unix npm executable")
	}
	base := t.TempDir()
	profile, config, root := filepath.Join(base, "home"), filepath.Join(base, "pi"), filepath.Join(base, "existing project")
	for _, dir := range []string{root, config} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("YESMAN_HOME", profile)
	t.Setenv("YESMAN_PI", filepath.Join(managed, "node_modules", ".bin", "pi"))
	t.Setenv("PI_CODING_AGENT_DIR", config)
	t.Setenv("PI_OFFLINE", "1")
	const original = "\tFirst request\n  keep my spacing\n"
	originalJSON, _ := json.Marshal(original)
	const second = "A second message"
	const ask = "Ask for my choice"
	const failLead = "Fail the final Lead request"
	const rejection = "synthetic request exceeds fixture limit"
	const candidate = "from the shared working directory"
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(candidate)))
	var workerCalls, leadCalls, askCalls, reviewCalls atomic.Int32
	var inspected, assignedReplacement, assignedReview atomic.Bool
	gate := make(chan struct{})
	workerStarted := make(chan struct{}, 1)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		if !json.Valid(data) {
			t.Error("invalid provider request")
			return
		}
		text := func(s string) []any {
			return []any{map[string]any{"type": "text_start", "contentIndex": 0}, map[string]any{"type": "text_end", "contentIndex": 0, "content": s}}
		}
		call := func(id, name string, args map[string]any) []any {
			return []any{map[string]any{"type": "toolcall_start", "contentIndex": 0, "id": id, "toolName": name}, map[string]any{"type": "toolcall_end", "contentIndex": 0, "toolCall": map[string]any{"type": "toolCall", "id": id, "name": name, "arguments": args}}}
		}
		var events []any
		reason := "stop"
		bodyText := string(data)
		switch {
		case strings.Contains(bodyText, failLead):
			http.Error(w, "synthetic Lead request refused", http.StatusForbidden)
			return
		case strings.Contains(bodyText, `You are \"review\", a worker`):
			if reviewCalls.Add(1) == 1 {
				cmd := `node -e 'const fs=require("node:fs"), crypto=require("node:crypto"); console.log(crypto.createHash("sha256").update(fs.readFileSync("worker.txt")).digest("hex"))'`
				events, reason = call("check", "bash", map[string]any{"command": cmd}), "toolUse"
			} else {
				if !strings.Contains(bodyText, checksum) {
					t.Error("checking worker did not receive the digest of the replacement's bytes")
				}
				events = text("Checked worker.txt SHA-256 " + checksum)
			}
		case strings.Contains(bodyText, "a worker of the yesman named"):
			switch workerCalls.Add(1) {
			case 1:
				events, reason = call("draft", "write", map[string]any{"path": "worker.txt", "content": "draft"}), "toolUse"
			case 2:
				http.Error(w, rejection, http.StatusRequestEntityTooLarge)
				return
			case 3:
				workerStarted <- struct{}{}
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
				events, reason = call("wr", "write", map[string]any{"path": "worker.txt", "content": candidate}), "toolUse"
			case 4:
				events, reason = call("task", "yesman_board", map[string]any{"add": []any{map[string]any{"title": "Wrote shared file", "status": "done", "owner": "helper"}}}), "toolUse"
			default:
				events = text("Worker report delivered")
			}
		case strings.Contains(bodyText, ask):
			if askCalls.Add(1) == 1 {
				events, reason = call("ask", "yesman_needs_you", map[string]any{"add": map[string]string{"type": "decision", "title": "Choose a color"}}), "toolUse"
			} else {
				events = text("Which color do you prefer?")
			}
		case strings.Contains(bodyText, second):
			if !bytes.Contains(data, originalJSON) {
				t.Error("resumed provider context lost the original message")
			}
			events = text("Resumed reply")
		case strings.Contains(bodyText, `[yesman] Worker \"review\"`) && strings.Contains(bodyText, "Checked worker.txt SHA-256 "+checksum):
			events = text("Final Lead reply after checking worker report")
		case strings.Contains(bodyText, `[yesman] Worker \"helper\"`) && assignedReview.CompareAndSwap(false, true):
			events, reason = call("review", "yesman_worker_start", map[string]any{"name": "review", "title": "Check candidate", "task": "Report the current SHA-256 of worker.txt."}), "toolUse"
		case strings.Contains(bodyText, `[yesman] Worker \"first\"`) && inspected.CompareAndSwap(false, true):
			events, reason = call("inspect", "yesman_agents", map[string]any{}), "toolUse"
		case strings.Contains(bodyText, rejection) && assignedReplacement.CompareAndSwap(false, true):
			events, reason = call("replace", "yesman_worker_start", map[string]any{"name": "helper", "title": "Write candidate", "task": "Write worker.txt and report."}), "toolUse"
		case leadCalls.Add(1) == 1:
			events, reason = call("start", "yesman_worker_start", map[string]any{"name": "first", "title": "First attempt", "task": "Write worker.txt and report."}), "toolUse"
		default:
			events = text("Lead delegated")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v any) { b, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", b) }
		send(map[string]any{"type": "start"})
		for _, e := range events {
			send(e)
		}
		send(map[string]any{"type": "done", "reason": reason, "usage": map[string]any{"input": 1, "output": 1, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 2, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}}})
	}))
	defer mock.Close()
	write := func(name string, data any) {
		b, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(config, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	const model = "local/only"
	write("settings.json", map[string]any{"defaultProjectTrust": "no", "compaction": map[string]bool{"enabled": false}, "retry": map[string]bool{"enabled": false}})
	write("models.json", map[string]any{"providers": map[string]any{"fixture": map[string]any{"baseUrl": mock.URL, "api": "pi-messages", "apiKey": "fixture", "models": []any{map[string]any{"id": model, "input": []string{"text"}}}}}})
	args := []string{"--workspace", "team", "--provider", "fixture", "--model", model, "--effort", "off", "--timeout", "35s"}
	run := func(args []string, prompt string) (agent.RunResult, int) {
		var stdout, stderr bytes.Buffer
		code := runCLI(t.Context(), args, strings.NewReader(prompt), &stdout, &stderr)
		var out agent.RunResult
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Errorf("%s: %v", stderr.String(), err)
		}
		return out, code
	}
	bad := append([]string{}, args...)
	bad[5] = "unknown"
	if out, code := run(append(bad, "--new", "--root", root), original); code != 1 || out.Status != "failure" {
		t.Fatalf("wrong model: %+v (exit %d)", out, code)
	}
	if _, err := os.Lstat(filepath.Join(root, ".yesman")); !os.IsNotExist(err) {
		t.Fatal("bad model modified the supplied root")
	}
	promptFile := filepath.Join(base, "instruction.txt")
	if err := os.WriteFile(promptFile, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan agent.RunResult, 1)
	go func() {
		out, code := run(append(args, "--new", "--root", root, "--prompt-file", promptFile), "")
		if code != 0 {
			t.Errorf("run exited %d after reassignment: %+v", code, out)
		}
		done <- out
	}()
	select {
	case <-workerStarted:
	case out := <-done:
		t.Fatalf("ended before worker: %+v", out)
	case <-time.After(35 * time.Second):
		t.Fatal("worker did not start")
	}
	// The provider has seen the Lead's follow-up to the startWorker result;
	// let that turn settle while the worker is still blocked in its request.
	deadline := time.Now().Add(8 * time.Second)
	for leadCalls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case out := <-done:
		close(gate)
		t.Fatalf("ended while worker was active: %+v", out)
	case <-time.After(400 * time.Millisecond):
	}
	close(gate)
	out := <-done
	if out.Status != "completed" || out.Reply != "Final Lead reply after checking worker report" || out.SubmissionID == "" || out.InterruptedAgents != nil {
		t.Errorf("first result: %+v", out)
	}
	if b, err := os.ReadFile(filepath.Join(root, "worker.txt")); err != nil || string(b) != candidate {
		t.Fatalf("worker output: %q %v", b, err)
	}
	var board yesmen.Board
	data, err := os.ReadFile(filepath.Join(root, ".yesman", "board.json"))
	if err != nil || json.Unmarshal(data, &board) != nil || len(board.Tasks) != 1 || board.Tasks[0].Status != "done" {
		t.Fatalf("worker board was not preserved: %s %v", data, err)
	}
	for _, name := range []string{"lead", "first", "helper", "review"} {
		logs, _ := filepath.Glob(filepath.Join(root, ".yesman", "sessions", name, "*.jsonl"))
		if len(logs) != 1 {
			t.Fatalf("%s session files: %v", name, logs)
		}
		data, _ := os.ReadFile(logs[0])
		n := 0
		var sawFailure bool
		for _, line := range bytes.Split(data, []byte("\n")) {
			var e struct {
				Type    string
				Cwd     string
				Message struct {
					Role, Model, Provider, StopReason, ErrorMessage string
					Content                                         json.RawMessage
				}
			}
			_ = json.Unmarshal(line, &e)
			if e.Type == "session" && e.Cwd != out.Root {
				t.Fatalf("%s cwd %s; root %s", name, e.Cwd, out.Root)
			}
			if e.Message.Role == "assistant" && (e.Message.Model != model || e.Message.Provider != "fixture") {
				t.Fatalf("%s selection: %s/%s", name, e.Message.Provider, e.Message.Model)
			}
			if name == "first" && e.Message.Role == "assistant" && e.Message.StopReason == "error" && strings.Contains(e.Message.ErrorMessage, rejection) {
				sawFailure = true
			}
			if name == "lead" && e.Message.Role == "user" && bytes.Contains(e.Message.Content, []byte(`[yesman] Worker \"first\"`)) && bytes.Contains(e.Message.Content, []byte("model error")) {
				sawFailure = true
			}
			if name == "lead" && e.Message.Role == "user" && bytes.Contains(e.Message.Content, originalJSON) {
				n++
			}
		}
		if (name == "lead" || name == "first") && !sawFailure {
			t.Errorf("%s session lacks the first worker's model diagnostic", name)
		}
		if name == "lead" && n != 1 {
			t.Fatalf("the Lead recorded %d original messages", n)
		}
	}
	if out, code := run([]string{"--workspace", "team", "--timeout", "30s"}, second); code != 0 || out.Reply != "Resumed reply" {
		t.Fatalf("resume: %+v (exit %d)", out, code)
	}
	if out, code := run(args, ask); code != 2 || out.Status != "needs_input" || len(out.Handoffs) != 1 || out.Handoffs[0].Title != "Choose a color" || out.InterruptedAgents != nil {
		t.Fatalf("handoff: %+v (exit %d)", out, code)
	}
	if out, code := run(args, failLead); code != 1 || out.Status != "failure" || !strings.Contains(out.Error, "synthetic Lead request refused") {
		t.Fatalf("failed Lead: %+v (exit %d)", out, code)
	}
}
