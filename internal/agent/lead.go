package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// The lead answers for its yesman: what its workers ask, how their runs end,
// and whether the work it took on is done. The user hears only what the lead
// cannot settle.

// DialogReply answers the dialog a worker waits on: Value for a choice or
// text, Confirmed for a yes or no, or Cancelled to dismiss it.
type DialogReply struct {
	Value     *string `json:"value,omitempty"`
	Confirmed *bool   `json:"confirmed,omitempty"`
	Cancelled bool    `json:"cancelled,omitempty"`
}

// dialog is an extension_ui_request that waits for an answer.
type dialog struct {
	ID          string   `json:"id"`
	Method      string   `json:"method"`
	Title       string   `json:"title"`
	Message     string   `json:"message"`
	Options     []string `json:"options"`
	Placeholder string   `json:"placeholder"`
	Prefill     string   `json:"prefill"`
	Timeout     int      `json:"timeout"` // milliseconds
}

// askLead hands a worker's question to its lead, which answers it with
// yesman_worker_answer, or asks the user when it cannot decide.
func (m *Manager) askLead(a *agent, raw json.RawMessage) {
	var d dialog
	if json.Unmarshal(raw, &d) != nil {
		return
	}
	m.mu.Lock()
	name := a.info.Name
	m.mu.Unlock()
	m.tellLead(a, describeQuestion(name, d))
}

func describeQuestion(worker string, d dialog) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[yesman] Worker %q asks:\n\n", worker)
	for _, s := range []string{d.Title, d.Message} {
		if s = strings.TrimSpace(s); s != "" {
			b.WriteString(s + "\n")
		}
	}
	switch d.Method {
	case "select":
		b.WriteString("\nChoose one of:\n")
		for _, o := range d.Options {
			fmt.Fprintf(&b, "- %s\n", o)
		}
	case "confirm":
		b.WriteString("\nAnswer yes or no.\n")
	case "input":
		b.WriteString("\nAnswer with a line of text")
		if d.Placeholder != "" {
			fmt.Fprintf(&b, ", such as %q", d.Placeholder)
		}
		b.WriteString(".\n")
	case "editor":
		if d.Prefill != "" {
			fmt.Fprintf(&b, "\nAnswer with text, starting from:\n\n%s\n", d.Prefill)
		} else {
			b.WriteString("\nAnswer with text.\n")
		}
	}
	if d.Timeout > 0 {
		fmt.Fprintf(&b, "It goes on without an answer after %s.\n", time.Duration(d.Timeout)*time.Millisecond)
	}
	b.WriteString("\nAnswer it with yesman_worker_answer. Decide it yourself whenever you can; only if you cannot, ask the user and answer with theirs.")
	return b.String()
}

// AnswerDialog answers the dialog a worker waits on, once the reply fits
// its question.
func (m *Manager) AnswerDialog(id string, r DialogReply) error {
	m.mu.Lock()
	var raw json.RawMessage
	if a := m.agents[id]; a != nil && a.info.Status == Waiting {
		raw = a.info.Dialog
	}
	m.mu.Unlock()
	if raw == nil {
		return fmt.Errorf("%s is not waiting for an answer", id)
	}
	var d dialog
	if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
		return fmt.Errorf("the question %s waits on cannot be read", id)
	}
	answer, err := d.reply(r)
	if err != nil {
		return err
	}
	return m.Answer(id, d.ID, answer)
}

func (d dialog) reply(r DialogReply) (map[string]any, error) {
	if r.Cancelled {
		return map[string]any{"cancelled": true}, nil
	}
	switch d.Method {
	case "confirm":
		if r.Confirmed == nil {
			return nil, errors.New("this is a yes or no question: give confirmed")
		}
		return map[string]any{"confirmed": *r.Confirmed}, nil
	case "select":
		if r.Value != nil {
			for _, o := range d.Options {
				if o == *r.Value {
					return map[string]any{"value": o}, nil
				}
			}
			for _, o := range d.Options {
				if strings.EqualFold(strings.TrimSpace(o), strings.TrimSpace(*r.Value)) {
					return map[string]any{"value": o}, nil
				}
			}
		}
		quoted := make([]string, len(d.Options))
		for i, o := range d.Options {
			quoted[i] = fmt.Sprintf("%q", o)
		}
		return nil, fmt.Errorf("give value as one of %s", strings.Join(quoted, ", "))
	default:
		if r.Value == nil {
			return nil, errors.New("this question asks for text: give value")
		}
		return map[string]any{"value": *r.Value}, nil
	}
}

