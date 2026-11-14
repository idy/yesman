package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/server"
	"github.com/idy/yesman/internal/yesmen"
)

// TestMain lets the test binary double as a fake pi for the Needs-you
// browser fixture.
func TestMain(m *testing.M) {
	if os.Getenv("YESMAN_NEEDS_YOU_FAKE_PI") != "" {
		needsYouFakePi()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// needsYouFakePi answers pi's RPC as a scripted agent. A prompt "ASK {…}"
// asks the dialog given, which holds the run until it is answered. A prompt
// "NEEDSYOU {…}" makes the call the extension's yesman_needs_you tool makes:
// POST /v1/needs-you with this agent's own credential, recorded in its
// history as that tool call. Every prompt is reported as test_prompt.
func needsYouFakePi() {
	out := bufio.NewWriter(os.Stdout)
	write := func(v any) {
		data, _ := json.Marshal(v)
		out.Write(append(data, '\n'))
		out.Flush()
	}
	respond := func(id, command string, data any) {
		write(map[string]any{"type": "response", "id": id, "command": command, "success": true, "data": data})
	}
	text := func(s string) []any { return []any{map[string]any{"type": "text", "text": s}} }
	history := []any{}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var cmd struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &cmd) != nil {
			continue
		}
		switch cmd.Type {
		case "extension_ui_response":
			write(map[string]any{"type": "test_ui_answer", "data": json.RawMessage(sc.Bytes())})
			write(map[string]any{"type": "agent_settled", "aborted": false})
		case "get_state":
			respond(cmd.ID, cmd.Type, map[string]any{"model": map[string]any{"id": "fixture", "provider": "local", "name": "Fixture"}, "thinkingLevel": "off"})
		case "get_messages":
			respond(cmd.ID, cmd.Type, map[string]any{"messages": history})
		case "prompt":
			respond(cmd.ID, cmd.Type, nil)
			write(map[string]any{"type": "test_prompt", "message": cmd.Message})
			write(map[string]any{"type": "agent_start"})
			now := time.Now().UnixMilli()
			switch {
			case strings.HasPrefix(cmd.Message, "ASK "):
				var d map[string]any
				_ = json.Unmarshal([]byte(strings.TrimPrefix(cmd.Message, "ASK ")), &d)
				d["type"] = "extension_ui_request"
				write(d)
				continue
			case strings.HasPrefix(cmd.Message, "NEEDSYOU "):
				body := strings.TrimPrefix(cmd.Message, "NEEDSYOU ")
				var args map[string]any
				_ = json.Unmarshal([]byte(body), &args)
				call, _ := args["toolCall"].(string)
				result, failed := needsYouAPI(body)
				history = append(history,
					map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "toolCall", "id": call, "name": "yesman_needs_you", "arguments": args}}, "timestamp": now, "stopReason": "toolUse"},
					map[string]any{"role": "toolResult", "toolCallId": call, "toolName": "yesman_needs_you", "content": text(result), "isError": failed, "timestamp": now + 1},
					map[string]any{"role": "assistant", "content": text("I left that with you under Needs you."), "timestamp": now + 2, "stopReason": "stop"})
				write(map[string]any{"type": "test_api", "result": result, "failed": failed})
			}
			write(map[string]any{"type": "agent_settled", "aborted": false})
		default:
			respond(cmd.ID, cmd.Type, nil)
		}
	}
}

func needsYouAPI(body string) (string, bool) {
	req, _ := http.NewRequest("POST", os.Getenv("YESMAN_API")+"/v1/needs-you", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+os.Getenv("YESMAN_TOKEN"))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error(), true
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return string(data), res.StatusCode != http.StatusOK
}

