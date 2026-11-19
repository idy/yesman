package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
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
	m.tellLead(a, "question", describeQuestion(name, d))
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

// DialogScope binds a window's answer to the exact workspace, agent and
// question instance it displayed. HandedOverOnly is used by Needs You.
type DialogScope struct {
	HiredAt        time.Time `json:"hiredAt"`
	Since          time.Time `json:"since"`
	AskedAt        time.Time `json:"askedAt"`
	HandedOverOnly bool      `json:"handedOverOnly,omitempty"`
}

// AnswerDialog answers the dialog a worker waits on, once the reply fits
// its question.
func (m *Manager) AnswerDialog(id string, r DialogReply) error {
	return m.answerDialog(id, nil, r)
}

func (m *Manager) AnswerWindowDialog(id string, scope DialogScope, r DialogReply) error {
	return m.answerDialog(id, &scope, r)
}

func (d dialog) windowReply(r DialogReply) (map[string]any, error) {
	switch {
	case r.Cancelled && r.Value == nil && r.Confirmed == nil:
		return map[string]any{"cancelled": true}, nil
	case !r.Cancelled && d.Method == "confirm" && r.Confirmed != nil && r.Value == nil:
		return map[string]any{"confirmed": *r.Confirmed}, nil
	case !r.Cancelled && r.Confirmed == nil && r.Value != nil:
		if d.Method == "select" && slices.Contains(d.Options, *r.Value) {
			return map[string]any{"value": *r.Value}, nil
		}
		// Native Pi dialogs can return entire documents. The durable
		// handoff's short-answer budget does not apply to this RPC.
		if d.Method == "input" || d.Method == "editor" {
			return map[string]any{"value": *r.Value}, nil
		}
	}
	return nil, errors.New("the reply does not fit this question")
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

// unnudge records that a message is on its way to the lead, which settles
// before yesman tells it about its tasks.
func (m *Manager) unnudge(id string) {
	m.mu.Lock()
	if a := m.agents[id]; a != nil {
		a.inbox++
	}
	m.mu.Unlock()
}
