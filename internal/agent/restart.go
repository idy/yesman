package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/idy/yesman/internal/restart"
	"github.com/idy/yesman/internal/yesmen"
)

// handoff is how far a worker's report of one run has got: one of the
// restart.Report states, and the digest of its final message once read.
// run tells this report from the next run's. active ties that run to a
// reservation in agent.reports, which can outlast or overlap other handoffs.
// All fields are guarded by the manager's mu.
type reportHandoff struct {
	run    uint64
	state  string
	digest string
	active bool
}

func (h reportHandoff) report() *restart.Report {
	return &restart.Report{State: h.state, Digest: h.digest}
}

// sendTrack follows one prompt from its admission to pi's answer, for a
// report whose fate must be known across a restart. admit runs under the
// manager's mu as the prompt registers, and refuses it by returning false;
// result runs once after, with restart.ReportNotSent when the prompt was
// not dispatched or pi refused it, ReportAccepted when pi took it, and
// ReportUncertain when it was sent and no answer came.
type sendTrack struct {
	admit  func() bool
	result func(state string)
	// bounded: the prompt's start and model lock end with its context too,
	// for a restart, which waits for nothing past its deadline.
	bounded bool
	current func() error // final effect admission, called under the process write lock
}

// reportTrack tracks the delivery of report run of a to its lead. Once
// the manager has closed for a restart or a quit, nothing more is
// admitted: the state recorded then is final for this instance. Only the
// latest run's report is tracked: an earlier one, still on its way when
// the worker finished another run, is delivered as before, untracked.
// once, for a report handed on after a restart, admits no other attempt
// after one sent with no answer, which may have arrived, and bounds the
// lead's start and model lock by ctx.
func (m *Manager) reportTrack(ctx context.Context, a *agent, run uint64, once bool) *sendTrack {
	return &sendTrack{
		bounded: once,
		current: func() error {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.closed || ctx.Err() != nil || m.agents[a.info.ID] != a || a.retiring || a.removing {
				return errStopped
			}
			if once {
				if a.givenWork {
					return errStopped
				}
				wp := m.workerPauses[a.info.ID]
				if wp != nil && wp.hiredAt.Equal(a.workspaceSince) && wp.since.Equal(a.info.Since) && wp.paused {
					return errStopped
				}
			}
			return nil
		},
		admit: func() bool {
			switch {
			case m.closed || ctx.Err() != nil || m.agents[a.info.ID] != a || a.retiring || a.removing:
				return false
			case a.handoff.run != run:
				return true // an earlier run's report: not tracked, not held back
			case once && a.handoff.state == restart.ReportUncertain:
				return false
			}
			a.handoff.state = restart.ReportUncertain // sent from here on, until pi answers
			return true
		},
		result: func(state string) {
			m.mu.Lock()
			if a.handoff.run == run && a.handoff.state != restart.ReportAccepted {
				a.handoff.state = state
			}
			m.mu.Unlock()
		},
	}
}

// endHandoff forgets an accepted report once delivery ended while the
// manager runs. Deferred, failed and ambiguous reports remain available to
// a later checkpoint; after shutdown all states stay for ReconcileRestart.
func (m *Manager) endHandoff(a *agent, run uint64) {
	m.mu.Lock()
	if !m.closed && a.handoff.run == run && a.handoff.state == restart.ReportAccepted {
		a.handoff = reportHandoff{run: run}
	}
	m.mu.Unlock()
}

