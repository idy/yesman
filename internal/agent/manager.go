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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	// Number tells a yesman's workers apart for the user, as a1, a2, …
	Number int `json:"number,omitempty"`
	// Title is the subject of a worker's work, for the user.
	Title string `json:"title,omitempty"`
	Task  string `json:"task,omitempty"`
	// FinishedAt is when a worker's report reached its lead and its process
	// closed; zero while it has work.
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	Status     Status    `json:"status"`
	// Since is when the yesman was hired, for a lead, or the worker
	// started. With ID it identifies an agent across reuses of a name.
	Since time.Time `json:"since,omitzero"`
	// StartedAt is when its running pi process started, which loaded pi's
	// packages as they were then.
	StartedAt time.Time `json:"startedAt,omitzero"`
	// Detail explains a failure.
	Detail string `json:"detail,omitempty"`
	// Dialog is the extension_ui_request a Waiting agent waits on, and
	// DialogAt when it was asked.
	Dialog   json.RawMessage `json:"dialog,omitempty"`
	DialogAt time.Time       `json:"dialogAt,omitzero"`
	// HandedOver: the lead handed this worker's Dialog to the user, who
	// sees it under Needs you. A worker's other questions stay with its lead.
	HandedOver bool `json:"handedOver,omitempty"`
	// Activity says what a working agent is doing now, from the events of
	// its run, such as "Running go test ./...". Empty between steps.
	Activity string `json:"activity,omitempty"`
	// RunStartedAt is when the current run started; zero between runs.
	RunStartedAt time.Time `json:"runStartedAt,omitzero"`
	// RunError is set while the last run this agent finished ended on a
	// model error, such as a provider's usage limit: pi recorded its last
	// assistant message with stopReason "error". A later run that ends
	// otherwise clears it, as does stopping a run, even one waiting to
	// retry the error; starting a run does not. It belongs to this
	// incarnation and is not kept on disk.
	RunError     *RunError `json:"runError,omitempty"`
	Model        string    `json:"model,omitempty"`
	Provider     string    `json:"provider,omitempty"`
	Effort       string    `json:"effort,omitempty"`
	EffortLevels []string  `json:"effortLevels,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt,omitzero"`
}

// RunError is how an agent's last finished run failed, from pi's own record
// of its last assistant message.
type RunError struct {
	// Message is pi's errorMessage, trimmed; empty when pi recorded none.
	Message  string    `json:"message"`
	Provider string    `json:"provider,omitempty"`
	Model    string    `json:"model,omitempty"`
	At       time.Time `json:"at"`
}

// Event is one record an agent's pi process emitted.
type Event struct {
	Agent      string    `json:"agent"`
	AgentSince time.Time `json:"agentSince,omitzero"`
	// Seq numbers an agent's events in order, across its restarts.
	Seq  uint64          `json:"seq"`
	Data json.RawMessage `json:"data"`
}

// Conversation is an agent's history, as pi's get_messages returns it,
// with the events of the reply it is writing, which the history does not
// hold yet. Seq is the last event it reflects: a client that queued
// events while loading it replays only later ones.
type Conversation struct {
	Messages   json.RawMessage `json:"messages"`
	Live       []Event         `json:"live"`
	Seq        uint64          `json:"seq"`
	Deliveries []Delivery      `json:"deliveries"`
	Queue      QueueStatus     `json:"queue"`
}

// Config configures a Manager.
type Config struct {
	// Pi is the pi executable.
	Pi string
	// Command resolves a managed runtime outside Manager locks. Nil preserves
	// the executable supplied by callers and tests.
	Command func(context.Context) (string, []string, error)
	// Extension is the path of yesman's pi extension, loaded with -e.
	Extension string
	// Env is added to every agent's environment, after the app's own.
	// Each agent also gets YESMAN_TOKEN, a credential of its own for the
	// app's API, which Authenticate maps back to it.
	Env []string
	// Yesmen knows the workspaces.
	Yesmen *yesmen.Store
	// OnEvent receives every record of every agent, in order per agent.
	OnEvent func(Event)
	// OnChange receives the agents whenever one's status changes. It runs
	// on a goroutine of its own, never inside a Manager call, so it may
	// wait for the UI thread while that thread waits for Stop; bursts of
	// changes are coalesced into one call with the latest agents.
	OnChange func([]Info)
	// deliveryWrite is a controllable atomic writer for persistence tests.
	deliveryWrite func(string, []byte) error
	deliveryRead  func(string, time.Time) (map[string]Delivery, int64, error)
}

// Manager runs agents.
type Manager struct {
	cfg Config

	mu               sync.Mutex
	agents           map[string]*agent
	tokens           map[string]string        // credential -> agent ID, while its process runs
	closed           bool                     // StopAll permanently prevents further starts; guarded by mu
	worktreeCleanup  map[string]bool          // reservations preventing owner startup/input during final removal
	shutdown         chan struct{}            // closed by StopAll to wake pending report retries
	recallBytes      int                      // full payload memory across all agent generations; guarded by mu
	continuations    map[string]*continuation // by yesman; guarded by mu, see continuation.go
	beforeNotice     func()                   // tests only: runs before a notice reserves the lead
	leadPauses       map[string]*leadPause    // by yesman: the user's pauses of leads; guarded by mu, see pause.go
	beforePromptDone func(*agent)             // tests only: runs once a prompt's RPC returned, before it ends

	notify chan struct{} // wakes the goroutine that calls OnChange
	// callsDone is signalled, under mu, when an agent's last caller
	// operation ends; Stop waits for them.
	callsDone *sync.Cond
}

