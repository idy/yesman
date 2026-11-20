package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/idy/yesman/internal/restart"
	"github.com/idy/yesman/internal/yesmen"
)

// RunResult describes one human submission and all work its team ran.
// Completed describes the execution. The reply and board explain its outcome.
type RunResult struct {
	Status            string                `json:"status"`
	Workspace         string                `json:"workspace"`
	Root              string                `json:"root,omitempty"`
	SubmissionID      string                `json:"submissionId,omitempty"`
	Reply             string                `json:"reply"`
	Handoffs          []yesmen.Handoff      `json:"handoffs,omitempty"`
	Dialogs           []RunDialog           `json:"dialogs,omitempty"`
	InterruptedAgents []RunInterruptedAgent `json:"interruptedAgents,omitempty"`
	Error             string                `json:"error,omitempty"`
}

// RunInterruptedAgent is the Manager's observation at the timeout boundary.
// It names the hire and phase without retaining any message or tool data.
type RunInterruptedAgent struct {
	ID           string    `json:"id"`
	Role         string    `json:"role"`
	Since        time.Time `json:"since,omitzero"`
	Phase        string    `json:"phase"`
	WaitingOn    string    `json:"waitingOn,omitempty"`
	ReportStatus string    `json:"reportStatus,omitempty"`
}

type RunDialog struct {
	Agent   string          `json:"agent"`
	Request json.RawMessage `json:"request"`
}

// All fields are guarded by Manager.mu. A successful prompt RPC can precede
// intake and a worker report can start a new Lead turn after an earlier settle.
// Count the Lead's admitted inputs until Pi identifies their recorded message.
type singleRun struct {
	id, lead   string
	pending    map[Origin]int
	recorded   bool
	lastInput  uint64 // lead.settled when its last tracked user message ended
	aborted    bool
	reply      string
	handoffErr error // a worker report or failure notice could not reach the Lead
	err        error
}

func checkExactModel(s modelState, want Choice) error {
	got := Choice{Provider: s.Model.Provider, Model: s.Model.ID, Effort: s.ThinkingLevel}
	if got != want {
		return fmt.Errorf("requested %s but Pi selected %s", want, got)
	}
	return nil
}

func (m *Manager) runPrompt(a *agent, o Origin, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.run; r != nil && a.info.ID == r.lead {
		r.pending[o] += count
		if r.pending[o] <= 0 {
			delete(r.pending, o)
		}
	}
}

// An extension can handle an input without ever recording it in the Lead's
// session. Neither the original request nor a worker report can then be
// counted as delivered.
func (m *Manager) runHandled(a *agent, response response) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.run == nil || a.info.ID != m.run.lead {
		return
	}
	var data struct{ Disposition string }
	if json.Unmarshal(response.Data, &data) == nil && data.Disposition == "handled" {
		m.run.err = errors.New("a Pi extension handled the Lead input before it entered the conversation")
	}
}

func (m *Manager) observeRun(a *agent, p *proc, kind string, raw json.RawMessage) {
	if kind != "message_end" && kind != "agent_settled" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.run
	if r == nil || a.proc != p || m.closed {
		return
	}
	if kind == "agent_settled" {
		if a.info.ID == r.lead {
			var e struct{ Aborted bool }
			if json.Unmarshal(raw, &e) == nil {
				r.aborted = e.Aborted
			}
		}
		return
	}
	var e struct {
		Message struct {
			Role, Provider, Model, ThinkingLevel string
			Yesman                               Origin
			Content                              json.RawMessage
		}
	}
	if json.Unmarshal(raw, &e) != nil {
		return
	}
	msg := e.Message
	if msg.Role == "assistant" {
		pin := m.cfg.ExactModel
		if !pin.IsZero() && (pin.Provider != msg.Provider || pin.Model != msg.Model || msg.ThinkingLevel != "" && pin.Effort != msg.ThinkingLevel) {
			r.err = fmt.Errorf("%s received a response for %s/%s at %s, requested %s", a.info.ID, msg.Provider, msg.Model, msg.ThinkingLevel, pin)
		}
	}
	if a.info.ID != r.lead {
		return
	}
	switch msg.Role {
	case "user":
		if n := r.pending[msg.Yesman]; n > 0 {
			r.lastInput = a.settled
			if n == 1 {
				delete(r.pending, msg.Yesman)
			} else {
				r.pending[msg.Yesman]--
			}
			if msg.Yesman.Source == OriginUser && msg.Yesman.Submission == r.id {
				r.recorded = true
			}
		}
	case "assistant":
		var blocks []struct{ Type, Text string }
		if r.recorded && json.Unmarshal(msg.Content, &blocks) == nil {
			var text []string
			for _, block := range blocks {
				if block.Type == "text" {
					text = append(text, block.Text)
				}
			}
			if len(text) > 0 {
				r.reply = strings.Join(text, "\n")
			}
		}
	}
}

