package agent

// A test-only HTTP transport for the production page. Queue operations below
// call the unchanged Manager and real pinned pi; only shell/clipboard UI is mocked.
import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestQueueActionsBrowserBridge(t *testing.T) {
	ready := os.Getenv("YESMAN_QUEUE_BRIDGE")
	if ready == "" {
		t.Skip("optional local browser transport")
	}
	var mu sync.Mutex
	events := []Event{}
	gate := newPreparationGate()
	f := newPromotionFixtureWithContext(t, func(e Event) { mu.Lock(); events = append(events, e); mu.Unlock() }, gate.serve)
	t.Cleanup(gate.open)
	var promotes int
	bindings := map[string][]string{}
	done := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "content-type")
		if r.Method == http.MethodOptions {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/stop" {
			once.Do(func() { close(done) })
			io.WriteString(w, `{}`)
			return
		}
		if r.URL.Path == "/events" {
			cursor, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
			mu.Lock()
			if cursor < 0 || cursor > len(events) {
				cursor = 0
			}
			items := append([]Event{}, events[cursor:]...)
			end := len(events)
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"events": items, "cursor": end, "agents": f.m.List()})
			return
		}
		var req struct {
			Method string
			Args   []json.RawMessage
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		text := func(n int) string {
			var s string
			if n < len(req.Args) {
				json.Unmarshal(req.Args[n], &s)
			}
			return s
		}
		since := func(n int) time.Time {
			var s time.Time
			if n < len(req.Args) {
				json.Unmarshal(req.Args[n], &s)
			}
			return s
		}
		var value any
		var err error
		switch strings.TrimPrefix(req.Method, "Yesman.") {
		case "State":
			ys, e := f.store.List()
			err = e
			value = map[string]any{"yesmen": ys, "agents": f.m.List(), "home": "fixture", "pool": "fixture"}
		case "Messages":
			value, err = f.m.Messages(r.Context(), text(0))
		case "Models":
			value, err = f.m.Models(r.Context(), text(0))
		case "Submit", "FixtureFollowUp":
			var images []Image
			json.Unmarshal(req.Args[3], &images)
			if req.Method == "FixtureFollowUp" {
				value, err = f.m.submit(r.Context(), text(0), text(1), text(2), images, "followUp", since(4))
			} else {
				value, err = f.m.Submit(r.Context(), text(0), text(1), text(2), images, since(4))
			}
		case "PromoteSubmission":
			mu.Lock()
			promotes++
			mu.Unlock()
			value, err = f.m.PromoteSubmission(r.Context(), text(0), text(1), since(2))
		case "CancelSubmission":
			value, err = f.m.CancelSubmission(r.Context(), text(0), text(1), since(2))
		case "SubmissionPayload":
			value, err = f.m.SubmissionPayload(text(0), text(1), since(2))
		case "SubmissionText":
			value, err = f.m.SubmissionText(text(0), text(1), since(2))
		case "AckRecall":
			err = f.m.AckRecall(text(0), text(1), since(2))
		case "NativeSnapshot":
			f.m.mu.Lock()
			value = f.m.agents[f.info.ID].nativeQueue
			f.m.mu.Unlock()
		case "Counts":
			mu.Lock()
			value = map[string]int{"promote": promotes}
			mu.Unlock()
		case "HoldContext":
			gate.armed.Store(true)
		case "ContextReady":
			select {
			case <-gate.entered:
				value = true
			default:
				value = false
			}
		case "ReleaseContext":
			gate.open()
		case "Abort":
			err = f.m.Abort(r.Context(), text(0))
		case "HoldProvider":
			_ = os.Remove(f.gate)
			_ = os.Remove(filepath.Join(filepath.Dir(f.gate), "audit"))
		case "ProviderReady":
			_, statErr := os.Stat(filepath.Join(filepath.Dir(f.gate), "audit"))
			value = statErr == nil
		case "ReleaseProvider":
			err = os.WriteFile(f.gate, []byte("release"), 0600)
		case "Shortcuts", "SetShortcuts", "ResetShortcuts":
			mu.Lock()
			if strings.TrimPrefix(req.Method, "Yesman.") == "ResetShortcuts" {
				bindings = map[string][]string{}
			}
			if strings.TrimPrefix(req.Method, "Yesman.") == "SetShortcuts" {
				var changes map[string]*[]string
				_ = json.Unmarshal(req.Args[0], &changes)
				for key, list := range changes {
					if list == nil {
						delete(bindings, key)
					} else {
						bindings[key] = append([]string{}, (*list)...)
					}
				}
			}
			copyBindings := map[string][]string{}
			for key, list := range bindings {
				copyBindings[key] = append([]string{}, list...)
			}
			mu.Unlock()
			value = copyBindings
		case "Repos", "Clones":
			value = []any{}
		default:
			value = map[string]any{}
		}
		result := map[string]any{"value": value}
		if err != nil {
			result["error"] = err.Error()
		}
		json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	if err := os.WriteFile(ready, []byte(server.URL), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(150 * time.Second):
		t.Fatal("browser transport timeout")
	}
}
