package agent

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/idy/yesman/internal/restart"
	"github.com/idy/yesman/internal/yesmen"
)

// CloseForRestart closes the manager to new starts, as StopAll does first,
// and returns the agents whose work an explicit restart of the app
// interrupts: those running a turn, workers waiting on a question to their
// lead or whose report is on its way, and agents waiting on the user (kept
// to be told, never woken). Agents that are idle, asleep, being put to
// sleep, failed, starting or retired are not listed: a run the user stopped
// leaves its agent idle. It reads memory only, under the manager's lock;
// StopAll still stops the agents.
func (m *Manager) CloseForRestart() []restart.Agent {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.closed = true
		close(m.shutdown)
	}
	var out []restart.Agent
	for _, a := range m.agents {
		if a.retiring || a.removing || a.stopping || a.proc == nil || a.starting != nil ||
			a.info.Since.IsZero() || a.workspaceSince.IsZero() {
			continue
		}
		var reason string
		switch {
		case a.info.Status == Working:
			reason = restart.ReasonWorking
		case a.info.Status == Waiting && a.info.Role == RoleWorker &&
			!(a.handedOver != "" && dialogID(a.info.Dialog) == a.handedOver):
			reason = restart.ReasonQuestion
		case a.info.Status == Waiting:
			reason = restart.ReasonNeedsYou
		case a.info.Role == RoleWorker && a.reports > 0:
			reason = restart.ReasonReport
		default:
			continue
		}
		out = append(out, restart.Agent{
			Yesman: a.info.Yesman, Name: a.info.Name, Role: a.info.Role,
			WorkspaceHiredAt: a.workspaceSince.UTC(), AgentSince: a.info.Since.UTC(), Reason: reason,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Yesman != out[j].Yesman {
			return out[i].Yesman < out[j].Yesman
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Resume lets an agent a restart interrupted go on, once: it checks the
// agent is still the one recorded, in the same workspace, unfinished and
// not already running, then starts it with its session and sends it note,
// or, for a worker whose report was on its way, hands its final message to
// the lead again. It returns a restart outcome; error explains a failure.
// It never sends the messages the agent had been given.
func (m *Manager) Resume(ctx context.Context, r restart.Agent, note string) (string, error) {
	id := ID(r.Yesman, r.Name)
	if _, _, err := SplitID(id); err != nil {
		return restart.Gone, err
	}
	var (
		a       *agent
		p       *proc
		outcome string
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
			case !w.FinishedAt.IsZero():
				outcome = restart.Finished // its report reached the lead
				return nil
			}
		}
		if r.Reason == restart.ReasonNeedsYou {
			outcome = restart.Waiting
			return nil
		}
		m.mu.Lock()
		closed := m.closed
		var running, slept bool
		if cur := m.agents[id]; cur != nil {
			running = cur.proc != nil || cur.starting != nil
			// Put to sleep in this instance before its turn came, as by
			// the user: it stays asleep. ensureIn would clear the mark.
			slept = !running && cur.stopping
		}
		m.mu.Unlock()
		switch {
		case closed || slept:
			outcome = restart.Stopped
			return nil
		case running:
			outcome = restart.Active // the user or the lead talked to it first
			return nil
		}
		var err error
		a, p, err = m.ensureIn(y, id, r.Name)
		return err
	})
	switch {
	case !ran:
		return restart.Gone, err
	case errors.Is(err, errStopped):
		return restart.Stopped, nil
	case err != nil:
		return restart.Failed, err
	case outcome != "":
		return outcome, nil
	}
	// Started after its time ran out: it waits for a message, unnoted, so
	// that a start that took too long does not wake it later.
	if err := ctx.Err(); err != nil {
		return restart.Failed, err
	}

	if r.Reason == restart.ReasonReport {
		m.mu.Lock()
		ok := m.agents[id] == a && a.proc == p && !a.retiring && !a.stopping && a.info.Since.Equal(r.AgentSince) &&
			a.info.Status == Idle && a.prompts == 0 && a.reports == 0
		if ok {
			a.reports++ // busy until the report reaches the lead, as after a run
		}
		m.mu.Unlock()
		if !ok {
			return restart.Active, nil
		}
		defer m.handoffDone(a)
		if err := m.reportToLeadAgain(ctx, a, p); err != nil {
			if errors.Is(err, errStopped) {
				return restart.Stopped, nil
			}
			return restart.Failed, err
		}
		return restart.Reported, nil
	}

	// Register before dispatch, as prompts do, and only if nothing came
	// first: a message the user or the lead sent meanwhile goes on instead.
	a.modelMu.Lock()
	m.mu.Lock()
	switch {
	case m.agents[id] != a || a.proc != p || a.retiring || a.stopping || !a.info.Since.Equal(r.AgentSince):
		outcome = restart.Stopped
	case a.info.Status != Idle || a.prompts > 0 || m.worktreeCleanup[id]:
		outcome = restart.Active
	default:
		a.prompts++
		a.inbox++
		a.nudged = false
	}
	m.mu.Unlock()
	a.modelMu.Unlock()
	if outcome != "" {
		return outcome, nil
	}
	if err := m.sendPrompt(ctx, a, p, note, "followUp", Origin{Source: OriginYesman, Kind: "restart"}); err != nil {
		if errors.Is(err, errStopped) {
			return restart.Stopped, nil
		}
		return restart.Failed, err
	}
	return restart.Resumed, nil
}

// reportToLeadAgain hands a worker's final message to its lead after a
// restart cut its handoff short, and closes the worker once it is
// delivered. The lead may have had it already.
func (m *Manager) reportToLeadAgain(ctx context.Context, a *agent, p *proc) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	report := m.finalMessage(ctx, p)
	if err := m.tellLeadWithin(ctx, a, "report", reportText(a.info.Name, report, true)); err != nil {
		return err
	}
	m.closeIfFinished(a, p, time.Now().UTC())
	return nil
}
