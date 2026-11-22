package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/crew"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

const projectBriefBytes = 5400

type projectBriefStatus struct {
	boardUnavailable, topicsUnavailable bool
	environment, needsYou               string
}

func projectPoolPath(p *pool.Pool) string {
	if p == nil {
		return "the pool"
	}
	return p.Root()
}

func (s *Server) projectPrompt(w http.ResponseWriter, r *http.Request) {
	token, actor, y, ok := s.projectActor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, agent.ErrRetired)
		return
	}
	board, boardErr := projectBoard(y)
	catalog := projectCatalog{server: s, workspace: y}
	var topics []yesmen.Topic
	var routing *crew.Routing
	lead := actor.Info.Role == agent.RoleLead
	status := projectBriefStatus{boardUnavailable: boardErr != nil, environment: s.envContext(lead)}
	linkedTopic := topicFromInput(actor.InputContext)
	if lead {
		var err error
		topics, err = s.Yesmen.Topics(y.Name, y.HiredAt)
		status.topicsUnavailable = err != nil
		var priority []string
		for _, t := range linkedTopicTasks(board, topics, linkedTopic) {
			if t.Wait != nil && t.Wait.Handoff != "" {
				priority = append(priority, t.Wait.Handoff)
			}
		}
		if t, ok := linkedScheduledTask(board, actor); ok && t.Wait != nil && t.Wait.Handoff != "" {
			priority = append(priority, t.Wait.Handoff)
		}
		status.needsYou = s.needsYouContext(caller{agent: actor.Info, yesman: y, token: token}, priority...)
		if s.Routing != nil {
			r := s.Routing(r.Context(), nil)
			routing = &r
		}
	}
	brief := buildProjectBrief(r.Context(), catalog, actor, board, topics, routing, linkedTopic, status)
	text := projectRules(s.Pool, y, actor.Info) + brief
	if s.GitHubApp != nil {
		text += s.GitHubApp.Context(y.Name, y.HiredAt.UTC().Format(time.RFC3339Nano))
	}
	if !s.deliverProject(w, r, token, actor, "text/plain; charset=utf-8", []byte(text)) && r.Context().Err() == nil {
		writeError(w, http.StatusUnauthorized, agent.ErrRetired)
	}
}

func topicFromInput(inputContext string) string {
	if id, ok := strings.CutPrefix(inputContext, "topic:"); ok && projectTopicID.MatchString(id) {
		return id
	}
	return ""
}

