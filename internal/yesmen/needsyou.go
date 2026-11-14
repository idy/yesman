package yesmen

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// A handoff is something only the user can do for a yesman, which its lead
// registered explicitly so that it shows under Needs you: a decision, some
// information, logging in, installing something, or another manual step.
// Nothing is inferred from conversation text, the board or how a run ended;
// the user closes a handoff (done, or not needed), or the lead resolves it.
// Handoffs live in the workspace, so archiving a yesman takes them with it
// and a new yesman of the same name starts without them.

// Handoff types.
const (
	HandoffDecision = "decision"
	HandoffInput    = "input"
	HandoffLogin    = "login"
	HandoffInstall  = "install"
	HandoffManual   = "manual"
)

// HandoffTypes lists the types of handoff.
var HandoffTypes = []string{HandoffDecision, HandoffInput, HandoffLogin, HandoffInstall, HandoffManual}

// How a handoff was closed.
const (
	HandoffDone      = "done"       // the user said they did it
	HandoffNotNeeded = "not-needed" // the user said it is not needed
	HandoffResolved  = "resolved"   // the lead settled it itself
)

// Handoff is one thing a yesman needs the user to do.
type Handoff struct {
	ID     string `json:"id"` // n1, n2, … never reused
	Type   string `json:"type"`
	Title  string `json:"title"`            // what to do, in a line
	Detail string `json:"detail,omitempty"` // how, in Markdown
	// Task is the board task waiting on it, when there is one.
	Task string `json:"task,omitempty"`
	// Worker and WorkerSince name the worker whose work it concerns.
	Worker      string    `json:"worker,omitempty"`
	WorkerSince time.Time `json:"workerSince,omitzero"`
	// ToolCall is the lead's tool call that registered it, which locates it
	// in the lead's conversation; it also makes a repeated call idempotent.
	ToolCall  string    `json:"toolCall,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	// ClosedAt, Resolution, ClosedBy ("user" or "lead") and Note say how
	// it was closed; zero while it is open.
	ClosedAt   time.Time `json:"closedAt,omitzero"`
	Resolution string    `json:"resolution,omitempty"`
	ClosedBy   string    `json:"closedBy,omitempty"`
	Note       string    `json:"note,omitempty"`
	// extra keeps the fields of the file this version does not know; only
	// the file has them, never the API.
	extra map[string]json.RawMessage
}

// Open reports whether the handoff still waits on the user.
func (h Handoff) Open() bool { return h.ClosedAt.IsZero() }

// Handoffs is a yesman's list of handoffs.
type Handoffs struct {
	Items []Handoff `json:"items"`
	Next  int       `json:"next"`
	extra map[string]json.RawMessage
}

// Open returns the handoffs still waiting on the user, oldest first.
func (hs Handoffs) Open() []Handoff {
	var out []Handoff
	for _, h := range hs.Items {
		if h.Open() {
			out = append(out, h)
		}
	}
	return out
}

// Find returns the handoff with an ID.
func (hs Handoffs) Find(id string) (Handoff, bool) {
	i := slices.IndexFunc(hs.Items, func(h Handoff) bool { return h.ID == id })
	if i < 0 {
		return Handoff{}, false
	}
	return hs.Items[i], true
}

const (
	maxOpenHandoffs   = 50
	keptClosed        = 30
	maxHandoffTitle   = 200
	maxHandoffDetail  = 4000
	maxHandoffNote    = 2000
	maxHandoffToolLen = 200
)

// HandoffsFile is where a yesman's handoffs are kept.
func (y Yesman) HandoffsFile() string { return filepath.Join(y.MetaDir(), "needs-you.json") }

// Handoffs returns a yesman's handoffs. A missing file is an empty list; an
// unreadable one is an error, and is never overwritten.
func (s *Store) Handoffs(name string) (Handoffs, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handoffs(name)
}

func (s *Store) handoffs(name string) (Handoffs, error) {
	f, hs, err := s.openHandoffs(name)
	if err != nil {
		return Handoffs{}, err
	}
	f.close()
	return hs, nil
}

func (s *Store) openHandoffs(name string) (*handoffsFile, Handoffs, error) {
	y, err := s.Get(name)
	if err != nil {
		return nil, Handoffs{}, err
	}
	return openHandoffs(y)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// AddHandoff registers a handoff and returns it, and whether it is new. A
// handoff registered by the same tool call again is returned as it is,
// without a second one.
func (s *Store) AddHandoff(name string, h Handoff) (Handoff, bool, error) {
	h.Type = strings.TrimSpace(h.Type)
	if !slices.Contains(HandoffTypes, h.Type) {
		return Handoff{}, false, fmt.Errorf("type must be one of %s", strings.Join(HandoffTypes, ", "))
	}
	h.Title = oneLine(h.Title)
	if h.Title == "" {
		return Handoff{}, false, errors.New("give a title: what the user needs to do")
	}
	if n := utf8.RuneCountInString(h.Title); n > maxHandoffTitle {
		return Handoff{}, false, fmt.Errorf("the title has %d characters: say what to do in at most %d, and put the rest in detail", n, maxHandoffTitle)
	}
	h.Detail = strings.TrimSpace(h.Detail)
	if utf8.RuneCountInString(h.Detail) > maxHandoffDetail {
		return Handoff{}, false, fmt.Errorf("the detail is longer than %d characters", maxHandoffDetail)
	}
	if !utf8.ValidString(h.Title + h.Detail) {
		return Handoff{}, false, errors.New("the handoff is not valid UTF-8")
	}
	if len(h.ToolCall) > maxHandoffToolLen {
		return Handoff{}, false, errors.New("the tool call ID is too long")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, hs, err := s.openHandoffs(name)
	if err != nil {
		return Handoff{}, false, err
	}
	defer f.close()
	if h.ToolCall != "" {
		if i := slices.IndexFunc(hs.Items, func(x Handoff) bool { return x.ToolCall == h.ToolCall }); i >= 0 {
			return hs.Items[i], false, nil
		}
	}
	if len(hs.Open()) >= maxOpenHandoffs {
		return Handoff{}, false, fmt.Errorf("%d handoffs already wait on the user: resolve some first", maxOpenHandoffs)
	}
	if h.Task = strings.TrimSpace(h.Task); h.Task != "" {
		h.Task = TaskID(h.Task)
		b, err := s.board(name)
		if err != nil {
			return Handoff{}, false, err
		}
		if !slices.ContainsFunc(b.Tasks, func(t Task) bool { return t.ID == h.Task }) {
			return Handoff{}, false, fmt.Errorf("the board has no task %s", h.Task)
		}
	}
	if h.Worker != "" {
		ws, err := s.workers(name)
		if err != nil {
			return Handoff{}, false, err
		}
		i := slices.IndexFunc(ws, func(w Worker) bool { return w.Name == h.Worker })
		if i < 0 {
			return Handoff{}, false, fmt.Errorf("%s has no worker named %s", name, h.Worker)
		}
		h.WorkerSince = ws[i].StartedAt
	}
	hs.Next = max(hs.Next, highestHandoff(hs.Items)) + 1
	h.ID = "n" + strconv.Itoa(hs.Next)
	h.CreatedAt = time.Now().UTC()
	h.ClosedAt, h.Resolution, h.ClosedBy, h.Note = time.Time{}, "", "", ""
	hs.Items = append(hs.Items, h)
	if err := f.write(hs); err != nil {
		return Handoff{}, false, err
	}
	return h, true, nil
}

// highestHandoff guards against a hand-edited Next going backwards.
func highestHandoff(items []Handoff) int {
	n := 0
	for _, h := range items {
		if v, err := strconv.Atoi(strings.TrimPrefix(h.ID, "n")); err == nil {
			n = max(n, v)
		}
	}
	return n
}

// CloseHandoff closes an open handoff. by is "user" or "lead". It reports an
// error for a handoff that is gone or already closed, which is never
// reopened.
func (s *Store) CloseHandoff(name, id, resolution, by, note string) (Handoff, error) {
	switch resolution {
	case HandoffDone, HandoffNotNeeded, HandoffResolved:
	default:
		return Handoff{}, fmt.Errorf("%q is not a way to close a handoff", resolution)
	}
	if by != "user" && by != "lead" {
		return Handoff{}, fmt.Errorf("%q cannot close a handoff", by)
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > maxHandoffNote {
		return Handoff{}, fmt.Errorf("the note is longer than %d characters", maxHandoffNote)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, hs, err := s.openHandoffs(name)
	if err != nil {
		return Handoff{}, err
	}
	defer f.close()
	i := slices.IndexFunc(hs.Items, func(h Handoff) bool { return h.ID == id })
	if i < 0 {
		return Handoff{}, fmt.Errorf("%s has no handoff %s", name, id)
	}
	if !hs.Items[i].Open() {
		return hs.Items[i], fmt.Errorf("handoff %s was already closed", id)
	}
	h := &hs.Items[i]
	h.ClosedAt, h.Resolution, h.ClosedBy, h.Note = time.Now().UTC(), resolution, by, note
	closed := *h
	// Keep the most recent closed handoffs as history, and every open one.
	var keep []Handoff
	kept := 0
	for j := len(hs.Items) - 1; j >= 0; j-- {
		x := hs.Items[j]
		if !x.Open() {
			if kept >= keptClosed {
				continue
			}
			kept++
		}
		keep = append(keep, x)
	}
	slices.Reverse(keep)
	hs.Items = keep
	if err := f.write(hs); err != nil {
		return Handoff{}, err
	}
	return closed, nil
}