type agent struct {
	modelMu sync.Mutex // orders prompt registration and model configuration
	// prompts counts outstanding prompt RPCs. Writers hold modelMu and the
	// manager's mu; readers may hold either.
	prompts int
	// reports counts worker handoffs being read or delivered to the lead,
	// registered before the worker becomes idle; guarded by the manager's mu.
	reports                        int
	info                           Info
	proc                           *proc
	token                          string
	tokenProc                      *proc         // the process token was given to
	seq                            uint64        // the last event's number
	turn                           []Event       // the events of the assistant message in progress
	starting                       chan struct{} // closed when a start finishes, nil otherwise
	startCancel                    context.CancelFunc
	stopping                       bool
	dir                            string   // the workspace, to show paths in it relative to it
	tools                          []string // the tool calls running, oldest first
	handedOver                     string   // the dialog ID the lead handed to the user
	calls                          int      // CallerOperations in progress; guarded by mu
	labels                         map[string]string
	deliveries                     map[string]Delivery // bounded receipt metadata; pi owns scheduling
	deliveryOnce                   sync.Once
	deliveryErr                    error
	deliveryWriter                 *deliveryWriter
	deliveryFloor                  int64
	deliveryVersion, deliveryOrder uint64
	workspaceSince                 time.Time
	retiring, deliverySealed       bool
	removing                       bool
	queue                          QueueStatus
	nativeQueue                    pendingQueue
	recallPayloads                 map[string]recallPayload
	recallBytes, recalls           int
	deliverySettled                uint64 // sequence of last observed settled boundary
	// runEnd is how the run in progress ended so far: its latest assistant
	// message's error, or nil; runSaid tells whether it had one at all.
	runEnd  *RunError
	runSaid bool
	// closeAt is when a finished worker's report reached its lead, while the
	// prompt that began that run had not ended yet; closeInbox its inbox
	// then. That prompt's end closes the worker if nothing came meanwhile.
	// Guarded by the manager's mu.
	closeAt    time.Time
	closeInbox uint64
	// inbox changes when a message is queued or registered. settledInbox is
	// the inbox at the last settle, so accepted prompts awaiting agent_start
	// still defer telling the lead about its tasks. Both are guarded by the manager's mu.
	inbox, settledInbox uint64
}

// errStopped is returned when Stop interrupts a start or the manager has quit.
var errStopped = errors.New("the agent was stopped")

// New returns a Manager.
func New(cfg Config) *Manager {
	if cfg.OnEvent == nil {
		cfg.OnEvent = func(Event) {}
	}
	if cfg.OnChange == nil {
		cfg.OnChange = func([]Info) {}
	}
	m := &Manager{cfg: cfg, agents: map[string]*agent{}, tokens: map[string]string{}, shutdown: make(chan struct{}), notify: make(chan struct{}, 1)}
	m.callsDone = sync.NewCond(&m.mu)
	go func() {
		for range m.notify {
			m.cfg.OnChange(m.List())
		}
	}()
	return m
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
		out = append(out, m.infoLocked(ID(y.Name, yesmen.LeadName), RoleLead, yesmen.Worker{StartedAt: y.HiredAt}, ""))
		ws, _ := m.cfg.Yesmen.Workers(y.Name)
		sort.SliceStable(ws, func(i, j int) bool { return ws[i].StartedAt.Before(ws[j].StartedAt) })
		board := m.boardFor(y.Name, ws)
		for _, w := range ws {
			out = append(out, m.infoLocked(ID(y.Name, w.Name), RoleWorker, w, w.Subject(board)))
		}
	}
	return out
}

// boardFor reads a yesman's board when a worker needs it for its subject:
// one the lead started without a title.
func (m *Manager) boardFor(yesman string, ws []yesmen.Worker) yesmen.Board {
	if slices.ContainsFunc(ws, func(w yesmen.Worker) bool { return w.Title == "" }) {
		b, _ := m.cfg.Yesmen.Board(yesman)
		return b
	}
	return yesmen.Board{}
}

// Get returns one agent.
func (m *Manager) Get(id string) (Info, error) {
	yesman, name, err := SplitID(id)
	if err != nil {
		return Info{}, err
	}
	y, err := m.cfg.Yesmen.Get(yesman)
	if err != nil {
		return Info{}, err
	}
	role, w, subject := RoleLead, yesmen.Worker{StartedAt: y.HiredAt}, ""
	if name != yesmen.LeadName {
		workers, err := m.cfg.Yesmen.Workers(yesman)
		if err != nil {
			return Info{}, err
		}
		i := slices.IndexFunc(workers, func(w yesmen.Worker) bool { return w.Name == name })
		if i < 0 {
			return Info{}, fmt.Errorf("no agent %s", id)
		}
		role, w = RoleWorker, workers[i]
		subject = w.Subject(m.boardFor(yesman, workers[i:i+1]))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.infoLocked(id, role, w, subject), nil
}

// infoLocked describes an agent from what runs and, for a worker, its
// record; a lead's record holds only when it was hired.
func (m *Manager) infoLocked(id, role string, w yesmen.Worker, subject string) Info {
	i := Info{ID: id, Status: Stopped}
	if a := m.agents[id]; a != nil && (a.info.Since.Equal(w.StartedAt) || a.info.Since.IsZero() && !a.retiring) {
		i = a.info
		i.HandedOver = a.handedOver != "" && dialogID(i.Dialog) == a.handedOver
	}
	if i.Dialog == nil {
		i.DialogAt = time.Time{}
	}
	i.Yesman, i.Name, _ = SplitID(id)
	i.Role, i.Number, i.Title, i.Task, i.Since, i.FinishedAt = role, w.Number, subject, w.Task, w.StartedAt, w.FinishedAt
	return i
}

// Authenticate returns the running agent a YESMAN_TOKEN belongs to.
func (m *Manager) Authenticate(token string) (Info, error) {
	m.mu.Lock()
	id, ok := m.tokens[token]
	m.mu.Unlock()
	if !ok || token == "" {
		return Info{}, errors.New("unknown agent credential")
	}
	return m.Get(id)
}

// changed schedules OnChange without waiting for it.
func (m *Manager) changed() {
	select {
	case m.notify <- struct{}{}:
	default: // one is pending, and will read the latest state
	}
}

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
	return m.ensureScoped(id, nil, "", nil, false)
}

// ensureScoped starts the agent if needed. auto: for yesman's own message to
// a lead, which starts nothing while the user's pause holds it.
func (m *Manager) ensureScoped(id string, owner *agent, messageID string, scope *time.Time, auto bool) (*agent, *proc, error) {
	ys, name, err := SplitID(id)
	if err != nil {
		return nil, nil, err
	}
	var (
		a *agent
		p *proc
	)
	// Starting inside Use keeps a dismissal from archiving the workspace
	// under a starting agent, or an agent from starting in an archived one.
	err = m.cfg.Yesmen.Use(ys, func(y yesmen.Yesman) error {
		if scope != nil && !y.HiredAt.Equal(*scope) {
			return errStopped
		}
		if owner != nil {
			m.mu.Lock()
			d, ok := owner.deliveries[messageID]
			current := m.agents[id] == owner && !owner.retiring && ok && d.State == "sending"
			m.mu.Unlock()
			if !current {
				return errStopped
			}
		}
		var err error
		a, p, err = m.ensureIn(y, id, name, auto)
		return err
	})
	return a, p, err
}

