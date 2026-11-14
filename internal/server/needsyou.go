package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// Needs you is what waits on the user. A lead puts things there explicitly:
// a worker's question only the user can answer (handOver), or something only
// the user can do or decide (add). Nothing is inferred from text.

// NeedsYouChange is one change a lead makes to what waits on the user.
type NeedsYouChange struct {
	Add *struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Task   string `json:"task"`
		Worker string `json:"worker"`
	} `json:"add,omitempty"`
	// ToolCall is the lead's tool call ID, which locates an added handoff in
	// its conversation and makes a repeated call idempotent.
	ToolCall string `json:"toolCall,omitempty"`
	HandOver *struct {
		Worker    string `json:"worker"`
		RequestID string `json:"requestId"`
	} `json:"handOver,omitempty"`
	Resolve *struct {
		ID   string `json:"id"`
		Note string `json:"note"`
	} `json:"resolve,omitempty"`
}

// HandedOver is a worker's question the user was given.
type HandedOver struct {
	Worker    string    `json:"worker"`
	RequestID string    `json:"requestId"`
	Title     string    `json:"title"`
	Since     time.Time `json:"since"`
}

// NeedsYouView is what waits on the user from one yesman, for its lead.
// CallMemory is there only when the tool-call memory is nearly full, full
// or kept by a pre-release version: counts, never a call.
type NeedsYouView struct {
	Handoffs   []yesmen.Handoff   `json:"handoffs"`
	Questions  []HandedOver       `json:"questions"`
	CallMemory *yesmen.CallMemory `json:"callMemory,omitempty"`
}

// AddedHandoff is a handoff add returns, with the tool-call memory when it
// is nearly full.
type AddedHandoff struct {
	yesmen.Handoff
	CallMemory *yesmen.CallMemory `json:"callMemory,omitempty"`
}

// callMemoryNotice returns the memory when the lead should hear of it.
func callMemoryNotice(hs yesmen.Handoffs) *yesmen.CallMemory {
	m := hs.CallMemory()
	if m.State == "near" || m.Refusing() {
		return &m
	}
	return nil
}

// callMemoryLine tells the lead what the tool-call memory means for it.
func callMemoryLine(m *yesmen.CallMemory) string {
	switch m.State {
	case "near":
		return fmt.Sprintf("The tool-call memory of Needs you holds %d of %d tool calls: once it is full, add is refused until the user starts a new one under Needs you.", m.Used, m.Capacity)
	case "full":
		return fmt.Sprintf("The tool-call memory of Needs you is full (%d of %d tool calls): add with a new tool call is refused until the user starts a new one under Needs you. Tell the user; calling again does not help.", m.Used, m.Capacity)
	case "blocked":
		return "This yesman's Needs you was kept by a pre-release version that forgot tool calls without a trace: add is refused until the user starts a new tool-call memory under Needs you. Tell the user; calling again does not help."
	case "legacy-full":
		return "This yesman's Needs you was kept by a pre-release version whose memory of older tool calls is so full that add refuses many new ones: tell the user, who can start a new tool-call memory under Needs you."
	}
	return ""
}

func (s *Server) needsYouView(c caller) (NeedsYouView, error) {
	hs, err := s.Yesmen.Handoffs(c.yesman.Name)
	if err != nil {
		return NeedsYouView{}, err
	}
	view := NeedsYouView{Handoffs: hs.Open(), Questions: []HandedOver{}, CallMemory: callMemoryNotice(hs)}
	if view.Handoffs == nil {
		view.Handoffs = []yesmen.Handoff{}
	}
	for _, i := range s.Agents.List() {
		if i.Yesman != c.yesman.Name || !i.HandedOver {
			continue
		}
		var d struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		_ = json.Unmarshal(i.Dialog, &d)
		view.Questions = append(view.Questions, HandedOver{Worker: i.Name, RequestID: d.ID, Title: d.Title, Since: i.DialogAt})
	}
	return view, nil
}

func (s *Server) needsYou(_ context.Context, c caller, r *http.Request) (any, error) {
	if c.agent.Role != agent.RoleLead {
		return nil, errLeadOnly
	}
	if id := strings.TrimSpace(r.URL.Query().Get("id")); id != "" {
		hs, err := s.Yesmen.Handoffs(c.yesman.Name)
		if err != nil {
			return nil, err
		}
		h, ok := hs.Find(id)
		if !ok {
			return nil, fmt.Errorf("no handoff %s", id)
		}
		return h, nil
	}
	return s.needsYouView(c)
}