func buildProjectBrief(ctx context.Context, c projectCatalog, actor agent.ContextActor, board yesmen.Board, topics []yesmen.Topic, routing *crew.Routing, linkedTopic string, status projectBriefStatus) string {
	var b strings.Builder
	b.WriteString("\n## Current project brief\n\n")
	write := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		if b.Len()+len(line) <= projectBriefBytes {
			b.WriteString(line)
		}
	}
	if status.boardUnavailable {
		write("Board unavailable. Use yesman_board to verify task status.\n")
	}
	if status.topicsUnavailable {
		write("Topics unavailable. Use yesman_topics to verify the Topic list.\n")
	}
	write("%s%s\n", status.environment, status.needsYou)
	ref := func(i projectItem) string { return contextToken("pc1.", i.locator(contextScope(c.workspace), 0, 0)) }
	quote := func(s string, bytes int) string { return briefQuote(s, bytes) }
	showTask := func(t yesmen.Task, primary bool) {
		write("Linked Board task %s [%s] %s\n", t.ID, quote(t.Status, 50), quote(t.Title, 180))
		if w := t.Wait; w != nil {
			write("Wait %s, tasks %s, handoff %s, blocker %s\n", quote(w.Kind, 40), quote(strings.Join(w.Tasks, ", "), 100), quote(w.Handoff, 40), quote(w.Reason, 380))
		}
		if primary {
			if t.Note != "" {
				write("Current note %s\n", quote(t.Note, 220))
			}
			write("Board ref %s\n", ref(boardProjectItem(t)))
		}
	}
	if actor.Info.Role == agent.RoleWorker {
		w := actor.Worker
		d := w.Dispatch
		if w.Title != "" {
			write("Assignment %s\n", quote(w.Title, 320))
		}
		if len(contextJSON(d)) <= dispatchBriefBytes {
			for _, f := range [][2]string{{"Goal", d.Goal}, {"Constraints", d.Constraints}, {"Deliverables", d.Deliverables}} {
				if f[1] != "" {
					write("%s %s\n", f[0], contextJSON(f[1]))
				}
			}
		}
		if t, ok := workerBoardLink(actor, board); ok {
			showTask(t, true)
		} else if d.BoardTask != "" && !status.boardUnavailable {
			write("Linked Board task %s is missing or its owner changed. Verify with yesman_board.\n", d.BoardTask)
		}
		if len(contextJSON(d)) <= dispatchBriefBytes {
			for _, src := range d.Sources {
				write("Source %s\n", contextJSON(src))
			}
		}
		if w.Task != "" && d.Goal == "" {
			write("Lead's dispatch %s\n", quote(w.Task, min(3200, projectBriefBytes-b.Len()-180)))
			write("Use the original dispatch in the conversation and read the linked Board detail for any missing constraints.\n")
		}
		return b.String()
	}
	linked := false
	for _, topic := range topics {
		if topic.ID != linkedTopic {
			continue
		}
		linked = true
		write("Linked Topic %s [%s] %s (revision %d)\n", topic.ID, topic.Status, quote(topic.Title, 240), topic.Revision)
		item := topicProjectItem(topic)
		field := max(0, len(topic.Conclusions)-1)
		write("Topic ref %s. Use yesman_topics to verify the full set of conclusions and sources.\n", contextToken("pc1.", item.locator(contextScope(c.workspace), field, 0)))
		for i, task := range linkedTopicTasks(board, topics, linkedTopic) {
			showTask(task, i == 0)
		}
		// The item has no per-conclusion edit timestamps. Favor the last
		// listed conclusions and provide the full Topic for the others.
		for i := len(topic.Conclusions) - 1; i >= max(0, len(topic.Conclusions)-3); i-- {
			conclusion := topic.Conclusions[i]
			write("Confirmed conclusion %s %s\n", conclusion.ID, quote(conclusion.Text, 275))
		}
		if topic.Summary != "" {
			write("Interim summary %s\n", quote(topic.Summary, 300))
		}
		if len(topic.Questions) != 0 {
			write("Open question %s\n", quote(topic.Questions[0].Text, 190))
		}
		// Resolve one directly cited note only when needed. Paths are
		// checked by the same source allowlist as context_read.
		for _, source := range topic.Sources {
			if ctx.Err() != nil || source.Kind != yesmen.SourceKnowledge {
				continue
			}
			c.visit(ctx, "knowledge", source.Ref, func(item projectItem) {
				write("Linked Knowledge %s ref %s\n", quote(item.Title, 140), ref(item))
			})
			break
		}
		break
	}
	if linkedTopic != "" && !linked {
		write("Linked Topic %s is unavailable.\n", linkedTopic)
		linked = true
	}
	if actor.Plan != "" {
		linked = true
		write("Linked scheduled plan %s\n", quote(actor.Plan, 40))
		if task, ok := linkedScheduledTask(board, actor); ok {
			showTask(task, true)
		}
	}
	if !linked {
		write("Open items (use yesman_board or yesman_topics for more)\n")
		remaining := 5
		for _, t := range board.Tasks {
			if t.Open() && remaining > 0 {
				write("- %s %s\n", t.ID, quote(t.Title, 220))
				remaining--
			}
		}
		for _, t := range topics {
			if t.Status == yesmen.TopicOpen && remaining > 0 {
				write("- %s %s\n", t.ID, quote(t.Title, 220))
				remaining--
			}
		}
		if remaining == 5 && !status.boardUnavailable && !status.topicsUnavailable {
			write("No open items in this brief.\n")
		}
	}
	if routing != nil {
		write("\n## Crew\n\nQuoted guidance is classification and fit preference.\n")
		if routing.CatalogUnknown {
			write("Catalog availability is unknown. Pi checks saved candidates when the worker starts.\n")
		}
		for _, label := range crew.Labels {
			p := routing.Pools[label.ID]
			guidance := label.Guidance
			if p.Instructions != "" && p.Validate() == nil {
				guidance = p.Instructions
			}
			write("- %s %s\n", label.ID, quote(guidance, 300))
		}
		for _, label := range crew.Labels {
			members := routing.Candidates(label.ID)
			for _, member := range members {
				metrics := ""
				if score := member.Score; score != nil {
					if !score.IntelligenceMissing {
						metrics += fmt.Sprintf(" intelligence %.3g", score.Intelligence)
					}
					if score.Coding != nil {
						metrics += fmt.Sprintf(" coding %.4g%%", *score.Coding)
						switch member.FieldSource("coding") {
						case crew.SourcePatch:
							metrics += " (user CSV)"
						case crew.SourceUser:
							metrics += " (user estimate)"
						}
					}
				}
				write("- %s %s%s\n", quote(member.Key, 190), label.ID, metrics)
			}
		}
	}
	return b.String()
}

