// Package agent runs the pi agents of yesmen and follows their state.
//
// Every agent is a pi process in RPC mode, a child of the yesman app, that
// works in its yesman's workspace and keeps its session log under
// .yesman/sessions/<name>. An agent's ID is "<yesman>/<name>"; the lead is
// named "lead". Agents start when someone talks to them and resume their
// latest session, so restarting the app loses at most a turn in progress.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// Status is where an agent is.
type Status string

const (
	// Stopped: no pi process runs; one starts when someone talks to it.
	Stopped Status = "stopped"
	// Starting: its pi process is starting.
	Starting Status = "starting"
	// Idle: running and waiting for a message.
	Idle Status = "idle"
	// Working: running a turn.
	Working Status = "working"
	// Waiting: an extension asked the user something and waits for the answer.
	Waiting Status = "waiting"
	// Failed: its pi process exited unexpectedly or failed to start.
	Failed Status = "failed"
)

// Role tells a yesman's lead from its workers.
const (
	RoleLead   = "lead"
	RoleWorker = "worker"
)

// Info describes an agent for the UI and for other agents.
type Info struct {
	ID     string `json:"id"`
	Yesman string `json:"yesman"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Task   string `json:"task,omitempty"`
	Status Status `json:"status"`
	// Detail explains a failure.
	Detail string `json:"detail,omitempty"`
	// Dialog is the extension_ui_request a Waiting agent waits on.
	Dialog    json.RawMessage `json:"dialog,omitempty"`
	Model     string          `json:"model,omitempty"`
	UpdatedAt time.Time       `json:"updatedAt,omitzero"`
}

// Event is one record an agent's pi process emitted.
type Event struct {
	Agent string          `json:"agent"`
	Data  json.RawMessage `json:"data"`
}

// Config configures a Manager.
type Config struct {
	// Pi is the pi executable.
	Pi string
	// Extension is the path of yesman's pi extension, loaded with -e.
	Extension string
	// Env is added to every agent's environment, after the app's own.
	Env []string
	// Yesmen knows the workspaces.
	Yesmen *yesmen.Store
	// OnEvent receives every record of every agent, in order per agent.
	OnEvent func(Event)
	// OnChange receives the agents whenever one's status changes.
	OnChange func([]Info)
}

// Manager runs agents.
type Manager struct {
	cfg Config

	mu     sync.Mutex
	agents map[string]*agent
}

type agent struct {
	info     Info
	proc     *proc
	starting chan struct{} // closed when a start finishes, nil otherwise
	stopping bool
}

// New returns a Manager.
func New(cfg Config) *Manager {
	if cfg.OnEvent == nil {
		cfg.OnEvent = func(Event) {}
	}
	if cfg.OnChange == nil {
		cfg.OnChange = func([]Info) {}
	}
	return &Manager{cfg: cfg, agents: map[string]*agent{}}
}

// ID joins a yesman and an agent name.
func ID(yesman, name string) string { return yesman + "/" + name }

// SplitID splits an agent ID.
func SplitID(id string) (yesman, name string, err error) {
	yesman, name, ok := strings.Cut(id, "/")
	if !ok || yesmen.ValidName(yesman) != nil || yesmen.ValidName(name) != nil {
		return "", "", fmt.Errorf("%q is not an agent ID", id)
	}
	return yesman, name, nil
}

// List returns every agent of every yesman: each lead, then its workers in
// the order they started.
func (m *Manager) List() []Info {
	ys, _ := m.cfg.Yesmen.List()
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Info
	for _, y := range ys {
		out = append(out, m.infoLocked(ID(y.Name, yesmen.LeadName), RoleLead, ""))
		ws, _ := m.cfg.Yesmen.Workers(y.Name)
		sort.SliceStable(ws, func(i, j int) bool { return ws[i].StartedAt.Before(ws[j].StartedAt) })
		for _, w := range ws {
			out = append(out, m.infoLocked(ID(y.Name, w.Name), RoleWorker, w.Task))
		}
	}
	return out
}

// Get returns one agent.
func (m *Manager) Get(id string) (Info, error) {
	for _, i := range m.List() {
		if i.ID == id {
			return i, nil
		}
	}
	return Info{}, fmt.Errorf("no agent %s", id)
}

func (m *Manager) infoLocked(id, role, task string) Info {
	if a := m.agents[id]; a != nil {
		i := a.info
		i.Role, i.Task = role, task
		return i
	}
	y, n, _ := SplitID(id)
	return Info{ID: id, Yesman: y, Name: n, Role: role, Task: task, Status: Stopped}
}

func (m *Manager) changed() { m.cfg.OnChange(m.List()) }

// setStatus records a status, unless the process it comes from was replaced.
func (m *Manager) setStatus(a *agent, p *proc, s Status, detail string) {
	m.mu.Lock()
	if p != nil && a.proc != p {
		m.mu.Unlock()
		return
	}
	a.info.Status, a.info.Detail, a.info.UpdatedAt = s, detail, time.Now()
	if s != Waiting {
		a.info.Dialog = nil
	}
	m.mu.Unlock()
	m.changed()
}

// ensure starts the agent unless it runs.
func (m *Manager) ensure(id string) (*agent, *proc, error) {
	ys, name, err := SplitID(id)
	if err != nil {
		return nil, nil, err
	}
	y, err := m.cfg.Yesmen.Get(ys)
	if err != nil {
		return nil, nil, err
	}
	role := RoleLead
	if name != yesmen.LeadName {
		role = RoleWorker
		ws, _ := m.cfg.Yesmen.Workers(ys)
		found := false
		for _, w := range ws {
			found = found || w.Name == name
		}
		if !found {
			return nil, nil, fmt.Errorf("%s has no worker named %s", ys, name)
		}
	}

	for {
		m.mu.Lock()
		a := m.agents[id]
		if a == nil {
			a = &agent{info: Info{ID: id, Yesman: ys, Name: name, Role: role, Status: Stopped}}
			m.agents[id] = a
		}
		if a.proc != nil {
			p := a.proc
			m.mu.Unlock()
			return a, p, nil
		}
		if a.starting != nil {
			wait := a.starting
			m.mu.Unlock()
			<-wait
			continue
		}
		a.starting = make(chan struct{})
		a.stopping = false
		a.info.Status, a.info.Detail, a.info.UpdatedAt = Starting, "", time.Now()
		m.mu.Unlock()
		m.changed()

		p, model, err := m.spawn(a, y, name, role)

		m.mu.Lock()
		close(a.starting)
		a.starting = nil
		if err != nil {
			a.info.Status, a.info.Detail, a.info.UpdatedAt = Failed, err.Error(), time.Now()
			m.mu.Unlock()
			m.changed()
			return nil, nil, err
		}
		a.proc = p
		a.info.Status, a.info.Model, a.info.UpdatedAt = Idle, model, time.Now()
		m.mu.Unlock()
		m.changed()
		go m.watch(a, p)
		return a, p, nil
	}
}

func (m *Manager) spawn(a *agent, y yesmen.Yesman, name, role string) (*proc, string, error) {
	sessions := y.SessionDir(name)
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		return nil, "", err
	}
	args := []string{"--mode", "rpc", "--session-dir", sessions, "-e", m.cfg.Extension}
	if hasSession(sessions) {
		args = append(args, "--continue")
	} else {
		args = append(args, "--name", ID(y.Name, name))
	}
	env := append(os.Environ(), m.cfg.Env...)
	env = append(env,
		"YESMAN_AGENT="+ID(y.Name, name),
		"YESMAN_AGENT_NAME="+name,
		"YESMAN_YESMAN="+y.Name,
		"YESMAN_ROLE="+role,
		"YESMAN_WORKSPACE="+y.Path,
	)
	p, err := startProc(spec{Path: m.cfg.Pi, Args: args, Dir: y.Path, Env: env}, func(p *proc, kind string, raw json.RawMessage) {
		m.record(a, p, kind, raw)
	})
	if err != nil {
		return nil, "", err
	}
	// get_state answers once pi has loaded its session and extensions.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r, err := p.call(ctx, map[string]any{"type": "get_state"})
	if err != nil {
		p.stop(2 * time.Second)
		if msg := p.stderr.String(); msg != "" {
			return nil, "", fmt.Errorf("pi did not start: %v: %s", err, lastLines(msg, 6))
		}
		return nil, "", fmt.Errorf("pi did not start: %w", err)
	}
	var state struct {
		Model struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"model"`
		IsStreaming bool `json:"isStreaming"`
	}
	_ = json.Unmarshal(r.Data, &state)
	model := state.Model.ID
	return p, model, nil
}