func (m *Manager) ensureIn(y yesmen.Yesman, id, name string, auto bool) (*agent, *proc, error) {
	ys := y.Name
	role := RoleLead
	since := y.HiredAt
	if name != yesmen.LeadName {
		role = RoleWorker
		ws, _ := m.cfg.Yesmen.Workers(ys)
		found := false
		for _, w := range ws {
			if w.Name == name {
				found = true
				since = w.StartedAt
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("%s has no worker named %s", ys, name)
		}
	}

	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, nil, errStopped
		}
		if m.worktreeCleanup[id] {
			m.mu.Unlock()
			return nil, nil, errWorktreeCleanup
		}
		if auto && m.leadPausedLocked(y) {
			m.mu.Unlock()
			return nil, nil, errLeadPaused
		}
		a, err := m.workspaceAgentLocked(y, Info{ID: id, Yesman: ys, Name: name, Role: role, Status: Stopped, Since: since})
		if err != nil {
			m.mu.Unlock()
			return nil, nil, err
		}
		if a.deliveries == nil {
			m.mu.Unlock()
			if err := m.ensureDeliveries(a, filepath.Join(y.Path, ".yesman", "sessions", name, "delivery.json")); err != nil {
				return nil, nil, err
			}
			continue
		}
		if a.proc != nil {
			p := a.proc
			select {
			case <-p.done:
				// Exited, and watch has not cleared it yet. Its credential
				// stops authenticating now; the changes it already began end
				// first, without the lock they may wait for.
				m.revokeLocked(a, p)
				m.mu.Unlock()
				p.retireChanges()
				m.mu.Lock()
				if a.proc == p {
					m.clearLocked(a, p)
				}
				m.mu.Unlock()
				continue
			default:
				m.mu.Unlock()
				return a, p, nil
			}
		}
		if a.starting != nil {
			wait := a.starting
			m.mu.Unlock()
			<-wait
			continue
		}
		a.starting = make(chan struct{})
		startContext, cancelStart := context.WithCancel(context.Background())
		a.startCancel = cancelStart
		a.stopping = false
		a.info.Status, a.info.Detail, a.info.UpdatedAt = Starting, "", time.Now()
		m.mu.Unlock()
		m.changed()

		started := time.Now()
		p, state, err := m.spawn(startContext, a, y, name, role)
		cancelStart()

		m.mu.Lock()
		a.startCancel = nil
		if (a.stopping || m.closed) && err != nil {
			err = errStopped
		}
		if err == nil && (a.stopping || m.closed) {
			// Stop came during the start: end what it started before
			// letting Stop return.
			m.mu.Unlock()
			p.stop(10 * time.Second)
			m.retireCredential(a, p)
			m.mu.Lock()
			m.clearLocked(a, p)
			err = errStopped
		}
		close(a.starting)
		a.starting = nil
		if errors.Is(err, errStopped) {
			a.info.Status, a.info.Detail, a.info.UpdatedAt = Stopped, "", time.Now()
			m.mu.Unlock()
			m.changed()
			return nil, nil, err
		}
		if err != nil {
			a.info.Status, a.info.Detail, a.info.UpdatedAt = Failed, err.Error(), time.Now()
			m.mu.Unlock()
			m.changed()
			return nil, nil, err
		}
		a.proc = p
		p.piSession.Store(state.SessionID)
		a.nativeQueue = state.PendingQueue
		if state.PendingQueue.supported() {
			a.queue = pendingStatus(state.PendingQueue)
		}
		a.info.StartedAt = started
		a.info.Status, a.info.Model, a.info.UpdatedAt = Idle, state.Model.ID, time.Now()
		a.info.Provider, a.info.Effort, a.info.EffortLevels = state.Model.Provider, state.ThinkingLevel, nil
		m.mu.Unlock()
		m.changed()
		go m.watch(a, p)
		return a, p, nil
	}
}

func (m *Manager) spawn(startContext context.Context, a *agent, y yesmen.Yesman, name, role string) (*proc, modelState, error) {
	sessions := y.SessionDir(name)
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		return nil, modelState{}, err
	}
	args := []string{"--mode", "rpc", "--session-dir", sessions, "-e", m.cfg.Extension}
	resumed := hasSession(sessions)
	if resumed {
		args = append(args, "--continue")
	} else {
		args = append(args, "--name", ID(y.Name, name))
	}
	token := newToken()
	env := append(os.Environ(), m.cfg.Env...)
	env = append(env,
		"YESMAN_TOKEN="+token,
		"YESMAN_AGENT="+ID(y.Name, name),
		"YESMAN_AGENT_NAME="+name,
		"YESMAN_YESMAN="+y.Name,
		"YESMAN_ROLE="+role,
		"YESMAN_WORKSPACE="+y.Path,
	)
	path := m.cfg.Pi
	if m.cfg.Command != nil {
		ctx, cancel := context.WithTimeout(startContext, 10*time.Minute)
		var prefix []string
		var err error
		path, prefix, err = m.cfg.Command(ctx)
		cancel()
		if err != nil {
			return nil, modelState{}, err
		}
		args = append(prefix, args...)
	}
	p, err := startProc(spec{Path: path, Args: args, Dir: y.Path, Env: env}, func(p *proc, kind string, raw json.RawMessage) {
		m.record(a, p, kind, raw)
	})
	if err != nil {
		return nil, modelState{}, err
	}
	m.mu.Lock()
	a.token, a.tokenProc = token, p
	m.tokens[token] = a.info.ID
	m.mu.Unlock()
	// get_state answers once pi has loaded its session and extensions.
	ctx, cancel := context.WithTimeout(startContext, 60*time.Second)
	defer cancel()
	state, err := p.startupState(ctx)
	if err != nil {
		p.stop(2 * time.Second)
		m.retireCredential(a, p)
		m.mu.Lock()
		m.clearLocked(a, p)
		m.mu.Unlock()
		if msg := p.stderr.String(); msg != "" {
			return nil, modelState{}, fmt.Errorf("pi did not start: %v: %s", err, lastLines(msg, 6))
		}
		return nil, modelState{}, fmt.Errorf("pi did not start: %w", err)
	}
	// A lead's new session starts as the model chosen when its yesman was
	// hired; a resumed one keeps the model it was using. Workers are given
	// theirs by StartWorkerAs.
	if c, ok := ParseChoice(y.Model); ok && role == RoleLead && !resumed {
		// A model pi no longer has must not keep the lead from starting:
		// it runs as pi's own default instead.
		if err := applyChoice(ctx, p, c); err != nil {
			log.Printf("%s: start as %s: %v", ID(y.Name, name), c, err)
		}
		if s, err := p.modelState(ctx); err == nil {
			state = s
		}
	}
	return p, state, nil
}

