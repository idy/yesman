package agent

import (
	"cmp"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// follow keeps an agent's Activity and RunStartedAt from the events of its
// run. Activity names only what the events show: the tool running, the
// model thinking or writing, a compaction or a retry. It runs on the
// process's reader, like record.
func (m *Manager) follow(a *agent, p *proc, kind string, raw json.RawMessage) {
	m.mu.Lock()
	if p != nil && a.proc != p {
		m.mu.Unlock()
		return
	}
	before, started := a.info.Activity, a.info.RunStartedAt
	switch kind {
	case "agent_start":
		if a.info.RunStartedAt.IsZero() {
			a.info.RunStartedAt = time.Now()
		}
		if a.info.Activity == "" {
			a.info.Activity = waitingForModel
		}
	case "agent_settled":
		a.endRun()
	case "message_start":
		var e struct {
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Message.Role == "assistant" && len(a.tools) == 0 {
			a.info.Activity = waitingForModel
		}
	case "message_update":
		var e struct {
			Event struct {
				Type     string `json:"type"`
				ToolName string `json:"toolName"`
			} `json:"assistantMessageEvent"`
		}
		if json.Unmarshal(raw, &e) == nil && len(a.tools) == 0 {
			switch e.Event.Type {
			case "thinking_start":
				a.info.Activity = "Thinking"
			case "text_start":
				a.info.Activity = "Writing"
			case "toolcall_start":
				a.info.Activity = describeTool(e.Event.ToolName, nil, a.dir)
			}
		}
	case "tool_execution_start":
		var e struct {
			ID   string          `json:"toolCallId"`
			Name string          `json:"toolName"`
			Args json.RawMessage `json:"args"`
		}
		if json.Unmarshal(raw, &e) == nil && e.ID != "" {
			if a.labels == nil {
				a.labels = map[string]string{}
			}
			if _, ok := a.labels[e.ID]; !ok {
				a.tools = append(a.tools, e.ID)
			}
			a.labels[e.ID] = describeTool(e.Name, e.Args, a.dir)
			a.info.Activity = a.toolActivity()
		}
	case "tool_execution_end":
		var e struct {
			ID string `json:"toolCallId"`
		}
		if json.Unmarshal(raw, &e) == nil {
			a.tools = slices.DeleteFunc(a.tools, func(id string) bool { return id == e.ID })
			delete(a.labels, e.ID)
			// With its tools done, a run asks the model what next; the
			// run's end clears this if there is no next.
			a.info.Activity = cmp.Or(a.toolActivity(), waitingForModel)
		}
	case "compaction_start", "auto_compaction_start":
		a.info.Activity = "Summarizing earlier conversation"
	case "compaction_end", "auto_compaction_end":
		a.info.Activity = cmp.Or(a.toolActivity(), waitingForModel)
	case "auto_retry_start":
		a.info.Activity = "Retrying after an error"
	}
	changed := a.info.Activity != before || !a.info.RunStartedAt.Equal(started)
	m.mu.Unlock()
	if changed {
		m.changed()
	}
}

const waitingForModel = "Waiting for the model"

// endRun forgets what the run was doing, once it is over.
func (a *agent) endRun() {
	a.info.Activity, a.info.RunStartedAt = "", time.Time{}
	a.tools, a.labels = nil, nil
}

// toolActivity describes the tool calls running: the latest, and how many
// others run beside it.
func (a *agent) toolActivity() string {
	if len(a.tools) == 0 {
		return ""
	}
	s := a.labels[a.tools[len(a.tools)-1]]
	if n := len(a.tools) - 1; n > 0 {
		s += fmt.Sprintf(" (+%d)", n)
	}
	return s
}

const activityLimit = 120

// describeTool says what a tool call does, from its name and arguments,
// such as "Reading web/app.js" or "Running go test ./...". Without arguments,
// while the model is still writing the call, it names the action alone.
func describeTool(name string, raw json.RawMessage, dir string) string {
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
	path := func() string {
		p := str("path", "file_path")
		if p != "" && dir != "" && filepath.IsAbs(p) {
			if rel, err := filepath.Rel(dir, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				p = rel
			}
		}
		return p
	}
	with := func(verb, object string) string {
		if object == "" {
			return verb
		}
		return clip(verb + " " + firstLine(object))
	}
	switch name {
	case "read":
		return with("Reading", path())
	case "edit":
		return with("Editing", path())
	case "write":
		return with("Writing", path())
	case "bash":
		return with("Running", str("command"))
	case "grep":
		return with("Searching for", str("pattern"))
	case "find":
		return with("Finding", str("pattern"))
	case "ls":
		return with("Listing", path())
	case "yesman_worker_start":
		return with("Starting worker", str("name"))
	case "yesman_worker_send":
		return with("Messaging", str("name"))
	case "yesman_worker_remove":
		return with("Removing worker", str("name"))
	case "yesman_checkout":
		return with("Checking out", str("repo"))
	case "yesman_worktree_remove":
		return with("Removing worktree", str("dir"))
	case "yesman_repo_add":
		return with("Adding to the pool", str("repo"))
	case "yesman_repos":
		return "Listing the repo pool"
	case "yesman_agents":
		return "Checking on the agents"
	case "yesman_board":
		if raw != nil && len(args) == 0 {
			return "Reading the board"
		}
		return "Updating the board"
	case "":
		return "Using a tool"
	}
	return with("Using", name)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	return s
}

func clip(s string) string {
	if utf8.RuneCountInString(s) <= activityLimit {
		return s
	}
	r := []rune(s)
	return string(r[:activityLimit-1]) + "…"
}