// record handles one record of an agent's process. It runs on the
// process's reader, so it must not wait for that process's answers.
func (m *Manager) record(a *agent, p *proc, kind string, raw json.RawMessage) {
	switch kind {
	case "agent_start":
		m.setStatus(a, p, Working, "")
	case "agent_settled":
		var e struct {
			Aborted bool `json:"aborted"`
		}
		_ = json.Unmarshal(raw, &e)
		m.setStatus(a, p, Idle, "")
		if a.info.Role == RoleWorker && !e.Aborted {
			go m.reportToLead(a, p)
		}
	case "extension_ui_request":
		var e struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(raw, &e)
		switch e.Method {
		case "select", "confirm", "input", "editor":
			m.mu.Lock()
			if a.proc == p || a.proc == nil {
				a.info.Status, a.info.Dialog, a.info.UpdatedAt = Waiting, raw, time.Now()
			}
			m.mu.Unlock()
			m.changed()
		}
	}
	m.cfg.OnEvent(Event{Agent: a.info.ID, Data: raw})
}

// watch follows a process until it exits.
func (m *Manager) watch(a *agent, p *proc) {
	<-p.done
	m.mu.Lock()
	if a.proc != p {
		m.mu.Unlock()
		return
	}
	a.proc = nil
	if a.stopping {
		a.info.Status, a.info.Detail = Stopped, ""
	} else {
		detail := "pi exited"
		if msg := p.stderr.String(); msg != "" {
			detail += ": " + lastLines(msg, 6)
		}
		a.info.Status, a.info.Detail = Failed, detail
	}
	a.info.Dialog, a.info.UpdatedAt = nil, time.Now()
	m.mu.Unlock()
	m.changed()
}