// TestNeedsYouBrowserBridge is the fixture scripts/check-needs-you-browser.mjs
// drives: the production page, the actual service, Store, Manager and the
// lead API, with scripted pi processes. Every workspace, setting and process
// lives in t.TempDir; no model is called.
func TestNeedsYouBrowserBridge(t *testing.T) {
	if os.Getenv("YESMAN_TEST_NEEDS_YOU_BROWSER") != "1" {
		t.Skip("run through scripts/check-needs-you-browser.mjs")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := crewApp(t)
	var mu sync.Mutex
	var notes []map[string]string
	prompts := map[string][]string{}
	a.notifier = func(id, title, body string) {
		mu.Lock()
		notes = append(notes, map[string]string{"id": id, "title": title, "body": body})
		mu.Unlock()
	}
	// No native app runs here to keep the machine awake.
	a.keepAwake = func(string) func() { return func() {} }
	a.pool = pool.New(a.home.Repos())
	srv := &server.Server{Pool: a.pool, Yesmen: a.yesmen, NeedsYouChanged: a.needsYouChanged}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	a.agents = agent.New(agent.Config{
		Pi: exe, Extension: filepath.Join(t.TempDir(), "none.ts"),
		Env:    []string{"YESMAN_NEEDS_YOU_FAKE_PI=1", "YESMAN_API=" + srv.URL()},
		Yesmen: a.yesmen, OnChange: a.agentsChanged,
		OnEvent: func(e agent.Event) {
			var head struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}
			if json.Unmarshal(e.Data, &head) == nil && head.Type == "test_prompt" {
				mu.Lock()
				prompts[e.Agent] = append(prompts[e.Agent], head.Message)
				mu.Unlock()
			}
		},
	})
	srv.Agents = a.agents
	t.Cleanup(a.agents.StopAll)
	s := &service{app: a}
	ctx := context.Background()
	if _, err := s.AddCrewOption(ctx, "magpie", "auto/claude-opus-5-5", "xhigh", nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob"} {
		if _, err := s.Hire(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Rename("alice", "Alice 团队"); err != nil {
		t.Fatal(err)
	}
	title := "Publish the package"
	if _, err := a.yesmen.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: &title}}}); err != nil {
		t.Fatal(err)
	}
	// Alice keeps 15 notes, as the Info card counts them.
	alice, err := a.yesmen.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 15 {
		note := filepath.Join(alice.KnowledgeDir(), fmt.Sprintf("note-%02d.md", i+1))
		if err := os.WriteFile(note, []byte(fmt.Sprintf("# Note %d\n", i+1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, value any, err error) {
		response := map[string]any{"value": value}
		if err != nil {
			response["error"] = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
	arg := func(args []json.RawMessage, i int, v any) error {
		if i >= len(args) {
			return fmt.Errorf("missing argument %d", i)
		}
		return json.Unmarshal(args[i], v)
	}
	mux.HandleFunc("POST /fixture/call", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Args   []json.RawMessage `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			reply(w, nil, err)
			return
		}
		var (
			value any
			err   error
			id    string
		)
		switch req.Method {
		case "State":
			value, err = s.State(r.Context())
		case "Messages":
			if err = arg(req.Args, 0, &id); err == nil {
				value, err = s.Messages(r.Context(), id)
			}
		case "Info":
			if err = arg(req.Args, 0, &id); err == nil {
				value, err = s.Info(r.Context(), id)
			}
		case "ListFiles", "ReadFile":
			var path string
			if err = arg(req.Args, 0, &id); err == nil {
				if err = arg(req.Args, 1, &path); err == nil {
					if req.Method == "ListFiles" {
						value, err = s.ListFiles(id, path)
					} else {
						value, err = s.ReadFile(id, path)
					}
				}
			}
		case "Answer":
			var requestID string
			var payload map[string]any
			if err = arg(req.Args, 0, &id); err == nil {
				if err = arg(req.Args, 1, &requestID); err == nil {
					if err = arg(req.Args, 2, &payload); err == nil {
						err = s.Answer(id, requestID, payload)
					}
				}
			}
		case "ResolveHandoff":
			var name, handoff, resolution string
			var hiredAt time.Time
			if err = arg(req.Args, 0, &name); err == nil {
				if err = arg(req.Args, 1, &hiredAt); err == nil {
					if err = arg(req.Args, 2, &handoff); err == nil {
						if err = arg(req.Args, 3, &resolution); err == nil {
							value, err = s.ResolveHandoff(name, hiredAt, handoff, resolution)
						}
					}
				}
			}
		default:
			err = fmt.Errorf("unsupported fixture method %s", req.Method)
		}
		reply(w, value, err)
	})
	// The scenario's other actors: pi processes asking, the lead's tool calls,
	// and the user's archive, hire and remove actions elsewhere.
	mux.HandleFunc("POST /fixture/control", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Op      string `json:"op"`
			Yesman  string `json:"yesman"`
			Agent   string `json:"agent"`
			Name    string `json:"name"`
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			reply(w, nil, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		var (
			value any
			err   error
		)
		switch req.Op {
		case "start-worker":
			value, err = a.agents.StartWorker(ctx, req.Yesman, req.Name, "Clean the build output", req.Message)
		case "prompt":
			err = a.agents.Prompt(ctx, req.Agent, req.Message)
		case "agent":
			value, err = a.agents.Get(req.Agent)
		case "dismiss":
			value, err = s.Dismiss(req.Yesman)
		case "hire":
			value, err = s.Hire(ctx, req.Yesman)
		case "remove-worker":
			err = s.RemoveWorker(req.Agent)
		case "observed":
			mu.Lock()
			value = map[string]any{"notes": append([]map[string]string(nil), notes...), "prompts": prompts}
			data, _ := json.Marshal(value)
			mu.Unlock()
			value = json.RawMessage(data)
		default:
			err = fmt.Errorf("unsupported fixture op %s", req.Op)
		}
		reply(w, value, err)
	})
	web, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", http.FileServerFS(web))
	page := httptest.NewServer(mux)
	defer page.Close()
	fmt.Println("NEEDS_YOU_BROWSER_URL=" + page.URL)
	_, _ = io.Copy(io.Discard, os.Stdin)
}
