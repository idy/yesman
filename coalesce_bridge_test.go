package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

// TestCoalesceBrowserBridge serves the production page's calls from the
// production Go service and Manager, the production extension, managed pi
// (capability 4) and a local deterministic provider whose every call waits
// for its own gate. Only scripts/check-coalesce-browser.mjs opts in; all data
// and configuration are synthetic and local.
func TestCoalesceBrowserBridge(t *testing.T) {
	ready := os.Getenv("YESMAN_COALESCE_BRIDGE")
	runtime := os.Getenv("YESMAN_TEST_RUNTIME")
	if ready == "" || runtime == "" {
		t.Skip("optional isolated browser bridge")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	config := filepath.Join(temp, "config")
	_ = os.MkdirAll(config, 0700)
	settings := map[string]any{"packages": []string{}, "extensions": []string{filepath.Join(root, "scripts", "fixtures", "append-provider.ts")}, "skills": []string{}, "promptTemplates": []string{}, "themes": []string{}, "defaultProjectTrust": "no", "defaultProvider": "append-test", "defaultModel": "m", "compaction": map[string]bool{"enabled": false}, "retry": map[string]bool{"enabled": false}}
	data, _ := json.Marshal(settings)
	if err = os.WriteFile(filepath.Join(config, "settings.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	holdContext := false
	contextHeld := 0
	contextRelease := make(chan struct{})
	extensionAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/context" {
			mu.Lock()
			hold, release := holdContext, contextRelease
			if hold {
				contextHeld++
			}
			mu.Unlock()
			if hold {
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}
			io.WriteString(w, "Isolated coalescing browser fixture")
		} else {
			io.WriteString(w, `{"env":{},"hide":[]}`)
		}
	}))
	defer extensionAPI.Close()
	store := yesmen.New(filepath.Join(temp, "workspaces"), filepath.Join(temp, "archive"))
	if _, err = store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	events := []agent.Event{}
	var submits, payloadReads int
	dropReply := false
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	gate, audit := filepath.Join(temp, "gate"), filepath.Join(temp, "audit")
	manager := agent.New(agent.Config{Pi: node, Command: func(context.Context) (string, []string, error) {
		return node, []string{filepath.Join(runtime, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "cli.js")}, nil
	}, Yesmen: store, Extension: filepath.Join(root, "pi", "yesman.ts"),
		Env: append(isolatedRecallBridgeEnv(), []string{"HOME=" + temp, "USERPROFILE=" + temp, "APPDATA=" + temp, "LOCALAPPDATA=" + temp, "PI_CODING_AGENT_DIR=" + config, "YESMAN_API=" + extensionAPI.URL, "APPEND_GATE=" + gate, "APPEND_AUDIT=" + audit}...),
		OnEvent: func(e agent.Event) { mu.Lock(); events = append(events, e); mu.Unlock() }})
	defer manager.StopAll()
	svc := &service{app: &app{yesmen: store, agents: manager, pool: pool.New(filepath.Join(temp, "pool"))}}
	// A real active turn (provider call 1, held), so composer inputs wait in
	// pi's steering queue where later ones can join them.
	if _, err = manager.Submit(context.Background(), "alice/lead", fmtFixtureID(1), "hold", nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var doneOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "content-type")
		if r.Method == http.MethodOptions {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/events" {
			cursor, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
			mu.Lock()
			if cursor < 0 || cursor > len(events) {
				cursor = 0
			}
			items := append([]agent.Event(nil), events[cursor:]...)
			end := len(events)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"events": items, "cursor": end, "agents": manager.List()})
			return
		}
		if r.URL.Path == "/stop" {
			doneOnce.Do(func() { close(done) })
			io.WriteString(w, `{}`)
			return
		}
		var req struct {
			Method string
			Args   []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		text := func(n int) string {
			var s string
			if n < len(req.Args) {
				_ = json.Unmarshal(req.Args[n], &s)
			}
			return s
		}
		since := func(n int) time.Time {
			var at time.Time
			if n < len(req.Args) {
				_ = json.Unmarshal(req.Args[n], &at)
			}
			return at
		}
		var value any
		var callErr error
		switch strings.TrimPrefix(req.Method, "Yesman.") {
		case "State":
			value, callErr = svc.State(r.Context())
		case "Messages":
			value, callErr = svc.Messages(r.Context(), text(0))
		case "Models":
			value, callErr = svc.Models(r.Context(), text(0))
		case "Submit", "SubmitCoalescing":
			var images []agent.Image
			_ = json.Unmarshal(req.Args[3], &images)
			if strings.HasSuffix(req.Method, "SubmitCoalescing") {
				value, callErr = svc.SubmitCoalescing(r.Context(), text(0), text(1), text(2), images, since(4))
			} else {
				value, callErr = svc.Submit(r.Context(), text(0), text(1), text(2), images, since(4))
			}
			mu.Lock()
			submits++
			drop := dropReply
			dropReply = false
			mu.Unlock()
			if drop {
				// The service ran; only its answer to the page is lost.
				value, callErr = nil, errors.New("fixture: the bridge lost the reply")
			}
		case "CancelSubmission":
			value, callErr = svc.CancelSubmission(r.Context(), text(0), text(1), since(2))
		case "PromoteSubmission":
			value, callErr = svc.PromoteSubmission(r.Context(), text(0), text(1), since(2))
		case "SubmissionPayload":
			mu.Lock()
			payloadReads++
			mu.Unlock()
			value, callErr = svc.SubmissionPayload(text(0), text(1), since(2))
		case "SubmissionText":
			value, callErr = svc.SubmissionText(text(0), text(1), since(2))
		case "AckRecall":
			callErr = svc.AckRecall(text(0), text(1), since(2))
		case "Abort":
			callErr = svc.Abort(r.Context(), text(0))
		case "ReleaseCall":
			callErr = os.WriteFile(gate+"."+text(0), []byte("release"), 0600)
		case "HoldContext":
			mu.Lock()
			holdContext, contextHeld, contextRelease = true, 0, make(chan struct{})
			mu.Unlock()
		case "ReleaseContext":
			mu.Lock()
			if holdContext {
				holdContext = false
				close(contextRelease)
			}
			mu.Unlock()
		case "DropNextReply":
			mu.Lock()
			dropReply = true
			mu.Unlock()
		case "RetrySubmit":
			// A duplicate of a submission the page already made, as a lost
			// bridge answer would make it.
			var images []agent.Image
			_ = json.Unmarshal(req.Args[3], &images)
			value, callErr = svc.SubmitCoalescing(r.Context(), text(0), text(1), text(2), images, since(4))
		case "Audit":
			raw, _ := os.ReadFile(audit)
			var calls []json.RawMessage
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				if line != "" {
					calls = append(calls, json.RawMessage(line))
				}
			}
			value = calls
		case "Counts":
			mu.Lock()
			value = map[string]int{"submit": submits, "payload": payloadReads, "contextHeld": contextHeld}
			mu.Unlock()
		case "Repos", "Clones":
			value = []any{}
		case "TakeFocus":
			value = ""
		default:
			value = map[string]any{}
		}
		result := map[string]any{"value": value}
		if callErr != nil {
			result["error"] = callErr.Error()
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	if err = os.WriteFile(ready, []byte(server.URL), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(150 * time.Second):
		t.Fatal("browser bridge timed out")
	}
	mu.Lock()
	if holdContext {
		close(contextRelease)
	}
	mu.Unlock()
	_ = os.WriteFile(gate+".all", []byte("release"), 0600)
}
