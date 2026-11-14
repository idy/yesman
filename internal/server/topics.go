package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

var errTopicsLeadOnly = errors.New("only the yesman's lead keeps its topics; report what is worth keeping to your lead")

// topics returns the caller's yesman's topics as an index, or one in full
// with ?id=. Agents get the topic's content and where it came from, never the
// app's records of the messages sent from it: their submission IDs, previews
// and states, and fields this version does not know, stay in the app.
func (s *Server) topics(_ context.Context, c caller, r *http.Request) (any, error) {
	if id := r.URL.Query().Get("id"); id != "" {
		t, err := s.Yesmen.Topic(c.yesman.Name, c.yesman.HiredAt, id)
		if err != nil {
			return nil, err
		}
		return agentTopicOf(t), nil
	}
	list, err := s.Yesmen.Topics(c.yesman.Name, c.yesman.HiredAt)
	if err != nil {
		return nil, err
	}
	index := make([]AgentTopicEntry, len(list))
	for i, t := range list {
		index[i] = AgentTopicEntry{ID: t.ID, Title: t.Title, Status: t.Status, Revision: t.Revision, UpdatedAt: t.UpdatedAt, Conclusions: len(t.Conclusions), Questions: len(t.Questions)}
	}
	return map[string]any{"topics": index}, nil
}

// AgentTopicEntry is a topic in an agent's list: enough to choose one to
// read, none of its content.
type AgentTopicEntry struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	Revision    int64     `json:"revision"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Conclusions int       `json:"conclusions"`
	Questions   int       `json:"questions"`
}

// AgentTopic is one topic as an agent reads it.
type AgentTopic struct {
	ID          string             `json:"id"`
	Title       string             `json:"title"`
	Status      string             `json:"status"`
	Summary     string             `json:"summary"`
	Conclusions []yesmen.TopicItem `json:"conclusions"`
	Questions   []yesmen.TopicItem `json:"questions"`
	Sources     []AgentTopicSource `json:"sources"`
	Revision    int64              `json:"revision"`
	CreatedAt   time.Time          `json:"createdAt"`
	UpdatedAt   time.Time          `json:"updatedAt"`
	UpdatedBy   string             `json:"updatedBy,omitempty"`
	// SendCount and LastSentAt say the user sent messages from the topic;
	// the messages themselves are in the lead's conversation.
	SendCount  int        `json:"sendCount"`
	LastSentAt *time.Time `json:"lastSentAt,omitempty"`
}

// AgentTopicSource is a source as an agent reads it. A submission source
// keeps its submission ID in the app: an agent only learns that the user sent
// a message from this topic.
type AgentTopicSource struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Agent     string `json:"agent,omitempty"`
	Role      string `json:"role,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Label     string `json:"label,omitempty"`
}

func agentTopicOf(t yesmen.Topic) AgentTopic {
	out := AgentTopic{
		ID: t.ID, Title: t.Title, Status: t.Status, Summary: t.Summary,
		Conclusions: append([]yesmen.TopicItem{}, t.Conclusions...),
		Questions:   append([]yesmen.TopicItem{}, t.Questions...),
		Sources:     make([]AgentTopicSource, 0, len(t.Sources)),
		Revision:    t.Revision, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, UpdatedBy: t.UpdatedBy,
		SendCount: len(t.Sends),
	}
	if n := len(t.Sends); n > 0 {
		at := t.Sends[n-1].At
		out.LastSentAt = &at
	}
	for _, src := range t.Sources {
		a := AgentTopicSource{ID: src.ID, Kind: src.Kind, Agent: src.Agent, Role: src.Role, Timestamp: src.Timestamp, Label: src.Label}
		switch src.Kind {
		case yesmen.SourceSubmission:
			if a.Label == "" {
				a.Label = "a message the user sent from this topic"
			}
		default:
			a.Ref = src.Ref
		}
		out.Sources = append(out.Sources, a)
	}
	return out
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
	if req.Create != nil && hasSubmissionSource(req.Create.Sources) || req.Patch != nil && hasSubmissionSource(req.Patch.AddSources) {
		return nil, errTopicSubmissionSource
	}
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
	return agentTopicOf(t), nil
}

var errTopicSubmissionSource = errors.New("a submission source is recorded by the app when the user sends from a topic; cite the conversation or a message instead")

func hasSubmissionSource(sources []yesmen.TopicSource) bool {
	for _, src := range sources {
		if strings.EqualFold(strings.TrimSpace(src.Kind), yesmen.SourceSubmission) {
			return true
		}
	}
	return false
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
