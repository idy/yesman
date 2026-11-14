package schedule

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// The test binary doubles as a deterministic pi in RPC mode when
// YESMAN_FAKE_PI is set. It logs every prompt it receives, with its
// streaming behavior, to FAKE_PI_LOG, across restarts of the app. Its
// answer to a prompt follows FAKE_PI_MODE:
//
//	""        accepted: the run starts, then settles
//	"busy"    the lead is answering: queued as a follow-up, in pi's queue
//	"noack"   received, never answered (the acknowledgment is lost)
//	"reject"  refused by pi
//
// It never sees the app's submission IDs or origins: only pi's own protocol.
func TestMain(m *testing.M) {
	if os.Getenv("YESMAN_FAKE_PI") != "" {
		fakePi()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fakeCommand struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Message  string `json:"message"`
	Behavior string `json:"streamingBehavior"`
}

type promptLog struct {
	Message  string `json:"message"`
	Behavior string `json:"behavior"`
	Pid      int    `json:"pid"`
}

func fakePi() {
	out := bufio.NewWriter(os.Stdout)
	write := func(v any) {
		data, _ := json.Marshal(v)
		out.Write(append(data, '\n'))
		out.Flush()
	}
	respond := func(c fakeCommand, data any) {
		write(map[string]any{"type": "response", "id": c.ID, "command": c.Type, "success": true, "data": data})
	}
	mode := os.Getenv("FAKE_PI_MODE")
	var queue []string
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 4096), 8<<20)
	for sc.Scan() {
		var c fakeCommand
		if json.Unmarshal(sc.Bytes(), &c) != nil {
			continue
		}
		switch c.Type {
		case "get_state":
			respond(c, map[string]any{"model": map[string]any{"id": "m", "provider": "p"}, "thinkingLevel": "high", "sessionId": "fixture-session", "isStreaming": mode == "busy"})
		case "get_messages":
			respond(c, map[string]any{"messages": []any{}})
		case "prompt":
			if path := os.Getenv("FAKE_PI_LOG"); path != "" {
				if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
					data, _ := json.Marshal(promptLog{Message: c.Message, Behavior: c.Behavior, Pid: os.Getpid()})
					f.Write(append(data, '\n'))
					f.Close()
				}
			}
			switch mode {
			case "reject":
				write(map[string]any{"type": "response", "id": c.ID, "command": "prompt", "success": false, "error": "rejected by the fixture"})
			case "noack":
			case "busy":
				queue = append(queue, c.Message)
				write(map[string]any{"type": "queue_update", "steering": []string{}, "followUp": queue})
				respond(c, map[string]any{"disposition": "queued"})
			default:
				respond(c, map[string]any{"disposition": "started"})
				write(map[string]any{"type": "agent_start"})
				write(map[string]any{"type": "agent_settled", "aborted": false})
			}
		default:
			respond(c, nil)
		}
	}
}

// fakeClock is the scheduler's clock in tests: it moves only when told.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// env is an app in a temporary data directory: a store with one yesman, a
// real agent manager running the fake pi, and a scheduler on a fake clock.
// restart replaces the manager and scheduler as quitting and reopening
// the app would.
type env struct {
	t       *testing.T
	root    string
	store   *yesmen.Store
	agents  *agent.Manager
	svc     *Service
	clock   *fakeClock
	log     string
	mode    string
	cfg     Config
	changes chan string
}

const ws = "alice"

func newEnv(t *testing.T, mode string, start time.Time) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{t: t, root: root, log: filepath.Join(root, "prompts.jsonl"), mode: mode, clock: &fakeClock{t: start}}
	e.store = yesmen.New(filepath.Join(root, "yesmen"), filepath.Join(root, "archive"))
	if _, err := e.store.Hire(ws); err != nil {
		t.Fatal(err)
	}
	// As the app does: one lock per data directory makes this app the owner.
	e.cfg.LockPath = filepath.Join(root, ".runtime", "schedules.lock")
	e.open()
	t.Cleanup(func() { e.close() })
	return e
}