func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// CloseForRestart closes the manager to new starts and to new deliveries
// of reports, as StopAll does first, and returns the agents whose work an
// explicit restart of the app interrupts: those running a turn, workers
// waiting on a question to their lead or whose report has not been settled,
// and agents waiting on the user (kept to be told, never woken). Agents that
// are idle, asleep, being put to sleep, failed, starting or retired are not
// listed: a run the user stopped leaves its agent idle, and the notice of
// it is not a report. It reads memory only, under the manager's lock;
// StopAll still stops the agents, and ReconcileRestart then updates the
// list with what settled meanwhile.
func (m *Manager) CloseForRestart() []restart.Agent {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeLocked()
	m.restartRuns = map[string]uint64{}
	var out []restart.Agent
	for id, a := range m.agents {
		if a.retiring || a.removing || a.stopping || a.proc == nil || a.starting != nil ||
			a.info.Since.IsZero() || a.workspaceSince.IsZero() {
			continue
		}
		e := restart.Agent{
			Yesman: a.info.Yesman, Name: a.info.Name, Role: a.info.Role,
			WorkspaceHiredAt: a.workspaceSince.UTC(), AgentSince: a.info.Since.UTC(),
		}
		switch {
		case a.info.Status == Working:
			e.Reason = restart.ReasonWorking
		case a.info.Status == Waiting && a.info.Role == RoleWorker &&
			!(a.handedOver != "" && dialogID(a.info.Dialog) == a.handedOver):
			e.Reason = restart.ReasonQuestion
		case a.info.Status == Waiting:
			e.Reason = restart.ReasonNeedsYou
		case a.info.Role == RoleWorker && a.handoff.state != "":
			e.Reason, e.Report = restart.ReasonReport, a.handoff.report()
		default:
			continue
		}
		m.restartRuns[id] = a.reportRun
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Yesman != out[j].Yesman {
			return out[i].Yesman < out[j].Yesman
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ReconcileRestart runs after StopAll, for the agents CloseForRestart
// listed. It waits, within ctx, for the report handoffs the shutdown cut
// short to end, then returns the list updated with where each report got:
// a worker listed working whose run settled meanwhile is listed for its
// report, which was not sent, and a report listed as on its way gets the
// state it ended in. A report that still has not ended stays uncertain.
func (m *Manager) ReconcileRestart(ctx context.Context, entries []restart.Agent) []restart.Agent {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		m.mu.Lock()
		busy := false
		for _, a := range m.agents {
			busy = busy || a.reports > 0
		}
		m.mu.Unlock()
		if !busy {
			break
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
			continue
		}
		break
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := slices.Clone(entries)
	for i, e := range out {
		if e.Role != RoleWorker || e.Reason != restart.ReasonWorking && e.Reason != restart.ReasonReport {
			continue
		}
		id := ID(e.Yesman, e.Name)
		a := m.agents[id]
		if a == nil || !a.info.Since.Equal(e.AgentSince) || !a.workspaceSince.Equal(e.WorkspaceHiredAt) || a.handoff.state == "" {
			continue
		}
		run, listed := m.restartRuns[id]
		switch {
		case !listed:
		case e.Reason == restart.ReasonWorking && a.handoff.run > run,
			e.Reason == restart.ReasonReport && a.handoff.run == run:
			out[i].Reason, out[i].Report = restart.ReasonReport, a.handoff.report()
		}
	}
	return out
}

// Why a resume sent nothing because its time ran out first.
func notAdmitted(err error) error {
	return fmt.Errorf("its time to continue ran out before anything was sent (%w); it waits for a message", err)
}

// ResumeAfterRestart lets an agent a restart interrupted go on, once: it checks the
// agent is still the one recorded, in the same workspace, unfinished, not
// asleep and not given newer work, starts it with its session, and sends it
// note. A worker whose report was known not to have reached its lead hands
// that report on instead, with no new run; one whose report the lead's pi
// took is recorded finished, as it would have been; one whose report may or
// may not have arrived is left as it is. It returns a restart outcome;
// error explains it. It never sends the messages the agent had been given.
//
// Everything Resume waits for ends with ctx: the time to start, and to
// take the agent's model lock. Once both are had, it checks again, under
// the manager's lock, that its time has not run out and that the agent is
// still the one recorded and free; only then does the note or report
// register. Expiry before that sends nothing; a note registered and sent,
// whose answer had not come when time ran out, is uncertain.
func (m *Manager) ResumeAfterRestart(ctx context.Context, r restart.Agent, note string) (string, error) {
	if !m.beginRestartOperation() {
		return restart.Stopped, nil
	}
	defer m.evaluations.Done()
	id := ID(r.Yesman, r.Name)
	if _, _, err := SplitID(id); err != nil {
		return restart.Gone, err
	}
	if err := ctx.Err(); err != nil {
		return restart.Failed, notAdmitted(err)
	}
	if r.Reason == restart.ReasonReport && r.Report == nil {
		// A claimed intent always says; where a report got is otherwise
		// unknown, and it is not sent.
		r.Report = &restart.Report{State: restart.ReportUncertain}
	}
	// Loading the lead pause may persist an unsaved setting, so do it
	// outside Use's lifecycle lease. Unknown remains paused.
	if r.Role == RoleLead || r.Reason == restart.ReasonReport && r.Report.State == restart.ReportNotSent {
		y, err := m.cfg.Yesmen.Get(r.Yesman)
		if err != nil || !y.HiredAt.Equal(r.WorkspaceHiredAt) {
			return restart.Gone, err
		}
		if m.loadPause(y) {
			return restart.Stopped, nil
		}
	}
	var (
		a       *agent
		p       *proc
		outcome string
		why     error
		ran     bool
	)
	err := m.cfg.Yesmen.Use(r.Yesman, func(y yesmen.Yesman) error {
		ran = true
		if !y.HiredAt.Equal(r.WorkspaceHiredAt) {
			outcome = restart.Gone // archived and hired again under this name
			return nil
		}
		if r.Role == RoleLead {
			if r.Name != yesmen.LeadName || !r.AgentSince.Equal(y.HiredAt) {
				outcome = restart.Gone
				return nil
			}
		} else {
			ws, err := m.cfg.Yesmen.Workers(y.Name)
			if err != nil {
				return err
			}
			var w *yesmen.Worker
			for i := range ws {
				if ws[i].Name == r.Name {
					w = &ws[i]
				}
			}
			switch {
			case w == nil || !w.StartedAt.Equal(r.AgentSince):
				outcome = restart.Gone // removed, or a new worker took the name
				return nil
			case !w.PausedAt.IsZero():
				outcome = restart.Stopped
				return nil
			case !w.FinishedAt.IsZero():
				outcome = restart.Finished // its report reached the lead
				return nil
			}
		}
		m.mu.Lock()
		closed := m.closed || m.restartPausedLocked(r)
		active, slept := m.restartHasInputLocked(r), false
		if cur := m.agents[id]; cur != nil {
			if cur.retiring || cur.removing || !cur.workspaceSince.Equal(r.WorkspaceHiredAt) || !cur.info.Since.Equal(r.AgentSince) {
				m.mu.Unlock()
				outcome = restart.Gone
				return nil
			}
			active = active || cur.restartHandled || cur.proc != nil && (cur.info.Status == Working || cur.info.Status == Waiting)
			// A history read can start pi without giving it work. Its pending
			// startup may be joined below; a Stop still wins at every boundary.
			slept = cur.stopping
		}
		m.mu.Unlock()
		switch {
		case closed || slept:
			outcome = restart.Stopped
			return nil
		case active:
			outcome = restart.Active // actual new work or an earlier recovery owns it
			return nil
		case r.Reason == restart.ReasonNeedsYou:
			outcome = restart.Waiting
			return nil
		case r.Reason == restart.ReasonReport && r.Report.State == restart.ReportAccepted:
			// Its lead's pi took the report; the old instance had not yet
			// recorded the worker finished. Record it, sending nothing.
			if err := m.cfg.Yesmen.FinishWorkerOf(y.Name, r.Name, r.AgentSince, time.Now().UTC()); err != nil {
				return err
			}
			outcome = restart.Accepted
			why = errors.New("its report had reached its lead's queue before the restart; it is not sent again, and whether the lead read it is not known")
			return nil
		case r.Reason == restart.ReasonReport && r.Report.State == restart.ReportUncertain:
			outcome = restart.Uncertain
			why = errors.New("its report was being sent to its lead when Yes Man restarted and no answer came, so it may or may not have arrived; it is not sent again: ask the worker or the lead")
			return nil
		}
		var err error
		a, p, err = m.ensureInCtx(ctx, y, id, r.Name, r.Role == RoleLead, false, func() error {
			cur := m.agents[id]
			if m.restartHasInputLocked(r) || m.restartPausedLocked(r) || cur == nil || cur.stopping || cur.restartHandled ||
				!cur.workspaceSince.Equal(r.WorkspaceHiredAt) || !cur.info.Since.Equal(r.AgentSince) {
				return errStopped
			}
			return nil
		})
		return err
	})
	switch {
	case !ran:
		return restart.Gone, err
	case errors.Is(err, errStopped):
		return restart.Stopped, nil
	case ctx.Err() != nil && err != nil:
		return restart.Failed, notAdmitted(err)
	case err != nil:
		return restart.Failed, err
	case outcome != "":
		if outcome == restart.Waiting || outcome == restart.Uncertain {
			why = errors.Join(why, m.HoldAfterRestart(r))
		}
		return outcome, why
	}

	if r.Reason == restart.ReasonReport {
		return m.resumeReport(ctx, r, id, a, p)
	}
	covered, err := m.restartTaskNotice(r)
	if err != nil {
		return restart.Failed, err
	}
	if outcome, err := m.admitResume(ctx, r, id, a, p, func() {
		a.prompts++
		a.inbox++
	}); outcome != "" {
		covered(restart.ReportNotSent)
		return outcome, err
	}
	state := make(chan string, 1)
	track := &sendTrack{result: func(s string) { covered(s); state <- s }}
	defer covered(restart.ReportUncertain) // a sent note may outlive this wait
	err = m.sendPromptTracked(ctx, a, p, note, "followUp", "", Origin{Source: OriginYesman, Kind: "restart"}, track, func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if m.closed || a.stopping || m.restartPausedLocked(r) || m.restartHasInputLocked(r) {
			return errStopped
		}
		return nil
	})
	if err == nil {
		return restart.Resumed, nil
	}
	select {
	case s := <-state:
		if s == restart.ReportNotSent {
			if errors.Is(err, errStopped) {
				return restart.Stopped, nil
			}
			return restart.Failed, err
		}
		return restart.Uncertain, fmt.Errorf("its note was sent and no answer came (%w); it may be continuing", err)
	default:
		// Time ran out while pi had the note and had not answered.
		return restart.Uncertain, fmt.Errorf("its note was sent and pi had not answered when time ran out (%w); it may be continuing", err)
	}
}

// admitResume takes a's model lock within ctx, then checks under the
// manager's lock that the restart may still act: time is left, the manager
// is open, the agent and its process are the ones started for it and not
// stopping, and nothing else is under way. Then reg registers its work, and
// admitResume returns no outcome; otherwise it returns the outcome.
func (m *Manager) admitResume(ctx context.Context, r restart.Agent, id string, a *agent, p *proc, reg func()) (string, error) {
	if err := lockWithin(ctx, &a.modelMu); err != nil {
		return restart.Failed, notAdmitted(err)
	}
	defer a.modelMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	exited := false
	select {
	case <-p.done:
		exited = true
	default:
	}
	switch {
	case ctx.Err() != nil:
		return restart.Failed, notAdmitted(ctx.Err())
	case m.closed || m.agents[id] != a || a.proc != p || a.retiring || a.stopping || m.restartPausedLocked(r):
		return restart.Stopped, nil
	case exited:
		return restart.Failed, errors.New("its pi exited before anything was sent")
	case !a.info.Since.Equal(r.AgentSince) || !a.workspaceSince.Equal(r.WorkspaceHiredAt):
		return restart.Gone, nil
	case m.restartHasInputLocked(r) || a.restartHandled || a.info.Status != Idle || a.prompts > 0 || a.reports > 0 || a.recalls > 0 || m.worktreeCleanup[id]:
		return restart.Active, nil
	}
	a.restartHandled = true
	reg()
	return "", nil
}

// lockWithin locks mu, or gives up when ctx ends. Waiting by trying lets a
// message that waits for mu go first.
func lockWithin(ctx context.Context, mu *sync.Mutex) error {
	if mu.TryLock() {
		return nil
	}
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if mu.TryLock() {
				return nil
			}
		}
	}
}

