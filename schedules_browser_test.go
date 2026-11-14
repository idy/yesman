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
	"github.com/idy/yesman/internal/schedule"
	"github.com/idy/yesman/internal/yesmen"
)

// The main test binary doubles as a deterministic pi when YESMAN_FAKE_PI is
// set, for the Tasks browser fixture: it never calls a model. A prompt is
// logged, then accepted, or queued while FAKE_PI_MODE_FILE holds "busy",
// or refused while it holds "reject".
func TestMain(m *testing.M) {
	if os.Getenv("YESMAN_FAKE_PI") != "" {
		browserFakePi()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func browserFakePi() {
	out := bufio.NewWriter(os.Stdout)
	write := func(v any) {
		data, _ := json.Marshal(v)
		out.Write(append(data, '\n'))
		out.Flush()
	}
	var queue []string
	var messages []any // the session's user messages, as pi keeps them
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 4096), 8<<20)
	for sc.Scan() {
		var c struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Message  string `json:"message"`
			Behavior string `json:"streamingBehavior"`
		}
		if json.Unmarshal(sc.Bytes(), &c) != nil {
			continue
		}
		respond := func(data any) {
			write(map[string]any{"type": "response", "id": c.ID, "command": c.Type, "success": true, "data": data})
		}
		mode, _ := os.ReadFile(os.Getenv("FAKE_PI_MODE_FILE"))
		switch c.Type {
		case "get_state":
			respond(map[string]any{"model": map[string]any{"id": "fixture", "provider": "local"}, "thinkingLevel": "off", "sessionId": "fixture-session"})
		case "get_messages":
			respond(map[string]any{"messages": messages})
		case "prompt":
			if f, err := os.OpenFile(os.Getenv("FAKE_PI_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				data, _ := json.Marshal(map[string]string{"message": c.Message, "behavior": c.Behavior})
				f.Write(append(data, '\n'))
				f.Close()
			}
			switch strings.TrimSpace(string(mode)) {
			case "reject":
				write(map[string]any{"type": "response", "id": c.ID, "command": "prompt", "success": false, "error": "refused by the fixture"})
			case "busy":
				queue = append(queue, c.Message)
				write(map[string]any{"type": "queue_update", "steering": []string{}, "followUp": queue})
				respond(map[string]any{"disposition": "queued"})
			default:
				// The run starts with the prompt as its user message, as pi's.
				respond(map[string]any{"disposition": "started"})
				write(map[string]any{"type": "agent_start"})
				msg := map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": c.Message}}, "timestamp": time.Now().UnixMilli()}
				write(map[string]any{"type": "message_start", "message": msg})
				write(map[string]any{"type": "message_end", "message": msg})
				messages = append(messages, msg)
				write(map[string]any{"type": "agent_settled", "aborted": false})
			}
		default:
			respond(nil)
		}
	}
}

type browserClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *browserClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

// TestSchedulesBrowserBridge serves the production page with the actual Go
// service, scheduler and agent manager (running the fake pi) behind a
// controlled bridge, in a temporary data directory, for
// scripts/schedules-browser.mjs. The scheduler's clock moves only when the
// script says; nothing touches the user's data, settings or system clock.
func TestSchedulesBrowserBridge(t *testing.T) {
	if os.Getenv("YESMAN_TEST_SCHEDULES_BROWSER") != "1" {
		t.Skip("run through scripts/schedules-browser.mjs")
	}
	a, _ := crewApp(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(a.home.Root, "prompts.jsonl")
	modePath := filepath.Join(a.home.Root, "pi-mode")
	a.pool = pool.New(a.home.Repos())
	events := newBrowserEvents()
	a.agents = agent.New(agent.Config{
		Pi: exe, Extension: filepath.Join(a.home.Root, "none.ts"), Yesmen: a.yesmen,
		Env: []string{"YESMAN_FAKE_PI=1", "FAKE_PI_LOG=" + logPath, "FAKE_PI_MODE_FILE=" + modePath},
		OnEvent: func(e agent.Event) {
			a.schedules.OnAgentEvent(e)
			events.send("agent-event", e) // the window follows conversations live
		},
		OnChange: func(list []agent.Info) { events.send("agents", list) },
	})
	t.Cleanup(a.agents.StopAll)
	clock := &browserClock{at: time.Now().UTC().Truncate(time.Second)}
	a.schedules = schedule.New(schedule.Config{
		Yesmen: a.yesmen, Agents: a.agents, Now: clock.Now, LockPath: filepath.Join(a.home.Runtime(), "schedules.lock"),
		OnChange:     func(y string) { events.send("schedules", y) },
		BoardChanged: func(y string) { events.send("board", y) },
	})
	t.Cleanup(a.schedules.Close)
	s := &service{app: a}
	for _, label := range []string{"Alice", "鲍勃 Bob"} {
		if _, err := a.yesmen.HireNamed(label, ""); err != nil {
			t.Fatal(err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /fixture/call", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Args   []json.RawMessage `json:"args"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		var value any
		if err == nil {
			value, err = browserCall(r.Context(), s, req.Method, req.Args)
		}
		reply(w, value, err)
	})
	mux.HandleFunc("POST /fixture/clock", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Add string `json:"add"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		var d time.Duration
		if err == nil {
			d, err = time.ParseDuration(req.Add)
		}
		clock.mu.Lock()
		clock.at = clock.at.Add(d)
		now := clock.at
		clock.mu.Unlock()
		reply(w, now, err)
	})
	mux.HandleFunc("POST /fixture/tick", func(w http.ResponseWriter, r *http.Request) {
		a.schedules.Tick(r.Context())
		a.schedules.Wait()
		reply(w, clock.Now(), nil)
	})
	mux.HandleFunc("POST /fixture/mode", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Mode string `json:"mode"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		if err == nil {
			err = os.WriteFile(modePath, []byte(req.Mode), 0o644)
		}
		reply(w, req.Mode, err)
	})
	mux.HandleFunc("POST /fixture/board", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Yesman, ID, Status, Note string
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		var b yesmen.Board
		if err == nil {
			b, err = a.yesmen.ChangeBoard(req.Yesman, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: req.ID, Status: &req.Status, Note: &req.Note}}})
			events.send("board", req.Yesman)
		}
		reply(w, b, err)
	})
	mux.HandleFunc("POST /fixture/corrupt", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Yesman string }
		err := json.NewDecoder(r.Body).Decode(&req)
		var y yesmen.Yesman
		if err == nil {
			y, err = a.yesmen.Get(req.Yesman)
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(y.MetaDir(), "schedules", "plans.json"), []byte(`{"version":1,"plans":[`), 0o644)
		}
		reply(w, nil, err)
	})
	mux.HandleFunc("GET /fixture/prompts", func(w http.ResponseWriter, r *http.Request) {
		data, _ := os.ReadFile(logPath)
		var out []map[string]string
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var p map[string]string
			if json.Unmarshal([]byte(line), &p) == nil && strings.HasPrefix(p["message"], "[yesman] ") && !strings.HasPrefix(p["message"], "[yesman] No worker") {
				out = append(out, p)
			}
		}
		reply(w, out, nil)
	})
	mux.HandleFunc("GET /fixture/events", events.serve)
	web, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", http.FileServerFS(web))
	server := httptest.NewServer(mux)
	defer server.Close()
	fmt.Println("SCHEDULES_BROWSER_URL=" + server.URL)
	_, _ = io.Copy(io.Discard, os.Stdin)
	events.close()
}