func linkedTopicTasks(board yesmen.Board, topics []yesmen.Topic, topicID string) []yesmen.Task {
	if topicID == "" {
		return nil
	}
	index := slices.IndexFunc(topics, func(t yesmen.Topic) bool { return t.ID == topicID })
	if index < 0 {
		return nil
	}
	var cited []yesmen.Task
	seen := make(map[string]bool)
	for _, source := range topics[index].Sources {
		if source.Kind != yesmen.SourceTask || seen[source.Ref] {
			continue
		}
		seen[source.Ref] = true
		if i := slices.IndexFunc(board.Tasks, func(t yesmen.Task) bool { return t.ID == source.Ref }); i >= 0 {
			cited = append(cited, board.Tasks[i])
		}
	}
	slices.SortStableFunc(cited, func(a, b yesmen.Task) int { return topicTaskPriority(a) - topicTaskPriority(b) })
	if len(cited) == 0 {
		return nil
	}
	if !cited[0].Open() {
		return cited[:1]
	}
	if len(cited) == 1 || !cited[1].Open() {
		return cited[:1]
	}
	return cited[:2]
}

func topicTaskPriority(t yesmen.Task) int {
	switch t.Status {
	case yesmen.TaskBlocked:
		return 0
	case yesmen.TaskDoing:
		return 1
	case yesmen.TaskTodo:
		return 2
	default:
		return 3
	}
}

func linkedScheduledTask(board yesmen.Board, actor agent.ContextActor) (yesmen.Task, bool) {
	if actor.Plan != "" {
		for _, t := range board.Tasks {
			if t.Schedule != nil && t.Schedule.Plan == actor.Plan && t.Schedule.Run == actor.Run {
				return t, true
			}
		}
	}
	return yesmen.Task{}, false
}

func workerBoardLink(actor agent.ContextActor, b yesmen.Board) (yesmen.Task, bool) {
	var implicit yesmen.Task
	count := 0
	for _, t := range b.Tasks {
		owned := t.Owner == actor.Info.Name && !t.OwnerSince.IsZero() && t.OwnerSince.Equal(actor.Info.Since)
		if actor.Worker.Dispatch.BoardTask == t.ID && (owned || t.Owner == "" || t.Owner == actor.Info.Name && t.OwnerSince.IsZero()) {
			return t, true
		}
		if owned && t.Open() {
			implicit = t
			count++
		}
	}
	return implicit, actor.Worker.Dispatch.BoardTask == "" && count == 1
}

func briefQuote(text string, maxBytes int) string {
	if maxBytes < 8 {
		return `"…"`
	}
	if len(text) > maxBytes {
		text = text[:maxBytes]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		text += "…"
	}
	runes := []rune(text)
	for n := len(runes); n >= 0; n-- {
		s := string(runes[:n])
		if n < len(runes) {
			s += "…"
		}
		quoted := contextJSON(s)
		if len(quoted) <= maxBytes {
			return string(quoted)
		}
	}
	return `"…"`
}
