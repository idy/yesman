package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/yesmen"
)

func TestContextReadsSessionStats(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_BUSY=1")
	id := ID("alice", yesmen.LeadName)
	if m.running(id) != nil {
		t.Fatal("agent already running before Context")
	}
	stats, err := m.Context(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if m.running(id) == nil {
		t.Fatal("Context did not start the agent")
	}
	if stats.UserMessages != 4 || stats.AssistantMessages != 6 || stats.ToolCalls != 3 || stats.ToolResults != 3 || stats.TotalMessages != 13 {
		t.Errorf("message and tool counts = %+v", stats)
	}
	wantTokens := TokenUsage{Input: 600000, Output: 40000, CacheRead: 200000, CacheWrite: 10000, Total: 850000}
	if stats.Tokens != wantTokens || stats.Cost != 1.25 {
		t.Errorf("cumulative usage = %+v, cost = %v", stats.Tokens, stats.Cost)
	}
	usage := stats.ContextUsage
	if usage == nil || usage.Tokens == nil || usage.Percent == nil {
		t.Fatalf("missing current-window usage: %+v", usage)
	}
	if *usage.Tokens != 32000 || usage.ContextWindow != 200000 || *usage.Percent != 16 {
		t.Errorf("current-window usage = %d/%d (%v%%), must not use cumulative totals", *usage.Tokens, usage.ContextWindow, *usage.Percent)
	}

	encoded, err := json.Marshal(stats)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 8 {
		t.Errorf("unexpected fields in context statistics: %s", encoded)
	}
	for _, private := range []string{"sessionFile", "sessionId", "/private/pi/sessions/context-test.jsonl", "private-context-session-id"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Errorf("context statistics leaked %q: %s", private, encoded)
		}
	}
}

func TestContextUsageJSON(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture string
		value   string
	}{
		{name: "unknown", fixture: "unknown", value: "null"},
		{name: "known empty", fixture: "zero", value: "0"},
		{name: "unavailable", fixture: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newTestManager(t, 0, "FAKE_PI_SESSION_STATS="+tc.fixture)
			stats, err := m.Context(context.Background(), ID("alice", yesmen.LeadName))
			if err != nil {
				t.Fatal(err)
			}
			if stats.Tokens.Total != 850000 {
				t.Fatalf("unavailable current usage lost cumulative totals: %+v", stats)
			}
			encoded, err := json.Marshal(stats)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if tc.value == "" {
				if stats.ContextUsage != nil || wire["contextUsage"] != nil {
					t.Fatalf("missing usage became a known window: %s", encoded)
				}
				return
			}
			var usage map[string]json.RawMessage
			if err := json.Unmarshal(wire["contextUsage"], &usage); err != nil {
				t.Fatal(err)
			}
			if string(usage["tokens"]) != tc.value || string(usage["percent"]) != tc.value || string(usage["contextWindow"]) != "200000" {
				t.Fatalf("null and zero usage were not preserved: %s", encoded)
			}
			var roundTrip ContextStats
			if err := json.Unmarshal(encoded, &roundTrip); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stats, roundTrip) {
				t.Errorf("statistics changed after JSON round trip: %+v -> %+v", stats, roundTrip)
			}
		})
	}
}

func TestContextErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      string
		fixture string
		want    string
	}{
		{name: "invalid agent ID", id: "invalid", want: "not an agent ID"},
		{name: "unknown agent", id: "alice/missing", want: "no worker named missing"},
		{name: "RPC rejected", id: "alice/lead", fixture: "error", want: "pi get_session_stats: stats unavailable"},
		{name: "invalid statistics", id: "alice/lead", fixture: "invalid", want: "cannot unmarshal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newTestManager(t, 0, "FAKE_PI_SESSION_STATS="+tc.fixture)
			stats, err := m.Context(context.Background(), tc.id)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Context error = %v, want %q", err, tc.want)
			}
			if !reflect.DeepEqual(stats, ContextStats{}) {
				t.Errorf("failed Context returned partial statistics: %+v", stats)
			}
		})
	}
}