// unnudge lets the lead be reminded again: someone wrote to it.
func (m *Manager) unnudge(id string) {
	m.mu.Lock()
	if a := m.agents[id]; a != nil {
		a.nudged = false
	}
	m.mu.Unlock()
}

// idleWith reports whether an agent is idle on p, with no message on its way.
func (m *Manager) idleWith(a *agent, p *proc) bool {
	a.modelMu.Lock()
	pending := a.prompts
	a.modelMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	return pending == 0 && a.proc == p && a.info.Status == Idle
}

// busy reports whether a worker has work under way or on its way.
func (m *Manager) busy(a *agent) bool {
	a.modelMu.Lock()
	pending := a.prompts
	a.modelMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	switch a.info.Status {
	case Starting, Working, Waiting:
		return true
	}
	return pending > 0 || a.starting != nil
}

const followLimit = 30

// followThrough runs when the lead ends a run. If no worker is busy and no
// message is on its way to the lead, but the board still has tasks to do or
// in progress, it reminds the lead once, so that the work it took on goes on
// without the user. A reminder is not repeated until the user or a worker
// writes to the lead again, so a lead that leaves tasks open on purpose is
// not pressed.
func (m *Manager) followThrough(lead *agent, p *proc) {
	m.mu.Lock()
	yesman, nudged := lead.info.Yesman, lead.nudged
	var workers []*agent
	for _, w := range m.agents {
		if w.info.Yesman == yesman && w.info.Role == RoleWorker {
			workers = append(workers, w)
		}
	}
	m.mu.Unlock()
	if nudged || !m.idleWith(lead, p) {
		return
	}
	for _, w := range workers {
		if m.busy(w) {
			return // its report starts the lead's next run
		}
	}
	board, err := m.cfg.Yesmen.Board(yesman)
	if err != nil {
		return
	}
	var open []yesmen.Task
	for _, t := range board.Tasks {
		if t.Status == yesmen.TaskTodo || t.Status == yesmen.TaskDoing {
			open = append(open, t)
		}
	}
	if len(open) == 0 {
		return
	}
	owners := map[string]Info{}
	for _, i := range m.List() {
		if i.Yesman == yesman {
			owners[i.Name] = i
		}
	}
	m.mu.Lock()
	if lead.proc != p || lead.nudged {
		m.mu.Unlock()
		return
	}
	lead.nudged = true
	m.mu.Unlock()

	var b strings.Builder
	b.WriteString("[yesman] No worker is running, but these tasks are still open:\n")
	for i, t := range open {
		if i == followLimit {
			fmt.Fprintf(&b, "- and %d more (yesman_board lists them)\n", len(open)-followLimit)
			break
		}
		fmt.Fprintf(&b, "- %s [%s] %s", t.ID, t.Status, t.Title)
		if t.Owner != "" {
			fmt.Fprintf(&b, " (owner %s: %s)", t.Owner, ownerState(owners, t.Owner))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nCarry on with every one you can now. Mark a task that waits on the user's decision blocked, with the question in its note. When only such tasks are left, put those decisions to the user together, each with the options and your recommendation.")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := m.prompt(ctx, ID(yesman, yesmen.LeadName), b.String(), "followUp"); err != nil {
		log.Printf("yesman: remind the lead of %s: %v", yesman, err)
	}
}

func ownerState(agents map[string]Info, name string) string {
	i, ok := agents[name]
	switch {
	case !ok:
		return "no such worker now"
	case !i.FinishedAt.IsZero():
		return "finished and closed"
	case i.Status == Failed:
		return "failed"
	case i.Status == Stopped:
		return "asleep"
	}
	return string(i.Status)
}