func reply(w http.ResponseWriter, value any, err error) {
	response := map[string]any{"value": value}
	if err != nil {
		response["error"] = err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// browserCall forwards the bridge's calls the Tasks pages need to the
// actual service.
func browserCall(ctx context.Context, s *service, method string, args []json.RawMessage) (any, error) {
	arg := func(i int, v any) error {
		if i >= len(args) {
			return fmt.Errorf("%s: argument %d is missing", method, i)
		}
		return json.Unmarshal(args[i], v)
	}
	var name, id, other, from string
	var revision int
	var on bool
	switch method {
	case "State":
		return s.State(ctx)
	case "Board", "Schedules", "ScheduleSummary", "Info":
		if err := arg(0, &name); err != nil {
			return nil, err
		}
		switch method {
		case "Board":
			return s.Board(name)
		case "Schedules":
			return s.Schedules(name)
		case "ScheduleSummary":
			return s.ScheduleSummary(name)
		default:
			return s.Info(ctx, name)
		}
	case "Messages":
		if err := arg(0, &id); err != nil {
			return nil, err
		}
		return s.Messages(ctx, id)
	case "SaveSchedule":
		var in schedule.PlanInput
		if err := arg(0, &name); err != nil {
			return nil, err
		}
		if err := arg(1, &in); err != nil {
			return nil, err
		}
		return s.SaveSchedule(name, in)
	case "SetScheduleEnabled":
		if err := firstErr(arg(0, &name), arg(1, &id), arg(2, &on), arg(3, &revision)); err != nil {
			return nil, err
		}
		return s.SetScheduleEnabled(name, id, on, revision)
	case "ArchiveSchedule":
		if err := firstErr(arg(0, &name), arg(1, &id), arg(2, &revision)); err != nil {
			return nil, err
		}
		return nil, s.ArchiveSchedule(name, id, revision)
	case "RunSchedule":
		if err := firstErr(arg(0, &name), arg(1, &id), arg(2, &other), arg(3, &from)); err != nil {
			return nil, err
		}
		return s.RunSchedule(name, id, other, from)
	case "RetryScheduleRun", "CheckScheduleRun":
		if err := firstErr(arg(0, &name), arg(1, &id)); err != nil {
			return nil, err
		}
		if method == "RetryScheduleRun" {
			return s.RetryScheduleRun(name, id)
		}
		return s.CheckScheduleRun(name, id)
	}
	return nil, fmt.Errorf("unsupported fixture method %s", method)
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// browserEvents streams the app's events to the bridge, as MyGo would.
type browserEvents struct {
	mu      sync.Mutex
	clients map[chan []byte]bool
	done    chan struct{}
}

func newBrowserEvents() *browserEvents {
	return &browserEvents{clients: map[chan []byte]bool{}, done: make(chan struct{})}
}

func (e *browserEvents) send(name string, value any) {
	data, _ := json.Marshal(map[string]any{"name": name, "value": value})
	e.mu.Lock()
	defer e.mu.Unlock()
	for c := range e.clients {
		select {
		case c <- data:
		default:
		}
	}
}

func (e *browserEvents) close() { close(e.done) }

func (e *browserEvents) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	c := make(chan []byte, 64)
	e.mu.Lock()
	e.clients[c] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.clients, c)
		e.mu.Unlock()
	}()
	flusher, _ := w.(http.Flusher)
	fmt.Fprint(w, ": ready\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	for {
		select {
		case data := <-c:
			fmt.Fprintf(w, "data: %s\n\n", data)
			if flusher != nil {
				flusher.Flush()
			}
		case <-r.Context().Done():
			return
		case <-e.done:
			return
		}
	}
}
