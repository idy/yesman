package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// RunResult describes one human submission and all work its team ran.
// Completed describes the execution. The reply and board explain its outcome.
type RunResult struct {
	Status       string           `json:"status"`
	Workspace    string           `json:"workspace"`
	Root         string           `json:"root,omitempty"`
	SubmissionID string           `json:"submissionId,omitempty"`
	Reply        string           `json:"reply"`
	Handoffs     []yesmen.Handoff `json:"handoffs,omitempty"`
	Dialogs      []RunDialog      `json:"dialogs,omitempty"`
	Error        string           `json:"error,omitempty"`
}

type RunDialog struct {
	Agent   string          `json:"agent"`
	Request json.RawMessage `json:"request"`
}

// All fields are guarded by Manager.mu. A successful prompt RPC can precede
// intake and a worker report can start a new Lead turn after an earlier settle.
// Count the Lead's admitted inputs until Pi identifies their recorded message.
type singleRun struct {
	id, lead  string
	pending   map[Origin]int
	recorded  bool
	lastInput uint64 // lead.settled when its last tracked user message ended
	aborted   bool
	reply     string
	err       error
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
	var failed []string
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
		if a.info.Status == Failed || a.info.RunError != nil || a.handoff.state != "" && a.reports == 0 {
			detail := a.info.Detail
			if a.info.RunError != nil {
				detail = "model error: " + a.info.RunError.Message
			}
			failed = append(failed, a.info.ID+": "+detail)
			continue
		}
		if a.proc != nil && (a.inbox != a.settledInbox || a.queue.Steering+a.queue.FollowUp > 0 || a.info.Role == RoleWorker && a.givenWork) {
			return
		}
	}
	if lead.info.Status == Idle {
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
	if len(failed) > 0 {
		slices.Sort(failed)
		out.Status, out.Error = "failure", strings.Join(failed, "\n")
		return
	}
	if !r.recorded || len(r.pending) != 0 || lead.settled <= r.lastInput {
		return
	}
	if r.aborted {
		out.Status = "cancelled"
		return
	}
	out.Status = "completed"
}