// resumeReport hands on a worker's report that was known not to have
// reached its lead, without a new run: it reads the worker's final message
// from its session, checks it is the one recorded when it was read before,
// and tells the lead. It then closes the worker, finished.
func (m *Manager) resumeReport(ctx context.Context, r restart.Agent, id string, a *agent, p *proc) (string, error) {
	var run uint64
	if outcome, err := m.admitResume(ctx, r, id, a, p, func() {
		a.reports++ // busy until the report reaches the lead, as after a run
		a.reportRun++
		run = a.reportRun
		a.handoff = reportHandoff{run: run, state: restart.ReportNotSent, digest: r.Report.Digest, active: true}
	}); outcome != "" {
		return outcome, err
	}
	defer m.handoffDone(a, run)
	defer m.endHandoff(a, run)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	text, err := m.readFinal(ctx, p) // a read: no new run
	if err != nil {
		return restart.Failed, fmt.Errorf("its final message could not be read, so its report was not sent: %w", err)
	}
	if r.Report.Digest != "" && digest(text) != r.Report.Digest {
		return restart.Uncertain, errors.New("its final message is no longer the one it finished with before the restart, so its report was not sent: ask the worker")
	}
	mark := m.runOf(a, p)
	err = m.tellLeadTracked(ctx, a, mark, "report", reportText(a.info.Name, text, true), m.reportTrack(ctx, a, run, true))
	m.mu.Lock()
	state := a.handoff.state // where the last attempt got
	m.mu.Unlock()
	switch {
	case err == nil || state == restart.ReportAccepted:
		m.closeIfFinished(a, mark, time.Now().UTC())
		return restart.Reported, nil
	case state == restart.ReportUncertain:
		return restart.Uncertain, fmt.Errorf("its report was sent to its lead and no answer came (%w); it is not sent again", err)
	case errors.Is(err, errStopped) || errors.Is(err, errLeadPaused):
		return restart.Stopped, nil
	}
	return restart.Failed, fmt.Errorf("its report did not reach the lead: %w", err)
}