func (e *env) open() {
	exe, err := os.Executable()
	if err != nil {
		e.t.Fatal(err)
	}
	e.agents = agent.New(agent.Config{
		Pi:        exe,
		Extension: filepath.Join(e.root, "none.ts"),
		Env:       []string{"YESMAN_FAKE_PI=1", "FAKE_PI_LOG=" + e.log, "FAKE_PI_MODE=" + e.mode},
		Yesmen:    e.store,
	})
	cfg := e.cfg
	cfg.Yesmen, cfg.Agents, cfg.Now = e.store, e.agents, e.clock.Now
	e.svc = New(cfg)
}

// reopen replaces the scheduler with one configured by cfg, as the app
// would start it, keeping the store and agents.
func (e *env) reopen(cfg Config) {
	e.svc.Close()
	cfg.Yesmen, cfg.Agents, cfg.Now = e.store, e.agents, e.clock.Now
	if cfg.LockPath == "" {
		cfg.LockPath = e.cfg.LockPath
	}
	e.svc = New(cfg)
}

// close quits as the app does, scheduler first, then the agents; once they
// stopped, the deliveries they held end and the lock is let go, which
// closing again waits for. The lock must then be free: a test leaves no
// lock behind (Windows cannot remove the folder of a file still open).
func (e *env) close() {
	if e.svc != nil {
		e.svc.Close()
	}
	if e.agents != nil {
		e.agents.StopAll()
	}
	if e.svc != nil {
		e.svc.Close()
	}
	if e.cfg.LockPath != "" {
		f, err := lockFile(e.cfg.LockPath)
		if err != nil {
			e.t.Errorf("the scheduler's lock is still held after it closed and its deliveries ended: %v", err)
			return
		}
		if err := f.Close(); err != nil {
			e.t.Errorf("closing the lock: %v", err)
		}
	}
}

// restart quits the app and opens it again on the same data, with a new
// store as a new process would have.
func (e *env) restart(mode string) {
	e.close()
	e.mode = mode
	e.store = yesmen.New(filepath.Join(e.root, "yesmen"), filepath.Join(e.root, "archive"))
	e.open()
}

// tick checks once and waits for the deliveries it started.
func (e *env) tick() {
	e.svc.Tick(t0ctx())
	e.svc.Wait()
}

// prompts lists what the fake pi received of scheduled runs, across
// restarts; the lead's own reminders are not scheduled runs.
func (e *env) prompts() []promptLog {
	data, _ := os.ReadFile(e.log)
	var out []promptLog
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var p promptLog
		if json.Unmarshal([]byte(line), &p) == nil && (strings.HasPrefix(p.Message, "[yesman] Scheduled task") || strings.HasPrefix(p.Message, "[yesman] The user ran") || strings.HasPrefix(p.Message, "[yesman] The user is catching up")) {
			out = append(out, p)
		}
	}
	return out
}

func (e *env) waitPrompts(n int) []promptLog {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if p := e.prompts(); len(p) >= n {
			return p
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("pi received %d scheduled prompts, want %d", len(e.prompts()), n)
	return nil
}

func (e *env) save(in PlanInput) Plan {
	e.t.Helper()
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	if in.Title == "" {
		in.Title = "Check the dependencies"
	}
	if in.Instructions == "" {
		in.Instructions = "Check for outdated dependencies and report."
	}
	p, err := e.svc.Save(ws, in)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) view() View {
	e.t.Helper()
	v, err := e.svc.View(ws)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

func (e *env) plan(id string) PlanView {
	e.t.Helper()
	for _, p := range e.view().Plans {
		if p.ID == id {
			return p
		}
	}
	e.t.Fatalf("no plan %s", id)
	return PlanView{}
}

func (e *env) run(id string) RunView {
	e.t.Helper()
	for _, p := range e.view().Plans {
		for _, r := range p.Runs {
			if r.ID == id {
				return r
			}
		}
	}
	e.t.Fatalf("no run %s", id)
	return RunView{}
}

func (e *env) board() yesmen.Board {
	e.t.Helper()
	b, err := e.store.Board(ws)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

// scheduledTasks lists the board tasks of a run.
func (e *env) scheduledTasks(run string) []yesmen.Task {
	var out []yesmen.Task
	for _, t := range e.board().Tasks {
		if t.Schedule != nil && t.Schedule.Run == run {
			out = append(out, t)
		}
	}
	return out
}

func (e *env) runFiles() []string {
	entries, _ := os.ReadDir(filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules", "runs"))
	var out []string
	for _, x := range entries {
		out = append(out, x.Name())
	}
	return out
}