// RunOnce owns a dedicated Manager from the first submission to shutdown. It
// does not start any unrelated workspace, schedules, or desktop recovery.
func (m *Manager) RunOnce(ctx context.Context, y yesmen.Yesman, prompt string) (out RunResult) {
	r := &singleRun{id: NewSubmissionID(), lead: ID(y.Name, yesmen.LeadName), pending: map[Origin]int{}}
	out = RunResult{Workspace: y.Name, Root: y.Path, SubmissionID: r.id}
	m.mu.Lock()
	if m.closed || m.run != nil || len(m.agents) != 0 {
		m.mu.Unlock()
		out.Status, out.Error = "failure", "RunOnce requires an unused Manager"
		return
	}
	m.run = r
	m.mu.Unlock()
	// Submit durably owns the input after registration. Interrupt the actual
	// processes on cancellation, including startup and a pending RPC ack.
	submitted := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		d, err := m.Submit(ctx, r.lead, r.id, prompt, nil, y.HiredAt)
		if err == nil {
			switch d.State {
			case "failed", "interrupted", "cancelled", "handled":
				err = fmt.Errorf("Lead submission %s: %s", d.State, d.Error)
			}
		}
		submitted <- err
	}()
	defer func() {
		if err := m.Shutdown(); err != nil {
			out.Error = errors.Join(nonemptyError(out.Error), err).Error()
			if out.Status == "completed" || out.Status == "needs_input" {
				out.Status = "failure"
			}
		}
		<-joined
	}()

	// This ticker only wakes the reader. Completion never depends on an
	// arbitrary quiet interval. Recheck all reservations and close admission
	// under the same mutex that admits a prompt, report or continuation.
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	var submitErr error
	for {
		m.mu.Lock()
		out.Reply = r.reply
		switch {
		case ctx.Err() != nil:
			out.Status, out.Error = ContextStatus(ctx.Err()), ctx.Err().Error()
		case submitErr != nil:
			out.Status, out.Error = "failure", submitErr.Error()
		case r.err != nil:
			out.Status, out.Error = "failure", r.err.Error()
		default:
			m.checkRunLocked(y, submitted == nil, &out)
		}
		if out.Status != "" {
			m.closeLocked()
			if out.Status == "timed_out" {
				out.InterruptedAgents = m.interruptedRunAgentsLocked(y)
			}
		}
		m.mu.Unlock()
		if out.Status != "" {
			break
		}
		select {
		case <-ctx.Done():
		case submitErr = <-submitted:
			submitted = nil
		case <-tick.C:
		}
	}
	if out.Status == "completed" || out.Status == "needs_input" {
		hs, err := m.cfg.Yesmen.Handoffs(y.Name)
		if err != nil {
			out.Status, out.Error = "failure", err.Error()
		} else if out.Handoffs = hs.Open(); len(out.Handoffs) > 0 {
			out.Status = "needs_input"
		}
	}
	return
}

func nonemptyError(text string) error {
	if text == "" {
		return nil
	}
	return errors.New(text)
}

// ContextStatus maps a run deadline or process signal to its shell result.
func ContextStatus(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed_out"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "failure"
}

// runInputQueuedLocked also sees a worker's admitted input before Pi reports
// its next turn. The Lead's intake and settlement are checked separately.
func runInputQueuedLocked(a *agent, worker bool) bool {
	return a.proc != nil && (a.queue.Steering+a.queue.FollowUp > 0 || worker && a.inbox != a.settledInbox)
}

