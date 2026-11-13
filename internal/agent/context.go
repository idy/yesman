package agent

import (
	"context"
	"encoding/json"
)

// ContextStats is the read-only portion of pi's session statistics. Tokens and
// Cost are cumulative across requests; ContextUsage estimates the current window.
type ContextStats struct {
	UserMessages      int           `json:"userMessages"`
	AssistantMessages int           `json:"assistantMessages"`
	ToolCalls         int           `json:"toolCalls"`
	ToolResults       int           `json:"toolResults"`
	TotalMessages     int           `json:"totalMessages"`
	Tokens            TokenUsage    `json:"tokens"`
	Cost              float64       `json:"cost"`
	ContextUsage      *ContextUsage `json:"contextUsage,omitempty"`
}

// TokenUsage counts tokens cumulatively across the session's requests.
type TokenUsage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
	Total      int `json:"total"`
}

// ContextUsage estimates the current context window. Nil Tokens and Percent
// mean usage is unknown, which differs from a known empty window.
type ContextUsage struct {
	Tokens        *int     `json:"tokens"`
	ContextWindow int      `json:"contextWindow"`
	Percent       *float64 `json:"percent"`
}

// Context starts an agent if needed and reads its session statistics, excluding
// pi's session path and ID.
func (m *Manager) Context(ctx context.Context, id string) (ContextStats, error) {
	_, p, err := m.ensure(id)
	if err != nil {
		return ContextStats{}, err
	}
	r, err := p.call(ctx, map[string]any{"type": "get_session_stats"})
	if err != nil {
		return ContextStats{}, err
	}
	var stats ContextStats
	if err := json.Unmarshal(r.Data, &stats); err != nil {
		return ContextStats{}, err
	}
	return stats, nil
}