// record handles one record of an agent's process. It runs on the
// process's reader, so it must not wait for that process's answers.
func (m *Manager) record(a *agent, p *proc, kind string, raw json.RawMessage) {
	raw = p.origins.observe(kind, raw)
	m.observeNativeInput(a, p, kind, raw)
	m.observeDelivery(a, p, kind, raw)
	m.follow(a, p, kind, raw)
	switch kind {
	case "queue_resume_start":
		// Native Session reserved preparation; no message has been taken and no
		// provider turn/usage is implied. Expose Stop while the app context loads.
		m.setStatus(a, p, Working, "")
	case "agent_start":
		m.setStatus(a, p, Working, "")
	case "agent_settled":
		var e struct {
			Aborted bool `json:"aborted"`
		}
		_ = json.Unmarshal(raw, &e)
		m.mu.Lock()
		if a.proc != p {
			m.mu.Unlock()
			break
		}
		worker, stopping, retiring := a.info.Role == RoleWorker, a.stopping, a.retiring
		handoff := worker && !retiring && (!e.Aborted || !stopping)
		if handoff {
			// The worker is still busy until its report reaches the lead.
			// Reserve this before publishing Idle, without waiting on RPC.
			a.reports++
		}
		a.info.Status, a.info.Detail, a.info.Dialog, a.info.UpdatedAt = Idle, "", nil, time.Now()
		a.settledInbox = a.inbox
		m.mu.Unlock()
		m.changed()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			a.modelMu.Lock()
			defer a.modelMu.Unlock()
			_, _ = m.syncModel(ctx, a, p)
		}()
		if !worker && !e.Aborted && !retiring {
			// A lead run the user stopped is theirs: it was recorded as a
			// pause by AbortByUser, and nothing is evaluated for it.
			go m.continueTasks(a.info.Yesman)
		} else if handoff {
			go func() {
				defer m.handoffDone(a)
				if !e.Aborted {
					m.reportToLead(a, p)
					return
				}
				// The user stopped its run. A worker being put to sleep, removed
				// or shut down with the app tells nobody: telling the lead would
				// start it again.
				m.tellLead(a, "stopped", fmt.Sprintf("[yesman] Worker %q was stopped by the user before it finished its run. It keeps its session; message it to continue.", a.info.Name))
			}()
		}
	case "thinking_level_changed":
		var e struct {
			Level string `json:"level"`
		}
		if json.Unmarshal(raw, &e) == nil {
			m.mu.Lock()
			if a.proc == p {
				a.info.Effort = e.Level
			}
			m.mu.Unlock()
			m.changed()
		}
	case "extension_ui_request":
		var e struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(raw, &e)
		switch e.Method {
		case "select", "confirm", "input", "editor":
			var d dialog
			_ = json.Unmarshal(raw, &d)
			m.mu.Lock()
			current := a.proc == p || a.proc == nil
			if current {
				now := time.Now()
				a.info.Status, a.info.Dialog, a.info.DialogAt, a.info.UpdatedAt = Waiting, raw, now, now
				if a.handedOver != d.ID {
					a.handedOver = ""
				}
			}
			worker := a.info.Role == RoleWorker
			m.mu.Unlock()
			m.changed()
			if current && d.Timeout > 0 && d.ID != "" {
				// pi answers a dialog itself when its timeout passes, and says
				// nothing: stop offering it then, so no late answer is taken.
				time.AfterFunc(time.Duration(d.Timeout)*time.Millisecond, func() { m.expireDialog(a, p, d.ID) })
			}
			if current && worker {
				// The lead answers for its workers; the user hears of it only
				// if the lead cannot decide.
				go m.askLead(a, raw)
			}
		}
	}
	m.cfg.OnEvent(m.number(a, kind, raw))
}