func (m *Manager) interruptedRunAgentsLocked(y yesmen.Yesman) []RunInterruptedAgent {
	var out []RunInterruptedAgent
	for _, a := range m.agents {
		if a.info.Yesman != y.Name || !a.workspaceSince.Equal(y.HiredAt) || a.retiring || a.removing {
			continue
		}
		e := RunInterruptedAgent{ID: a.info.ID, Role: a.info.Role, Since: a.info.Since.UTC()}
		worker := e.Role == RoleWorker
		live := a.proc != nil && !a.stopping
		if live {
			select {
			case <-a.proc.done:
				live = false
			default:
			}
		}
		switch {
		case !a.stopping && (a.starting != nil || a.info.Status == Starting):
			e.Phase = "starting"
		case live && a.info.Status == Working:
			e.Phase = "working"
		case live && a.info.Status == Waiting:
			e.Phase = "waiting"
			if id := dialogID(a.info.Dialog); id != "" {
				if !worker || id == a.handedOver {
					e.WaitingOn = "user"
				} else {
					e.WaitingOn = "lead"
				}
			}
		case live && (a.prompts > 0 || runInputQueuedLocked(a, worker)):
			e.Phase = "input_pending"
		case worker && (a.reports > 0 || a.handoff.state != ""):
			accepted := a.reports <= 1 && a.handoff.state == restart.ReportAccepted
			if accepted && !live {
				continue // its accepted report is only finishing cleanup
			}
			e.Phase, e.ReportStatus = "reporting", "unknown"
			// Only the latest final report is tracked. An outstanding notice
			// or a retry has no reliable negative receipt for the whole handoff.
			if accepted {
				e.ReportStatus = "accepted"
			}
		default:
			continue
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b RunInterruptedAgent) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func (m *Manager) checkRunLocked(y yesmen.Yesman, submitted bool, out *RunResult) {
	r := m.run
	lead := m.agents[r.lead]
	if lead == nil {
		return
	}
	if lead.info.Status == Waiting && len(lead.info.Dialog) > 0 {
		// Only a human can release the Lead's dialog, even when a worker
		// is also waiting for the Lead to process its question or report.
		out.Status, out.Dialogs = "needs_input", []RunDialog{{lead.info.ID, lead.info.Dialog}}
		return
	}
	var dialogs []RunDialog
	var undelivered []string
	for _, a := range m.agents {
		if a.info.Yesman != y.Name || !a.workspaceSince.Equal(y.HiredAt) {
			continue
		}
		if a.info.Status == Waiting && a.handedOver != "" && dialogID(a.info.Dialog) == a.handedOver {
			dialogs = append(dialogs, RunDialog{a.info.ID, a.info.Dialog})
			continue
		}
		if busyLocked(a) || a.calls > 0 || len(a.operations) > 0 || a.removing || a.proc != nil && a.stopping {
			return
		}
		if runInputQueuedLocked(a, a != lead) {
			return // includes a worker given new work after an earlier error
		}
		if a.handoff.state != "" {
			// reports is zero here. Delivery attempts have ended, yet the
			// worker has no receipt, so its normal close may never happen.
			undelivered = append(undelivered, fmt.Sprintf("%s: worker report %s", a.info.ID, a.handoff.state))
			continue
		}
		if a != lead && a.proc != nil && a.givenWork {
			return // the worker's final report closes it
		}
	}
	// A rejected prompt increments the Lead inbox but can never settle it.
	// Check the recorded inputs below even if that transport failure prevents
	// the usual idle check from succeeding.
	if lead.info.Status == Idle && r.handoffErr == nil && len(undelivered) == 0 {
		if _, _, _, state := m.leadStateLocked(y.Name); state != leadIdle {
			return // includes a just-exited process awaiting its watcher
		}
	}
	if c := m.continuations[y.Name]; c != nil && (c.running || c.again || c.timer != nil) {
		return
	}
	if len(dialogs) > 0 {
		out.Status, out.Dialogs = "needs_input", dialogs
		return
	}
	if !submitted {
		return
	}
	if lead.info.Status == Failed {
		out.Status, out.Error = "failure", lead.info.ID+": "+lead.info.Detail
		return
	}
	// A terminal error can be known even if an external Pi never identified
	// the original user message. Every later Lead input still has to drain.
	pending := len(r.pending)
	if !r.recorded && r.pending[Origin{Source: OriginUser, Submission: r.id}] == 1 {
		pending--
	}
	if pending != 0 || lead.settled <= r.lastInput {
		return
	}
	if r.handoffErr != nil {
		out.Status, out.Error = "failure", r.handoffErr.Error()
		return
	}
	if len(undelivered) != 0 {
		slices.Sort(undelivered)
		out.Status, out.Error = "failure", strings.Join(undelivered, "\n")
		return
	}
	// The Lead's final execution outcome governs the CLI result. Its reply
	// and the board describe what the team delivered.
	if lead.info.RunError != nil {
		out.Status, out.Error = "failure", lead.info.ID+": model error: "+lead.info.RunError.Message
		return
	}
	if !r.recorded {
		return
	}
	if r.aborted {
		out.Status = "cancelled"
		return
	}
	out.Status = "completed"
}
