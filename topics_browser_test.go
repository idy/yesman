package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/server"
	"github.com/idy/yesman/internal/yesmen"
)

// TestTopicsBrowserBridge serves the production page with the actual Go
// service, Manager and server behind a controlled MyGo bridge, for
// scripts/topics-browser.mjs. Agents run the installed pi with yesman's
// extension and the local deterministic provider; a temporary data
// directory holds everything. It runs until its stdin closes.
func TestTopicsBrowserBridge(t *testing.T) {
	if os.Getenv("YESMAN_TEST_TOPICS_BROWSER") == "" {
		t.Skip("run by scripts/topics-browser.mjs")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the pi wrapper is a shell script")
	}
	cwd, _ := os.Getwd()
	tmp := t.TempDir()
	gate := filepath.Join(tmp, "gate")
	wrapper, command, cfgDir, _ := localTopicsPi(t, cwd, tmp)

	store := yesmen.New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	y, err := store.HireNamed("Alpha Lab", "")
	must(t, err)
	must(t, os.WriteFile(filepath.Join(y.KnowledgeDir(), "icons.md"), []byte("# Icons\n\nUse the Phosphor helper.\n"), 0o644))
	str := func(s string) *string { return &s }
	_, err = store.ChangeBoard(y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: str("Fixture task"), Status: str("doing")}, {Title: str("Second task")}}})
	must(t, err)

	var mu sync.Mutex
	type event struct {
		Name string `json:"name"`
		Data any    `json:"data"`
	}
	var events []event
	emit := func(name string, data any) { mu.Lock(); events = append(events, event{name, data}); mu.Unlock() }

	p := pool.New(filepath.Join(tmp, "repos"))
	srv := &server.Server{Pool: p, Yesmen: store, TopicsChanged: func(n string) { emit("topics", n) }}
	must(t, srv.Start())
	agents := agent.New(agent.Config{
		Pi: wrapper, Command: command, Extension: filepath.Join(cwd, "pi", "yesman.ts"), Yesmen: store,
		Env:      []string{"YESMAN_API=" + srv.URL(), "PI_CODING_AGENT_DIR=" + cfgDir, "TOPICS_AUDIT=" + filepath.Join(tmp, "audit.jsonl"), "TOPICS_GATE=" + gate},
		OnEvent:  func(e agent.Event) { emit("agent-event", e) },
		OnChange: func(list []agent.Info) { emit("agents", list) },
	})
	defer agents.StopAll()
	srv.Agents = agents
	a := &app{pool: p, yesmen: store, agents: agents, onTopics: func(n string) { emit("topics", n) }}
	a.home.Root = tmp
	svc := reflect.ValueOf(&service{app: a})
	forwarded := map[string]bool{"State": true, "Messages": true, "Submit": true, "Abort": true, "Board": true, "Info": true,
		"Topics": true, "CreateTopic": true, "UpdateTopic": true, "SubmitTopic": true,
		"CancelSubmission": true, "PromoteSubmission": true, "SubmissionPayload": true, "SubmissionText": true, "AckRecall": true}
	errType := reflect.TypeOf((*error)(nil)).Elem()
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.Dir(filepath.Join(cwd, "web"))))
	mux.HandleFunc("POST /fixture/call", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Args   []json.RawMessage `json:"args"`
		}
		reply := func(v any, err error) {
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": v})
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !forwarded[req.Method] {
			reply(nil, fmt.Errorf("fixture does not forward %q", req.Method))
			return
		}
		m := svc.MethodByName(req.Method)
		var in []reflect.Value
		next := 0
		for i := 0; i < m.Type().NumIn(); i++ {
			pt := m.Type().In(i)
			if pt == ctxType {
				in = append(in, reflect.ValueOf(context.Background()))
				continue
			}
			v := reflect.New(pt)
			if next < len(req.Args) && string(req.Args[next]) != "null" {
				if err := json.Unmarshal(req.Args[next], v.Interface()); err != nil {
					reply(nil, fmt.Errorf("argument %d of %s: %w", next, req.Method, err))
					return
				}
			}
			next++
			in = append(in, v.Elem())
		}
		var value any
		var err error
		for _, o := range m.Call(in) {
			if o.Type() == errType {
				if !o.IsNil() {
					err = o.Interface().(error)
				}
				continue
			}
			value = o.Interface()
		}
		reply(value, err)
	})
	// The lead's own change, as its yesman_topics tool makes it.
	mux.HandleFunc("POST /fixture/lead", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID    string            `json:"id"`
			Patch yesmen.TopicPatch `json:"patch"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		cur, err := store.Topic(y.Name, y.HiredAt, req.ID)
		if err == nil {
			req.Patch.Revision = cur.Revision
			cur, err = store.PatchTopic(y.Name, y.HiredAt, yesmen.TopicByLead, req.ID, req.Patch)
		}
		if err == nil {
			emit("topics", y.Name)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"topic": cur, "error": fmt.Sprint(err)})
	})
	mux.HandleFunc("POST /fixture/gate", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("open") == "1" {
			_ = os.WriteFile(gate, []byte("open"), 0o644)
		} else {
			_ = os.Remove(gate)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /fixture/events", func(w http.ResponseWriter, r *http.Request) {
		var after int
		fmt.Sscan(r.URL.Query().Get("after"), &after)
		mu.Lock()
		list := []event{}
		if after < len(events) {
			list = append(list, events[after:]...)
		}
		n := len(events)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"events": list, "next": n})
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	httpSrv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = httpSrv.Serve(ln) }()
	defer httpSrv.Close()
	fmt.Printf("TOPICS_BROWSER_URL=http://%s/ YESMAN=%s\n", ln.Addr(), y.Name)
	in := bufio.NewReader(os.Stdin)
	for {
		if _, err := in.ReadString('\n'); err != nil {
			return
		}
	}
}