// number gives an event its sequence number and keeps the events of the
// assistant message in progress, for Messages.
func (m *Manager) number(a *agent, kind string, raw json.RawMessage) Event {
	var role string
	if kind == "message_start" || kind == "message_end" {
		var e struct {
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		role = e.Message.Role
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a.seq++
	ev := Event{Agent: a.info.ID, AgentSince: a.info.Since, Seq: a.seq, Data: raw}
	switch {
	case kind == "message_start" && role == "assistant":
		a.turn = []Event{ev}
	case kind == "message_end" && role == "assistant":
		a.turn = nil
	case kind == "message_update" && a.turn != nil:
		a.turn = append(a.turn, ev)
	case kind == "agent_settled":
		a.turn = nil // nothing is in progress any more
	}
	return ev
}

// watch follows a process until it exits.
func (m *Manager) watch(a *agent, p *proc) {
	<-p.done
	m.retireCredential(a, p)
	m.mu.Lock()
	if a.proc != p {
		m.mu.Unlock()
		return
	}
	m.clearLocked(a, p)
	failed := !a.stopping
	if failed {
		detail := "pi exited"
		if msg := p.stderr.String(); msg != "" {
			detail += ": " + lastLines(msg, 6)
		}
		a.info.Status, a.info.Detail = Failed, detail
	} else {
		a.info.Status, a.info.Detail = Stopped, ""
	}
	a.info.Dialog, a.info.UpdatedAt = nil, time.Now()
	worker, detail, since := a.info.Role == RoleWorker, a.info.Detail, a.info.Since
	m.mu.Unlock()
	m.changed()
	if failed && worker {
		go func() {
			// Recorded for its generation, so that the failure outlives a
			// restart and is told about once.
			if err := m.cfg.Yesmen.FailWorker(a.info.Yesman, a.info.Name, since, time.Now().UTC()); err != nil {
				log.Printf("yesman: record that %s failed: %v", a.info.ID, err)
			}
			m.tellLead(a, "failed", fmt.Sprintf("[yesman] Worker %q stopped unexpectedly (%s). It keeps its session; message it to start it again.", a.info.Name, clip(strings.Join(strings.Fields(detail), " "))))
			m.continueTasks(a.info.Yesman)
		}()
	} else if failed {
		go m.continueTasks(a.info.Yesman)
	}
}

// Prompt sends a user message. While the agent works, it steers the run:
// pi delivers it after the current tool calls.
func (m *Manager) Prompt(ctx context.Context, id, text string) error {
	return m.PromptImages(ctx, id, text, nil)
}

// PromptImages sends text and image content together, including when steering.
func (m *Manager) PromptImages(ctx context.Context, id, text string, images []Image) error {
	return m.promptImages(ctx, id, text, images, Origin{Source: OriginUser})
}

// PromptFromLead sends a worker a message its lead wrote, steering a run in
// progress like the user's messages.
func (m *Manager) PromptFromLead(ctx context.Context, id, text string) error {
	return m.promptImages(ctx, id, text, nil, Origin{Source: OriginLead, Kind: "message"})
}

func (m *Manager) promptImages(ctx context.Context, id, text string, images []Image, origin Origin) error {
	if err := validateImages(images); err != nil {
		return err
	}
	m.unnudge(id)
	if origin.Source == OriginUser {
		// What was kept for a lead the user paused follows their message.
		defer m.resumeByUser(id)()
	}
	return m.prompt(ctx, id, text, "steer", origin, images...)
}

func (m *Manager) prompt(ctx context.Context, id, text, behavior string, origin Origin, images ...Image) error {
	return m.promptDelivery(ctx, id, text, behavior, "", nil, nil, origin, images...)
}

func (m *Manager) promptDelivery(ctx context.Context, id, text, behavior, messageID string, owner *agent, scope *time.Time, origin Origin, images ...Image) error {
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return errors.New("empty message")
	}
	var (
		a *agent
		p *proc
		y yesmen.Yesman
	)
	auto := automatic(id, origin)
	if auto {
		ys, _, _ := SplitID(id)
		var err error
		if y, err = m.cfg.Yesmen.Get(ys); err != nil {
			return err
		}
		if m.loadPause(y) {
			return errLeadPaused
		}
	}
	for {
		var err error
		if a, p, err = m.ensureScoped(id, owner, messageID, scope, auto); err != nil {
			return err
		}
		// Register before dispatch, under the same lock as configuration's
		// idle check and writes, SleepIfIdle, and closing a finished worker.
		// Release it before waiting so other messages can steer a running
		// turn. pi may await extensions before reporting isStreaming.
		a.modelMu.Lock()
		if err := ctx.Err(); err != nil {
			a.modelMu.Unlock()
			return err
		}
		m.mu.Lock()
		current := a.proc == p && !a.retiring
		m.mu.Unlock()
		select {
		case <-p.done:
			current = false
		default:
		}
		if current {
			break
		}
		// Put to sleep or closed as finished while this waited: start again.
		a.modelMu.Unlock()
		m.mu.Lock()
		retiring := a.retiring
		m.mu.Unlock()
		if retiring {
			return errStopped
		}
	}
	m.mu.Lock()
	if m.worktreeCleanup[id] {
		m.mu.Unlock()
		a.modelMu.Unlock()
		return errWorktreeCleanup
	}
	if auto && m.leadPausedLocked(y) {
		m.mu.Unlock()
		a.modelMu.Unlock()
		return errLeadPaused
	}
	a.prompts++
	// A task notice is deduped by its own record: refused, it must not
	// leave the lead looking busy, which would hold off its retry.
	if origin.Source != OriginYesman || origin.Kind != "tasks" {
		a.inbox++
	}
	m.mu.Unlock()
	if a.info.Role == RoleWorker {
		// A message gives a finished worker work again.
		if reopened, err := m.reopenWorkerInScope(a); err != nil {
			log.Printf("yesman: reopen %s: %v", id, err)
		} else if reopened {
			m.changed()
		}
	}
	a.modelMu.Unlock()
	return m.sendPromptDelivery(ctx, a, p, text, behavior, messageID, origin, images...)
}

// sendPrompt dispatches a prompt already registered under modelMu and mu.
// No manager lock is held while sending or waiting for pi's answer.
func (m *Manager) sendPrompt(ctx context.Context, a *agent, p *proc, text, behavior string, origin Origin, images ...Image) error {
	return m.sendPromptDelivery(ctx, a, p, text, behavior, "", origin, images...)
}

func (m *Manager) sendPromptDelivery(ctx context.Context, a *agent, p *proc, text, behavior, messageID string, origin Origin, images ...Image) error {
	done := make(chan error, 1)
	go func() {
		// Cancelling the caller does not cancel the RPC already sent to pi.
		// Keep it registered until pi answers (or exits), even if the caller
		// stops waiting, so configuration cannot slip into its startup gap.
		cmd := map[string]any{"type": "prompt", "message": text, "streamingBehavior": behavior}
		if len(images) > 0 {
			cmd["images"] = images
		}
		m.mu.Lock()
		retiring := a.retiring || a.proc != p
		if a.nativeQueue.Capability == 3 {
			cmd["hostOrigin"] = origin
		}
		if messageID != "" && a.nativeQueue.supported() {
			cmd["submissionId"] = messageID
		}
		if !a.nativeQueue.supported() {
			m.forgetRecallLocked(a, messageID)
		}
		m.mu.Unlock()
		var err error
		if retiring {
			err = errStopped
		} else {
			err = m.bindDelivery(a, p, messageID)
		}
		if err == nil {
			// pi's answer, read in order with its records, says what became
			// of the prompt; see originTracker.
			p.origins.sent()
			answered := false
			_, err = p.callObserved(context.Background(), cmd, func(r response) {
				answered = true
				p.origins.answered(origin, behavior, &r)
				m.deliveryResponse(a, p, messageID, behavior, r)
			})
			if !answered {
				p.origins.answered(origin, behavior, nil)
			}
		}
		if m.beforePromptDone != nil {
			m.beforePromptDone(a) // tests: the answered prompt is slow to end
		}
		a.modelMu.Lock()
		m.mu.Lock()
		a.prompts--
		closeAt := time.Time{}
		if a.prompts == 0 && !a.closeAt.IsZero() {
			if a.inbox == a.closeInbox {
				closeAt = a.closeAt
			}
			a.closeAt = time.Time{}
		}
		// A lead that settled while this prompt was still unanswered was
		// not yet free to hear about its tasks: it is now.
		settled := a.prompts == 0 && a.info.Role == RoleLead && a.info.Status == Idle && a.inbox == a.settledInbox && !a.retiring
		m.mu.Unlock()
		a.modelMu.Unlock()
		if settled {
			go m.continueTasks(a.info.Yesman)
		}
		if !closeAt.IsZero() {
			go m.closeWhenIdle(a, p, closeAt)
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
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

// Messages returns the agent's conversation, starting it if needed. A
// finished worker started only to be read is closed again.
func (m *Manager) Messages(ctx context.Context, id string) (Conversation, error) {
	wasRunning := m.running(id) != nil
	a, p, err := m.ensure(id)
	if err != nil {
		return Conversation{}, err
	}
	if !wasRunning {
		defer func() { go m.closeIfFinished(a, p, time.Time{}) }()
	}
	var session string
	_, _ = p.modelState(ctx, func(s modelState) {
		session = s.SessionID
		m.deliverySessionChanged(a, p, s.SessionID)
		m.syncPending(a, p, s.PendingQueue)
	})
	// Origins recorded before this process are in the session's entries,
	// read once per session; the reader adds later ones.
	p.origins.scanMu.Lock()
	if p.origins.needsScan(session) {
		r, err := p.call(ctx, map[string]any{"type": "get_entries"})
		if err != nil {
			r.Data = nil
		}
		p.origins.indexEntries(session, r.Data)
	}
	p.origins.scanMu.Unlock()
	// pi answers after emitting every earlier event, and the reader numbers
	// those first. Capture the reply in progress and the last number on the
	// reader as the answer arrives: events it handles next are not in the
	// history, and the client replays them.
	var conv Conversation
	r, err := p.callObserved(ctx, map[string]any{"type": "get_messages"}, func(r response) {
		if p.origins.native && r.Success {
			var recorded struct {
				HostSession    string         `json:"hostSession"`
				RecordedInputs []hostInputRef `json:"recordedInputs"`
			}
			if json.Unmarshal(r.Data, &recorded) == nil {
				m.reconcileRecordedInputs(a, p, recorded.HostSession, recorded.RecordedInputs)
			}
		}
		m.mu.Lock()
		pruned := m.pruneDeliveryEventLocked(a)
		conv.Live, conv.Seq = append([]Event(nil), a.turn...), a.seq
		conv.Deliveries, conv.Queue = a.deliverySnapshot(), a.queue
		m.mu.Unlock()
		if pruned != nil {
			m.cfg.OnEvent(*pruned)
		}
	})
	if err != nil {
		return Conversation{}, err
	}
	var data struct {
		Messages       json.RawMessage `json:"messages"`
		InputRefs      []hostInputRef  `json:"inputRefs"`
		RecordedInputs []hostInputRef  `json:"recordedInputs"`
		HostSession    string          `json:"hostSession"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		return Conversation{}, err
	}
	if p.origins.native && data.HostSession == p.piSession.Load().(string) {
		conv.Messages = projectNativeMessages(data.Messages, data.InputRefs)
	} else if p.origins.native {
		// v2 can move queues but cannot bind user events/history. Do not feed
		// transfers into the legacy text/count provenance heuristic.
		conv.Messages = projectNativeMessages(data.Messages, nil)
	} else {
		conv.Messages = p.origins.project(data.Messages)
	}
	return conv, nil
}

// Answer answers the dialog an agent waits on. answer holds the response's
// fields: value, confirmed or cancelled.
func (m *Manager) Answer(id, requestID string, answer map[string]any) error {
	m.mu.Lock()
	a := m.agents[id]
	if a == nil || a.proc == nil {
		m.mu.Unlock()
		return fmt.Errorf("%s is not running", id)
	}
	var d dialog
	if a.info.Status != Waiting || json.Unmarshal(a.info.Dialog, &d) != nil || d.ID == "" || d.ID != requestID {
		m.mu.Unlock()
		return fmt.Errorf("%s is no longer waiting on question %q", id, requestID)
	}
	p, raw := a.proc, a.info.Dialog
	// Reserve the question before sending. A second answer cannot consume
	// it, and events after the response (including a new question) stay put.
	handedOver := a.handedOver
	a.info.Status, a.info.Dialog, a.info.UpdatedAt = Working, nil, time.Now()
	m.mu.Unlock()
	msg := map[string]any{}
	for k, v := range answer {
		msg[k] = v
	}
	msg["type"], msg["id"] = "extension_ui_response", requestID
	if err := p.send(msg); err != nil {
		m.mu.Lock()
		if a.proc == p && a.info.Status == Working && a.info.Dialog == nil {
			a.info.Status, a.info.Dialog = Waiting, raw
			a.handedOver = handedOver
		}
		m.mu.Unlock()
		m.changed()
		return err
	}
	m.changed()
	return nil
}

// Stop ends the agent's process, including one that is starting, and
// returns once it has exited. Its session stays, to resume later.
func (m *Manager) Stop(id string) {
	m.mu.Lock()
	a := m.agents[id]
	if a == nil {
		m.mu.Unlock()
		return
	}
	a.stopping = true
	if a.startCancel != nil {
		a.startCancel()
	}
	// Caller operations already admitted finish first; stopping admits no
	// more, so none commits for this process after Stop returns.
	for a.calls > 0 {
		m.callsDone.Wait()
	}
	wait := a.starting
	m.mu.Unlock()
	if wait != nil {
		<-wait // the start sees stopping and ends its process
	}
	m.mu.Lock()
	p := a.proc
	m.mu.Unlock()
	if p == nil {
		return
	}
	p.stop(10 * time.Second)
	// Its credential stops authenticating, and the changes its requests
	// already began end before Stop does.
	m.retireCredential(a, p)
	m.mu.Lock()
	if a.proc == p {
		m.clearLocked(a, p)
		a.info.Status, a.info.Detail, a.info.Dialog, a.info.UpdatedAt = Stopped, "", nil, time.Now()
	}
	m.mu.Unlock()
	m.changed()
}

// SleepIfIdle stops the agent's process, as Stop does, only if it is idle
// and started before the time given, and reports whether it stopped it.
// It holds the lock prompts register under from its check to the end of the
// stop, so no message slips in between: one sent meanwhile waits, then
// starts the agent again. pi is asked too, as it may be running or
// compacting before yesman hears of it.
func (m *Manager) SleepIfIdle(id string, startedBefore time.Time) bool {
	m.mu.Lock()
	a := m.agents[id]
	m.mu.Unlock()
	if a == nil {
		return false
	}
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	m.mu.Lock()
	p := a.proc
	idle := p != nil && a.starting == nil && a.prompts == 0 && a.recalls == 0 && a.reports == 0 && a.info.Status == Idle &&
		!a.info.StartedAt.IsZero() && a.info.StartedAt.Before(startedBefore)
	m.mu.Unlock()
	if !idle {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	state, err := p.modelState(ctx)
	cancel()
	if err != nil || state.IsStreaming || state.IsCompacting {
		return false
	}
	m.Stop(id)
	return true
}

// clearLocked forgets an agent's process, its credential, and the reply
// it was writing, which a new process will not continue. Callers first
// retire p's credential (retireCredential), which already forgot it.
func (m *Manager) clearLocked(a *agent, p *proc) {
	if a.proc == p {
		a.proc = nil
	}
	a.turn = nil
	a.handedOver = ""
	if events := m.interruptDeliveriesLocked(a); len(events) > 0 {
		go func() {
			for _, ev := range events {
				m.cfg.OnEvent(ev)
			}
		}()
	}
	a.endRun()
	if a.token != "" {
		delete(m.tokens, a.token)
		a.token, a.tokenProc = "", nil
	}
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// StopAll permanently closes the manager and stops every agent, for quitting.
func (m *Manager) StopAll() {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		close(m.shutdown)
	}
	for _, c := range m.continuations {
		if c.timer != nil {
			c.timer.Stop()
			c.timer = nil
		}
	}
	ids := make([]string, 0, len(m.agents))
	for id := range m.agents {
		ids = append(ids, id)
		a := m.agents[id]
		a.retiring = true
	}
	m.mu.Unlock()
	// Initializers already published their agent before leaving the manager
	// lock. Join them before collecting writers; none may appear after close.
	for _, id := range ids {
		m.mu.Lock()
		a := m.agents[id]
		var path string
		if a != nil && a.dir != "" && a.deliveries == nil {
			path = filepath.Join(a.dir, ".yesman", "sessions", a.info.Name, "delivery.json")
		}
		m.mu.Unlock()
		if path != "" {
			_ = m.ensureDeliveries(a, path)
		}
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() { m.Stop(id) })
	}
	wg.Wait()
	m.mu.Lock()
	var writers []*deliveryWriter
	for _, a := range m.agents {
		if a.deliveryWriter != nil {
			if !a.deliverySealed {
				m.sealDeliveriesLocked(a)
			}
			writers = append(writers, a.deliveryWriter)
		}
	}
	m.mu.Unlock()
	for _, writer := range writers {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			writer.close(ctx)
		})
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

// StartWorker records a worker of a yesman and gives it its task. title is
// the subject of its work in a few words, for the user.
func (m *Manager) StartWorker(ctx context.Context, yesman, name, title, task string) (Info, error) {
	return m.StartWorkerAs(ctx, yesman, name, title, task, Choice{})
}

// StartWorkerAs is StartWorker with the model and effort the worker runs
// as, in place of the workers' default; a zero Choice keeps the default.
func (m *Manager) StartWorkerAs(ctx context.Context, yesman, name, title, task string, as Choice) (Info, error) {
	if strings.TrimSpace(task) == "" {
		return Info{}, errors.New("a worker needs a task")
	}
	title = strings.Join(strings.Fields(title), " ")
	if n := utf8.RuneCountInString(title); n > titleLimit {
		return Info{}, fmt.Errorf("the title has %d characters: name the subject in at most %d, and put the rest in the task", n, titleLimit)
	}
	if err := m.cfg.Yesmen.AddWorker(yesman, yesmen.Worker{Name: name, Title: title, Task: task, StartedAt: time.Now().UTC()}); err != nil {
		return Info{}, err
	}
	id := ID(yesman, name)
	if !as.IsZero() {
		if _, err := m.RunAs(ctx, id, as); err != nil {
			return Info{}, errors.Join(fmt.Errorf("run as %s: %w", as, err), m.RemoveWorker(yesman, name))
		}
	}
	if err := m.prompt(ctx, id, task, "followUp", Origin{Source: OriginLead, Kind: "task"}); err != nil {
		// Leave no worker that never got its task, so the name can be
		// used again.
		return Info{}, errors.Join(err, m.RemoveWorker(yesman, name))
	}
	return m.Get(id)
}

// RemoveWorker stops a worker and takes it off its yesman's list. Its
// session log stays in the workspace.
func (m *Manager) RemoveWorker(yesman, name string) error {
	if name == yesmen.LeadName {
		return errors.New("the lead cannot be removed")
	}
	y, release, err := m.cfg.Yesmen.BeginUse(yesman)
	if err != nil {
		return err
	}
	defer release()
	id := ID(yesman, name)
	m.mu.Lock()
	a := m.agents[id]
	if a != nil {
		if a.removing {
			m.mu.Unlock()
			return errStopped
		}
		a.retiring, a.removing = true, true
	}
	m.mu.Unlock()
	fail := func(err error) error {
		m.mu.Lock()
		if a != nil {
			a.removing = false
		}
		m.mu.Unlock()
		return err
	}
	if a != nil {
		if err := m.ensureDeliveries(a, filepath.Join(y.SessionDir(name), "delivery.json")); err != nil {
			return fail(err)
		}
	}
	m.Stop(id)
	if a != nil {
		m.mu.Lock()
		var writer *deliveryWriter
		var events []Event
		if a.deliveryWriter != nil {
			if a.deliveryWriter.finished() {
				a.deliverySealed = false
				a.deliveryWriter = newDeliveryWriter(filepath.Join(y.SessionDir(name), "delivery.json"), m.cfg.deliveryWrite)
			}
			events = m.sealDeliveriesLocked(a)
			writer = a.deliveryWriter
		}
		m.mu.Unlock()
		for _, event := range events {
			m.cfg.OnEvent(event)
		}
		if writer != nil {
			if err := writer.close(context.Background()); err != nil {
				return fail(err)
			}
		}
	}
	if err = m.cfg.Yesmen.RemoveWorker(yesman, name); err != nil {
		return fail(err)
	}
	m.mu.Lock()
	if a != nil && m.agents[id] == a {
		delete(m.agents, id)
	}
	m.mu.Unlock()
	m.changed()
	return nil
}

// handoffDone releases the report reservation after delivery and closing.
// The lead may already have settled while the handoff was still pending.
func (m *Manager) handoffDone(a *agent) {
	m.mu.Lock()
	a.reports--
	lead := m.agents[ID(a.info.Yesman, yesmen.LeadName)]
	current := !a.retiring && (lead == nil || lead.workspaceSince.IsZero() || lead.workspaceSince.Equal(a.workspaceSince))
	m.mu.Unlock()
	if current {
		go m.continueTasks(a.info.Yesman)
	}
}

// reportToLead hands a worker's final message to its lead, which starts a
// run or reads it after the current one. A report that cannot be delivered
// after a few tries is shown on the worker, for the user.
func (m *Manager) reportToLead(a *agent, p *proc) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	report := "(no final message)"
	if r, err := p.call(ctx, map[string]any{"type": "get_last_assistant_text"}); err != nil {
		report = "(its final message could not be read: " + err.Error() + ")"
	} else {
		var data struct {
			Text *string `json:"text"`
		}
		_ = json.Unmarshal(r.Data, &data)
		if data.Text != nil && strings.TrimSpace(*data.Text) != "" {
			report = *data.Text
		}
	}
	if m.tellLeadWithin(ctx, a, "report", fmt.Sprintf("[yesman] Worker %q finished a run. Its final message:\n\n%s", a.info.Name, report)) == nil {
		m.closeIfFinished(a, p, time.Now().UTC())
	}
}

// closeIfFinished ends a finished worker's process while it is idle with no
// message on its way, so that a worker that is done takes nothing and shows
// as done. A finishedAt other than zero first records that it finished;
// zero closes it only if it already had. A message to it later starts it
// again, with its session, and gives it work again.
func (m *Manager) closeIfFinished(a *agent, p *proc, finishedAt time.Time) {
	if a.info.Role != RoleWorker {
		return
	}
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	m.mu.Lock()
	idle := !a.retiring && a.proc == p && a.starting == nil && a.info.Status == Idle && a.prompts == 0 && a.recalls == 0
	if !idle && !finishedAt.IsZero() && !a.retiring && a.proc == p && a.starting == nil && a.info.Status == Idle &&
		a.recalls == 0 && a.prompts > 0 && a.inbox == a.settledInbox {
		// Only prompts given before its run settled are still ending: the
		// last to end closes it, unless something new comes meanwhile.
		a.closeAt, a.closeInbox = finishedAt, a.inbox
	}
	yesman, name := a.info.Yesman, a.info.Name
	m.mu.Unlock()
	if !idle {
		return
	}
	_ = m.cfg.Yesmen.Use(yesman, func(y yesmen.Yesman) error {
		m.mu.Lock()
		active := !a.retiring && a.workspaceSince.Equal(y.HiredAt)
		m.mu.Unlock()
		if !active {
			return errStopped
		}
		if finishedAt.IsZero() {
			ws, err := m.cfg.Yesmen.Workers(yesman)
			if err != nil || !slices.ContainsFunc(ws, func(w yesmen.Worker) bool { return w.Name == name && !w.FinishedAt.IsZero() }) {
				return err
			}
		} else if err := m.cfg.Yesmen.FinishWorker(yesman, name, finishedAt); err != nil {
			log.Printf("yesman: finish %s: %v", a.info.ID, err)
			return err
		}
		// Prompts wait for modelMu, held here, and start it again once closed.
		m.Stop(a.info.ID)
		return nil
	})
}

// closeWhenIdle closes a finished worker whose report reached its lead
// before the prompt that began its run ended. pi is asked first, as it may
// be running that prompt before yesman hears of it.
func (m *Manager) closeWhenIdle(a *agent, p *proc, finishedAt time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	state, err := p.modelState(ctx)
	cancel()
	if err != nil || state.IsStreaming || state.IsCompacting {
		return // it has work: its next report closes it
	}
	m.closeIfFinished(a, p, finishedAt)
}

const titleLimit = 80

// tellLead hands the lead a message about one of its workers, which starts
// a run or is read after the current one. The lead never waits for its
// workers, so it learns how each run ended this way: finished, stopped by
// the user, or failed.
func (m *Manager) tellLead(a *agent, kind, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	m.tellLeadWithin(ctx, a, kind, msg)
}

// tellLeadWithin tries a few times; a message that cannot be delivered is
// shown on the worker, for the user.
func (m *Manager) tellLeadWithin(ctx context.Context, a *agent, kind, msg string) error {
	scope, active := m.reportScope(a)
	if !active {
		return errStopped
	}
	m.mu.Lock()
	origin := Origin{Source: OriginYesman, Kind: kind, Worker: a.info.Name}
	m.mu.Unlock()
	lead := ID(a.info.Yesman, yesmen.LeadName)
	m.unnudge(lead)
	var err error
	for attempt := range reportAttempts {
		select {
		case <-m.shutdown:
			return errStopped
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if _, active := m.reportScope(a); !active {
			return errStopped
		}
		if err = m.promptDelivery(ctx, lead, msg, "followUp", "", nil, &scope, origin); err == nil {
			return nil
		}
		if errors.Is(err, errLeadPaused) {
			// The user stopped the lead: keep this for when they write to it.
			if m.deferForLead(a.info.Yesman, scope, func() { m.redeliver(a, kind, msg) }) {
				m.mu.Lock()
				a.info.Detail, a.info.UpdatedAt = "Its last message waits until you write to the lead you stopped.", time.Now()
				m.mu.Unlock()
				m.changed()
				return err
			}
			continue // lifted meanwhile
		}
		select {
		case <-m.shutdown:
			return errStopped
		default:
		}
		if attempt < reportAttempts-1 {
			select {
			case <-time.After(time.Duration(attempt+1) * reportBackoff):
			case <-m.shutdown:
				return errStopped
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	log.Printf("yesman: report of %s to its lead failed: %v", a.info.ID, err)
	m.mu.Lock()
	a.info.Detail, a.info.UpdatedAt = "Its last report did not reach the lead: "+err.Error(), time.Now()
	m.mu.Unlock()
	m.changed()
	return err
}

// redeliver sends a message kept while the user had the lead paused. A
// report delivered then closes its finished worker, as it would have.
func (m *Manager) redeliver(a *agent, kind, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if m.tellLeadWithin(ctx, a, kind, msg) != nil {
		return
	}
	m.mu.Lock()
	p := a.proc
	if a.info.Detail == "Its last message waits until you write to the lead you stopped." {
		a.info.Detail = ""
	}
	m.mu.Unlock()
	m.changed()
	if kind == "report" && p != nil {
		m.closeIfFinished(a, p, time.Now().UTC())
	}
}

// How often, and how patiently, a worker's report is offered to its lead.
var (
	reportAttempts = 3
	reportBackoff  = 2 * time.Second
)

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
