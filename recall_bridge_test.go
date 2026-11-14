package main

import (
	"context"
	"encoding/json"
	"fmt"
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

// Only the browser harness opts in. All data/config/provider calls are local;
// product Submit/CancelSubmission/AckRecall/Messages execute unchanged.
func TestRecallBrowserBridge(t *testing.T) {
	ready := os.Getenv("YESMAN_RECALL_BRIDGE")
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
	settings := map[string]any{"packages": []string{}, "extensions": []string{filepath.Join(root, "scripts", "fixtures", "delivery-provider.ts")}, "defaultProjectTrust": "no", "defaultProvider": "delivery-test", "defaultModel": "m", "compaction": map[string]bool{"enabled": false}, "retry": map[string]bool{"enabled": false}}
	data, _ := json.Marshal(settings)
	if err = os.WriteFile(filepath.Join(config, "settings.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	extensionAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/context" {
			io.WriteString(w, "Isolated browser fixture")
		} else {
			io.WriteString(w, `{"env":{},"hide":[]}`)
		}
	}))
	defer extensionAPI.Close()
	store := yesmen.New(filepath.Join(temp, "workspaces"), filepath.Join(temp, "archive"))
	for _, name := range []string{"alice", "bob"} {
		if _, err = store.Hire(name); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	events := []agent.Event{}
	var cancelHeld bool
	var releaseCancel chan struct{}
	var cancelCalls, submitCalls int
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	manager := agent.New(agent.Config{Pi: node, Command: func(context.Context) (string, []string, error) {
		return node, []string{filepath.Join(runtime, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "cli.js")}, nil
	}, Yesmen: store, Extension: filepath.Join(root, "pi", "yesman.ts"),
		Env: append(isolatedRecallBridgeEnv(), []string{"HOME=" + temp, "PI_CODING_AGENT_DIR=" + config, "YESMAN_API=" + extensionAPI.URL, "DELIVERY_GATE=" + filepath.Join(temp, "gate"), "DELIVERY_AUDIT=" + filepath.Join(temp, "audit")}...), OnEvent: func(e agent.Event) { mu.Lock(); events = append(events, e); mu.Unlock() }})
	defer manager.StopAll()
	svc := &service{app: &app{yesmen: store, agents: manager, pool: pool.New(filepath.Join(temp, "pool"))}}
	// Reserve a real active turn, so browser inputs use native steering.
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
		var value any
		var callErr error
		switch strings.TrimPrefix(req.Method, "Yesman.") {
		case "State":
			value, callErr = svc.State(r.Context())
		case "Messages":
			value, callErr = svc.Messages(r.Context(), text(0))
		case "Models":
			value, callErr = svc.Models(r.Context(), text(0))
		case "Submit":
			mu.Lock()
			submitCalls++
			mu.Unlock()
			var images []agent.Image
			var since time.Time
			_ = json.Unmarshal(req.Args[3], &images)
			_ = json.Unmarshal(req.Args[4], &since)
			value, callErr = svc.Submit(r.Context(), text(0), text(1), text(2), images, since)
		case "CancelSubmission":
			mu.Lock()
			cancelCalls++
			hold, ch := cancelHeld, releaseCancel
			mu.Unlock()
			var since time.Time
			_ = json.Unmarshal(req.Args[2], &since)
			value, callErr = svc.CancelSubmission(r.Context(), text(0), text(1), since)
			if hold {
				select {
				case <-ch:
				case <-r.Context().Done():
					return
				}
			}
		case "SubmissionText":
			var since time.Time
			_ = json.Unmarshal(req.Args[2], &since)
			value, callErr = svc.SubmissionText(text(0), text(1), since)
		case "AckRecall":
			var since time.Time
			_ = json.Unmarshal(req.Args[2], &since)
			callErr = svc.AckRecall(text(0), text(1), since)
		case "HoldCancel":
			mu.Lock()
			cancelHeld = true
			releaseCancel = make(chan struct{})
			mu.Unlock()
		case "ReleaseCancel":
			mu.Lock()
			if cancelHeld {
				cancelHeld = false
				close(releaseCancel)
			}
			mu.Unlock()
		case "ReleaseProvider":
			callErr = os.WriteFile(filepath.Join(temp, "gate"), []byte("release"), 0600)
		case "Counts":
			mu.Lock()
			value = map[string]int{"cancel": cancelCalls, "submit": submitCalls}
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
	case <-time.After(90 * time.Second):
		t.Fatal("browser bridge timed out")
	}
}
func isolatedRecallBridgeEnv() []string {
	var clear []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "PATH", "PATHEXT", "SYSTEMROOT", "COMSPEC", "TMPDIR", "TEMP", "TMP":
		default:
			clear = append(clear, key+"=")
		}
	}
	return clear
}
func fmtFixtureID(n int) string {
	return fmt.Sprintf("%012x%036x", time.Now().UnixMilli(), n)
}
