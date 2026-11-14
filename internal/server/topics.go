package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

var errTopicsLeadOnly = errors.New("only the yesman's lead keeps its topics; report what is worth keeping to your lead")

// topics returns the caller's yesman's topics, or one with ?id=.
func (s *Server) topics(_ context.Context, c caller, r *http.Request) (any, error) {
	if id := r.URL.Query().Get("id"); id != "" {
		return s.Yesmen.Topic(c.yesman.Name, c.yesman.HiredAt, id)
	}
	list, err := s.Yesmen.Topics(c.yesman.Name, c.yesman.HiredAt)
	if err != nil {
		return nil, err
	}
	return map[string]any{"topics": list}, nil
}

// TopicChange is one change the lead makes to its topics.
type TopicChange struct {
	Create *yesmen.TopicInput `json:"create,omitempty"`
	ID     string             `json:"id,omitempty"`
	Patch  *yesmen.TopicPatch `json:"patch,omitempty"`
}

// changeTopics creates or patches a topic for the lead. Only the lead writes
// topics through the API; workers report to it, and the user edits them in
// the app. The workspace is always the caller's own.
func (s *Server) changeTopics(_ context.Context, c caller, r *http.Request) (any, error) {
	if c.agent.Role != agent.RoleLead {
		return nil, errTopicsLeadOnly
	}
	var req TopicChange
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	var (
		t   yesmen.Topic
		err error
	)
	switch {
	case req.Create != nil && req.Patch == nil && req.ID == "":
		in := *req.Create
		in.Sources = s.resolveSources(c, in.Sources)
		t, err = s.Yesmen.CreateTopic(c.yesman.Name, c.yesman.HiredAt, yesmen.TopicByLead, in)
	case req.Patch != nil && req.Create == nil && req.ID != "":
		p := *req.Patch
		p.AddSources = s.resolveSources(c, p.AddSources)
		t, err = s.Yesmen.PatchTopic(c.yesman.Name, c.yesman.HiredAt, yesmen.TopicByLead, req.ID, p)
	default:
		return nil, errors.New("give create, or id with patch")
	}
	if err != nil {
		return nil, err
	}
	if s.TopicsChanged != nil {
		s.TopicsChanged(c.yesman.Name)
	}
	return t, nil
}

// resolveSources ties cited agents to the generation they have now, so a
// later agent of the same name is not taken for the one cited.
func (s *Server) resolveSources(c caller, sources []yesmen.TopicSource) []yesmen.TopicSource {
	out := make([]yesmen.TopicSource, len(sources))
	for i, src := range sources {
		kind := strings.ToLower(strings.TrimSpace(src.Kind))
		name := strings.TrimSpace(src.Agent)
		if name == "" && (kind == yesmen.SourceConversation || kind == yesmen.SourceMessage) {
			name = yesmen.LeadName
		}
		if src.Since.IsZero() && name != "" && (kind == yesmen.SourceWorker || kind == yesmen.SourceConversation || kind == yesmen.SourceMessage) {
			if info, err := s.Agents.Get(agent.ID(c.yesman.Name, name)); err == nil {
				src.Since = info.Since
			}
		}
		out[i] = src
	}
	return out
}

const topicsContextLimit = 20

// TopicsContext tells a lead what topics are for and lists the open ones by
// title only; their content is read with yesman_topics when it matters.
func TopicsContext(topics []yesmen.Topic, err error) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("\n## Topics\n\n")
	w("Topics keep the user's questions and discussions worth finding again, on the Topics page beside the board: a short summary of what was asked or discussed, the conclusions the user confirmed, the questions still open, and where they came from. Keep them with yesman_topics; the user also edits them in the app.\n")
	w("- Record a topic only for a reusable explanation, an important conclusion of a discussion, or an open question the user will come back to. Process notifications, worker reports, tool results and routine progress are not topics.\n")
	w("- A conclusion is what the user agreed or decided. A suggestion they have not agreed to goes under open questions or the summary, never under conclusions.\n")
	w("- Worker reports can be cited as sources (kind worker); they are not the user's questions.\n")
	w("- Task status stays on the board, task history in the task log and durable rules in Knowledge: cite them (kind task, knowledge) instead of copying them into a topic.\n")
	w("- A user message beginning with a line like \"[Topic tp3: title]\" was sent from that topic's page. Answer it in the conversation as usual; when the exchange settles something or raises a question worth keeping, update that topic.\n")
	w("- Every change needs the topic's current revision. If it changed meanwhile (the user may be editing it), read it again and apply your change to the new version; never restore an older version over it.\n")
	w("- Do not reconstruct topics from old conversations in bulk: add them as discussions happen.\n")
	if err != nil {
		w("\nThe topics cannot be read now: %s\n", oneLine(err.Error(), 200))
		return b.String()
	}
	open, resolved, archived := 0, 0, 0
	for _, t := range topics {
		switch t.Status {
		case yesmen.TopicOpen:
			if open++; open > topicsContextLimit {
				continue
			}
			if open == 1 {
				w("\nOpen topics:\n")
			}
			w("- %s %s", t.ID, oneLine(t.Title, 120))
			if n := len(t.Questions); n > 0 {
				w(" (%d open question%s)", n, plural(n))
			}
			w("\n")
		case yesmen.TopicResolved:
			resolved++
		default:
			archived++
		}
	}
	switch {
	case open == 0 && resolved == 0 && archived == 0:
		w("\nNo topics yet.\n")
	case open == 0:
		w("\nNo open topics.\n")
	case open > topicsContextLimit:
		w("… and %d more open topics (yesman_topics lists them).\n", open-topicsContextLimit)
	}
	if resolved+archived > 0 {
		w("%d resolved and %d archived (yesman_topics lists them).\n", resolved, archived)
	}
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