func (s *Server) changeNeedsYou(_ context.Context, c caller, r *http.Request) (any, error) {
	if c.agent.Role != agent.RoleLead {
		return nil, errLeadOnly
	}
	var req NeedsYouChange
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	n := 0
	for _, set := range []bool{req.Add != nil, req.HandOver != nil, req.Resolve != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return nil, errors.New("give exactly one of add, handOver and resolve")
	}
	var (
		out   any
		added *yesmen.Handoff
	)
	err := s.Yesmen.Use(c.yesman.Name, func(y yesmen.Yesman) error {
		// A lead of an archived yesman cannot write into a new one of the
		// same name.
		if !y.HiredAt.Equal(c.yesman.HiredAt) {
			return errors.New("this yesman was replaced")
		}
		// Nor can a lead that was stopped or restarted after its request
		// authenticated: the change commits only while that same process
		// still holds its credential, and Stop waits for it.
		return s.Agents.CallerOperation(c.token, c.agent, func() error {
			var err error
			out, added, err = s.commitNeedsYou(y, req)
			return err
		})
	})
	if err != nil {
		return nil, err
	}
	if s.NeedsYouChanged != nil {
		s.NeedsYouChanged(c.yesman.Name, added)
	}
	return out, nil
}

// commitNeedsYou makes one change for the lead, inside its operation, and
// returns the result and the handoff it added, if it added one.
func (s *Server) commitNeedsYou(y yesmen.Yesman, req NeedsYouChange) (any, *yesmen.Handoff, error) {
	switch {
	case req.Add != nil:
		// The tool call, never when the lead started, tells a repeat
		// from a new request; see AddHandoff.
		h, created, err := s.Yesmen.AddHandoff(y.Name, yesmen.Handoff{
			Type: req.Add.Type, Title: req.Add.Title, Detail: req.Add.Detail,
			Task: req.Add.Task, Worker: strings.TrimSpace(req.Add.Worker),
			ToolCall: req.ToolCall,
		})
		if err != nil {
			return nil, nil, err
		}
		out := AddedHandoff{Handoff: h}
		if hs, err := s.Yesmen.Handoffs(y.Name); err == nil {
			out.CallMemory = callMemoryNotice(hs)
		}
		if created {
			return out, &h, nil
		}
		return out, nil, nil
	case req.HandOver != nil:
		name := strings.TrimSpace(req.HandOver.Worker)
		if name == yesmen.LeadName || name == "" {
			return nil, nil, errors.New("name the worker whose question to hand over")
		}
		if err := s.Agents.HandOver(agent.ID(y.Name, name), req.HandOver.RequestID); err != nil {
			return nil, nil, err
		}
		return map[string]bool{"handedOver": true}, nil, nil
	default:
		h, err := s.Yesmen.CloseHandoff(y.Name, strings.TrimSpace(req.Resolve.ID), yesmen.HandoffResolved, "lead", req.Resolve.Note)
		if err != nil {
			return nil, nil, err
		}
		return h, nil, nil
	}
}

const needsYouContextLimit = 5

// needsYouContext tells a lead, briefly, what it has left with the user.
func (s *Server) needsYouContext(c caller) string {
	view, err := s.needsYouView(c)
	if err != nil {
		return fmt.Sprintf("\n## Needs you\n\nWhat waits on the user cannot be read: %v\n", err)
	}
	total := len(view.Handoffs) + len(view.Questions)
	if total == 0 && view.CallMemory == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Needs you\n\n")
	if view.CallMemory != nil {
		b.WriteString(callMemoryLine(view.CallMemory) + "\n")
		if total == 0 {
			return b.String()
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%d waiting on the user; see them with yesman_needs_you:\n", total)
	shown := 0
	for _, h := range view.Handoffs {
		if shown == needsYouContextLimit {
			break
		}
		fmt.Fprintf(&b, "- %s (%s): %s\n", h.ID, h.Type, h.Title)
		shown++
	}
	for _, q := range view.Questions {
		if shown == needsYouContextLimit {
			break
		}
		fmt.Fprintf(&b, "- worker %s's question %q: %s\n", q.Worker, q.RequestID, q.Title)
		shown++
	}
	if total > shown {
		fmt.Fprintf(&b, "… and %d more.\n", total-shown)
	}
	return b.String()
}
