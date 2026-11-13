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
	RequestID string  `json:"requestId"`
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
	fmt.Fprintf(&b, "\nAnswer it with yesman_worker_answer, name %q, requestId %q. Decide it yourself whenever you can; only if you cannot, ask the user and answer with theirs.", worker, d.ID)
	return b.String()
}

// AnswerDialog answers the dialog a worker waits on, once the reply fits
// its question.
func (m *Manager) AnswerDialog(id string, r DialogReply) error {
	if r.RequestID == "" {
		return errors.New("give the original question's requestId")
	}
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
	if r.RequestID != d.ID {
		return fmt.Errorf("%s is no longer waiting on question %q", id, r.RequestID)
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
		a.inbox++
	}
	m.mu.Unlock()
}

// canRemindLocked checks the lead and all its workers under mu. A settled
// worker still has work under way while its report is read or delivered.
func (m *Manager) canRemindLocked(lead *agent, p *proc, inbox uint64) bool {
	if p == nil || lead.proc != p || lead.nudged || lead.stopping || lead.info.Status != Idle ||
		lead.prompts != 0 || lead.inbox != inbox || lead.settledInbox != inbox {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
	}
	for _, w := range m.agents {
		if w.info.Yesman != lead.info.Yesman || w.info.Role != RoleWorker {
			continue
		}
		if w.prompts > 0 || w.reports > 0 || w.starting != nil {
			return false
		}
		switch w.info.Status {
		case Starting, Working, Waiting:
			return false
		}
	}
	return true
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
	yesman, inbox := lead.info.Yesman, lead.inbox
	ready := m.canRemindLocked(lead, p, inbox)
	m.mu.Unlock()
	if !ready {
		return
	}
	board, err := m.cfg.Yesmen.Board(yesman)
	if err != nil {
		return
	}
	owners := map[string]Info{}
	for _, i := range m.List() {
		if i.Yesman == yesman {
			owners[i.Name] = i
		}
	}
	m.remindOfBoard(lead, p, inbox, board, owners)
}

// remindOfBoard rechecks after the board read and reserves the prompt before
// releasing the locks. A new message, worker or handoff takes precedence.
func (m *Manager) remindOfBoard(lead *agent, p *proc, inbox uint64, board yesmen.Board, owners map[string]Info) {
	lead.modelMu.Lock()
	m.mu.Lock()
	if !m.canRemindLocked(lead, p, inbox) {
		m.mu.Unlock()
		lead.modelMu.Unlock()
		return
	}
	yesman := lead.info.Yesman
	var open []yesmen.Task
	for _, t := range board.Tasks {
		if t.Status != yesmen.TaskTodo && t.Status != yesmen.TaskDoing {
			continue
		}
		if w := m.agents[ID(yesman, t.Owner)]; w != nil && w.stopping &&
			w.info.Status == Stopped && owners[t.Owner].FinishedAt.IsZero() {
			continue // deliberately put to sleep before finishing
		}
		open = append(open, t)
	}
	if len(open) == 0 {
		m.mu.Unlock()
		lead.modelMu.Unlock()
		return
	}

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
	b.WriteString("\nLeave workers deliberately put to sleep by the user asleep, and leave their unfinished tasks alone until the user asks to resume them. Carry on with every listed task you can now. Mark a task that waits on the user's decision blocked, with the question in its note. When only such tasks are left, put those decisions to the user together, each with the options and your recommendation.")
	lead.nudged = true
	lead.prompts++
	m.mu.Unlock()
	lead.modelMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := m.sendPrompt(ctx, lead, p, b.String(), "followUp"); err != nil {
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
