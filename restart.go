package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"github.com/idy/yesman/internal/restart"
)

// Restart and continue: the window, the tray and Restart to update all
// restart through one path. The intent is recorded as the old instance
// quits, before its agents stop; the next instance claims it once and lets
// the agents it lists go on. Quit records nothing.

// restartEvent tells the page what the last restart came to, or that one
// is under way.
var restartEvent = mygo.NewEvent[RestartView]("restart")

// RestartView is where restarting stands, for Settings › About.
type RestartView struct {
	// Pending: a restart was asked for and the app is quitting.
	Pending bool `json:"pending,omitempty"`
	// Last is what the last restart came to, once its agents were seen to.
	Last *restart.Result `json:"last,omitempty"`
}

// restarts coordinates the app's own restarts.
type restarts struct {
	dir *restart.Dir
	// Hooks: mygo.App.Relaunch, the running version, the time and the page.
	relaunch func()
	version  func() string
	now      func() time.Time
	changed  func(RestartView)

	mu      sync.Mutex
	pending *pendingRestart
	written string // the ID of the intent this instance wrote
	last    *restart.Result
	loaded  bool
}

type pendingRestart struct {
	id, kind, to string
}

func newRestarts(dir *restart.Dir) *restarts {
	return &restarts{
		dir:      dir,
		relaunch: func() { mygo.App.Relaunch() },
		version:  mygo.App.Version,
		now:      time.Now,
		changed:  func(v RestartView) { _ = restartEvent.Broadcast(v) },
	}
}

// begin asks for a restart of the given kind. A second request while one
// is under way does nothing more. An update that is installed starts with
// either kind; a restart to update needs one.
func (r *restarts) begin(kind string, update UpdateView) error {
	r.mu.Lock()
	if r.pending != nil {
		r.mu.Unlock()
		return nil // already restarting: one relaunch only
	}
	p := &pendingRestart{id: restart.NewID(), kind: kind}
	switch {
	case update.State == updateReady:
		p.kind, p.to = restart.KindUpdate, update.Version
	case kind == restart.KindUpdate:
		r.mu.Unlock()
		return errors.New("no update is ready")
	}
	r.pending = p
	v := r.viewLocked()
	r.mu.Unlock()
	r.changed(v)
	r.relaunch()
	return nil
}

// beforeStop runs as the app quits, before its agents stop. For a restart
// it closes the agents to new starts, records those whose work it cuts
// short, and keeps the record on disk before they stop. A record that
// cannot be written is logged, and the restart goes on without resuming.
func (r *restarts) beforeStop(closeAgents func() []restart.Agent) {
	r.mu.Lock()
	p := r.pending
	r.mu.Unlock()
	if p == nil {
		return // a plain Quit: nothing resumes
	}
	in := restart.Intent{
		Version: restart.Version, ID: p.id, Kind: p.kind, CreatedAt: r.now().UTC(),
		FromVersion: r.version(), ToVersion: p.to, Agents: closeAgents(),
	}
	if in.Agents == nil {
		in.Agents = []restart.Agent{}
	}
	if err := r.dir.Write(in); err != nil {
		log.Print("restart: agents will not continue by themselves: ", err)
		return
	}
	r.mu.Lock()
	r.written = p.id
	r.mu.Unlock()
}

// canceled runs when a quit was canceled: the restart did not happen, and
// its record, if this instance wrote one, goes.
func (r *restarts) canceled() {
	r.mu.Lock()
	written := r.written
	r.pending, r.written = nil, ""
	v := r.viewLocked()
	r.mu.Unlock()
	if written != "" {
		if err := r.dir.Cancel(written); err != nil {
			log.Print("restart: ", err)
		}
	}
	r.changed(v)
}

// resumer resumes one agent; Manager.Resume in the app.
type resumer func(ctx context.Context, a restart.Agent, note string) (string, error)

// Bounds of resuming after a restart.
const (
	resumeParallel = 4
	resumeTimeout  = 3 * time.Minute
)