// Prompt sends a user message. While the agent works, it steers the run:
// pi delivers it after the current tool calls.
func (m *Manager) Prompt(ctx context.Context, id, text string) error {
	return m.prompt(ctx, id, text, "steer")
}

func (m *Manager) prompt(ctx context.Context, id, text, behavior string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("empty message")
	}
	_, p, err := m.ensure(id)
	if err != nil {
		return err
	}
	_, err = p.call(ctx, map[string]any{"type": "prompt", "message": text, "streamingBehavior": behavior})
	return err
}

// Abort stops the agent's current run.
func (m *Manager) Abort(ctx context.Context, id string) error {
	p := m.running(id)
	if p == nil {
		return nil
	}
	_, err := p.call(ctx, map[string]any{"type": "abort"})
	return err
}

// Messages returns the agent's conversation, starting it if needed, as
// pi's get_messages data: {"messages": [...]}.
func (m *Manager) Messages(ctx context.Context, id string) (json.RawMessage, error) {
	_, p, err := m.ensure(id)
	if err != nil {
		return nil, err
	}
	r, err := p.call(ctx, map[string]any{"type": "get_messages"})
	if err != nil {
		return nil, err
	}
	return r.Data, nil
}

// Answer answers the dialog an agent waits on. answer holds the response's
// fields: value, confirmed or cancelled.
func (m *Manager) Answer(id, requestID string, answer map[string]any) error {
	m.mu.Lock()
	a := m.agents[id]
	var p *proc
	if a != nil {
		p = a.proc
	}
	m.mu.Unlock()
	if p == nil {
		return fmt.Errorf("%s is not running", id)
	}
	msg := map[string]any{}
	for k, v := range answer {
		msg[k] = v
	}
	msg["type"], msg["id"] = "extension_ui_response", requestID
	if err := p.send(msg); err != nil {
		return err
	}
	m.setStatus(a, p, Working, "")
	return nil
}

// Stop ends the agent's process. Its session stays, to resume later.
func (m *Manager) Stop(id string) {
	m.mu.Lock()
	a := m.agents[id]
	var p *proc
	if a != nil {
		p = a.proc
		a.stopping = true
	}
	m.mu.Unlock()
	if p != nil {
		p.stop(10 * time.Second)
	}
}

// StopYesman stops every agent of a yesman.
func (m *Manager) StopYesman(name string) {
	var wg sync.WaitGroup
	for _, i := range m.List() {
		if i.Yesman == name {
			wg.Go(func() { m.Stop(i.ID) })
		}
	}
	wg.Wait()
}

// StopAll stops every agent, for quitting.
func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.agents))
	for id := range m.agents {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() { m.Stop(id) })
	}
	wg.Wait()
}

func (m *Manager) running(id string) *proc {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a := m.agents[id]; a != nil {
		return a.proc
	}
	return nil
}

// StartWorker records a worker of a yesman and gives it its task.
func (m *Manager) StartWorker(ctx context.Context, yesman, name, task string) (Info, error) {
	if strings.TrimSpace(task) == "" {
		return Info{}, errors.New("a worker needs a task")
	}
	if err := m.cfg.Yesmen.AddWorker(yesman, yesmen.Worker{Name: name, Task: task, StartedAt: time.Now().UTC()}); err != nil {
		return Info{}, err
	}
	id := ID(yesman, name)
	if err := m.prompt(ctx, id, task, "followUp"); err != nil {
		return Info{}, err
	}
	return m.Get(id)
}

// RemoveWorker stops a worker and takes it off its yesman's list. Its
// session log stays in the workspace.
func (m *Manager) RemoveWorker(yesman, name string) error {
	if name == yesmen.LeadName {
		return errors.New("the lead cannot be removed")
	}
	id := ID(yesman, name)
	m.Stop(id)
	m.mu.Lock()
	delete(m.agents, id)
	m.mu.Unlock()
	err := m.cfg.Yesmen.RemoveWorker(yesman, name)
	m.changed()
	return err
}

// reportToLead hands a worker's final message to its lead, which starts a
// run or reads it after the current one.
func (m *Manager) reportToLead(a *agent, p *proc) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r, err := p.call(ctx, map[string]any{"type": "get_last_assistant_text"})
	if err != nil {
		return
	}
	var data struct {
		Text *string `json:"text"`
	}
	_ = json.Unmarshal(r.Data, &data)
	report := "(no final message)"
	if data.Text != nil && strings.TrimSpace(*data.Text) != "" {
		report = *data.Text
	}
	msg := fmt.Sprintf("[yesman] Worker %q finished a run. Its final message:\n\n%s", a.info.Name, report)
	_ = m.prompt(ctx, ID(a.info.Yesman, yesmen.LeadName), msg, "followUp")
}

func hasSession(dir string) bool {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	return len(matches) > 0
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