// resume claims what the last restart recorded, once, and lets the agents
// it lists go on. A start that finds no record does nothing.
func (r *restarts) resume(resume resumer) {
	in, claim, found, rejected, err := r.dir.Claim()
	if err != nil && !found {
		log.Print("restart: ", err)
		return
	}
	if !found {
		return
	}
	running := r.version()
	res := restart.Result{ResumedAt: r.now().UTC(), Version: running, Agents: []restart.Outcome{}}
	if rejected != "" {
		log.Print("restart: the record of the last restart was not used and was kept aside: ", rejected)
		res.Rejected = rejected
	} else {
		res.ID, res.Kind, res.RestartedAt, res.FromVersion, res.ToVersion = in.ID, in.Kind, in.CreatedAt, in.FromVersion, in.ToVersion
		res.Agents = make([]restart.Outcome, len(in.Agents))
		sem := make(chan struct{}, resumeParallel)
		var wg sync.WaitGroup
		for i, a := range in.Agents {
			wg.Go(func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				ctx, cancel := context.WithTimeout(context.Background(), resumeTimeout)
				defer cancel()
				outcome, err := resume(ctx, a, restartNote(in, running, a.Reason))
				o := restart.Outcome{Yesman: a.Yesman, Name: a.Name, Role: a.Role, Reason: a.Reason, Outcome: outcome}
				if err != nil {
					o.Error = err.Error()
					log.Printf("restart: %s/%s: %s: %v", a.Yesman, a.Name, outcome, err)
				}
				res.Agents[i] = o
			})
		}
		wg.Wait()
	}
	if err := r.dir.Finish(claim, res); err != nil {
		log.Print("restart: ", err)
	}
	r.mu.Lock()
	r.last, r.loaded = &res, true
	v := r.viewLocked()
	r.mu.Unlock()
	r.changed(v)
}

// view is where restarting stands.
func (r *restarts) view() RestartView {
	r.mu.Lock()
	loaded := r.loaded
	r.mu.Unlock()
	if !loaded {
		last, ok, err := r.dir.Last()
		if err != nil {
			log.Print("restart: ", err)
		}
		r.mu.Lock()
		if !r.loaded {
			r.loaded = true
			if ok {
				r.last = &last
			}
		}
		r.mu.Unlock()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.viewLocked()
}

func (r *restarts) viewLocked() RestartView {
	return RestartView{Pending: r.pending != nil, Last: r.last}
}

// restartNote is what an agent the restart cut short is told: a checkpoint
// to continue from its own session, never the messages it had been given.
func restartNote(in restart.Intent, running, reason string) string {
	var b strings.Builder
	b.WriteString("[yesman] Yes Man restarted")
	if in.Kind == restart.KindUpdate && in.ToVersion != "" {
		if running == "" || running == in.ToVersion {
			fmt.Fprintf(&b, " to update to %s", in.ToVersion)
		} else {
			fmt.Fprintf(&b, " to update to %s, but the update did not apply: it still runs %s", in.ToVersion, running)
		}
	}
	if reason == restart.ReasonQuestion {
		b.WriteString(" while you waited for your lead to answer a question. The restart cancelled that question, so no answer will come to it: ask it again if you still need it, then continue your task.")
		return b.String()
	}
	b.WriteString(" while you were working, and your run was interrupted: a command or reply in progress may not have finished, and messages still queued for you were not delivered (whoever sent them can send them again). Continue the work you were doing, from your session and the board.")
	b.WriteString(" Before you repeat a command, push, message or anything else with effects outside this session, check whether it already took effect, and do not redo it blindly. If you had been told to stop, or were waiting for the user, keep waiting.")
	return b.String()
}

// Restart quits yesman and starts it again; agents that were working go
// on afterwards, from their sessions. An installed update starts with it.
func (s *service) Restart() error {
	return s.app.restarts.begin(restart.KindRestart, s.app.updates.snapshot())
}

// RestartStatus says what the last restart came to.
func (s *service) RestartStatus() RestartView { return s.app.restarts.view() }
