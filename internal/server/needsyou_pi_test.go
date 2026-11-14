package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// TestNeedsYouForgottenCallWithPiFixture runs the installed pi with yesman's
// extension and a local backend. A history trimmed by real closes forgets
// call-0 from what it keeps as it is; a new pi process that sends call-0
// again is refused, the file untouched, while a new call registers once.
// Once the tool-call memory is full, a new call is refused, saying so, until
// the user starts a new memory; then it registers:
//
//	YESMAN_TEST_PI_FIXTURE=1 go test ./internal/server -run NeedsYouForgottenCallWithPiFixture -v
func TestNeedsYouForgottenCallWithPiFixture(t *testing.T) {
	call := func(id, title string) []any {
		c := map[string]any{"type": "toolCall", "id": id, "name": "yesman_needs_you",
			"arguments": map[string]any{"add": map[string]any{"type": "manual", "title": title}}}
		return []any{
			map[string]any{"type": "toolcall_start", "contentIndex": 0, "id": id, "toolName": "yesman_needs_you"},
			map[string]any{"type": "toolcall_end", "contentIndex": 0, "toolCall": c},
		}
	}
	answered := func(body, id string) int { return strings.Count(body, `"toolCallId":"`+id+`"`) }
	f := startPiFixture(t, func(body string) []any {
		switch {
		case strings.Contains(body, "SAME-REQUEST") && answered(body, "toolu_fresh") == 1:
			return call("toolu_fresh", "log in")
		case strings.Contains(body, "NEW-REQUEST") && answered(body, "toolu_fresh") == 0:
			return call("toolu_fresh", "log in")
		case strings.Contains(body, "REPLAY-FORGOTTEN") && answered(body, "call-0") == 0:
			return call("call-0", "original completed step")
		case strings.Contains(body, "WHEN-FULL") && answered(body, "toolu_full") == 0:
			return call("toolu_full", "install the CLI")
		case strings.Contains(body, "AFTER-RESET") && answered(body, "toolu_full") == 1:
			return call("toolu_full", "install the CLI")
		}
		return []any{map[string]any{"type": "text_start", "contentIndex": 0}, map[string]any{"type": "text_end", "contentIndex": 0, "content": "fixture reply"}}
	})
	y, err := f.store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	// keptCalls (1000) + keptClosed (30) + 1 closed handoffs and one open:
	// closing it forgets call-0 and call-1 but for their fingerprint.
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	var items []map[string]any
	for i := range 1031 {
		at := base.Add(time.Duration(i) * time.Minute)
		items = append(items, map[string]any{"id": fmt.Sprintf("n%d", i+1), "type": "manual", "title": "step",
			"toolCall": fmt.Sprintf("call-%d", i), "createdAt": at, "closedAt": at.Add(time.Second), "closedBy": "user", "resolution": "done"})
	}
	items = append(items, map[string]any{"id": "n1032", "type": "manual", "title": "last", "toolCall": "last", "createdAt": base.Add(48 * time.Hour)})
	raw, _ := json.Marshal(map[string]any{"items": items, "next": 1032})
	if err := os.WriteFile(y.HandoffsFile(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CloseHandoff("alice", "n1032", yesmen.HandoffDone, "user", ""); err != nil {
		t.Fatal(err)
	}
	trimmed, _ := os.ReadFile(y.HandoffsFile())
	if bytes.Contains(trimmed, []byte(`"call-0"`)) || !bytes.Contains(trimmed, []byte(`"call-2"`)) {
		t.Fatal("not trimmed as expected")
	}

	lead := agent.ID("alice", yesmen.LeadName)
	turn := 0
	ask := func(text string) {
		t.Helper()
		turn++
		sid := fmt.Sprintf("%012x%036x", time.Now().UnixMilli(), turn)
		if _, err := f.m.Submit(context.Background(), lead, sid, text, nil, y.HiredAt); err != nil {
			t.Fatal(err)
		}
		f.wait(text, func() bool { return f.settled(lead) >= turn })
	}
	result := func(id string) []piMessage {
		var out []piMessage
		for _, m := range f.messages(lead) {
			if m.Role == "toolResult" && m.ToolCallID == id {
				out = append(out, m)
			}
		}
		return out
	}
	pending := func() []yesmen.Handoff {
		hs, err := f.store.Handoffs("alice")
		if err != nil {
			t.Fatal(err)
		}
		return hs.Open()
	}

	ask("REPLAY-FORGOTTEN the original call")
	if r := result("call-0"); len(r) != 1 || !r[0].IsError || !strings.Contains(string(r[0].Content), "long ago") {
		t.Fatalf("the forgotten call's result: %+v", r)
	}
	if after, _ := os.ReadFile(y.HandoffsFile()); !bytes.Equal(after, trimmed) || len(pending()) != 0 {
		t.Fatalf("the forgotten call changed the handoffs: %d open", len(pending()))
	}

	ask("NEW-REQUEST please")
	open := pending()
	if r := result("toolu_fresh"); len(r) != 1 || r[0].IsError || len(open) != 1 || open[0].ToolCall != "toolu_fresh" {
		t.Fatalf("a new call: %+v, %+v", r, open)
	}
	ask("SAME-REQUEST again")
	if again := pending(); len(again) != 1 || again[0].ID != open[0].ID {
		t.Fatalf("the same call registered another: %+v", again)
	}
	if r := result("toolu_fresh"); len(r) != 2 || r[1].IsError || !strings.Contains(string(r[1].Content), open[0].ID) {
		t.Fatalf("the same call's result: %+v", r)
	}

	// Full: as many fingerprints as leave no room for another call.
	hs, err := f.store.Handoffs("alice")
	if err != nil {
		t.Fatal(err)
	}
	m := hs.CallMemory()
	var file map[string]json.RawMessage
	current, _ := os.ReadFile(y.HandoffsFile())
	if err := json.Unmarshal(current, &file); err != nil {
		t.Fatal(err)
	}
	file["callMemory"] = json.RawMessage(fingerprintsJSON(m.Capacity - (m.Used - m.Forgettable)))
	full, _ := json.Marshal(file)
	if err := os.WriteFile(y.HandoffsFile(), full, 0o600); err != nil {
		t.Fatal(err)
	}
	if hs, _ := f.store.Handoffs("alice"); hs.CallMemory().State != "full" {
		t.Fatalf("not full: %+v", hs.CallMemory())
	}
	ask("WHEN-FULL a new request")
	if r := result("toolu_full"); len(r) != 1 || !r[0].IsError || !strings.Contains(string(r[0].Content), "memory is full") || strings.Contains(string(r[0].Content), "call again") {
		t.Fatalf("a new call when full: %+v", r)
	}
	if after, _ := os.ReadFile(y.HandoffsFile()); !bytes.Equal(after, full) {
		t.Fatal("refusing changed the file")
	}
	hs, _ = f.store.Handoffs("alice")
	m = hs.CallMemory()
	if _, _, err := f.store.ResetCallMemory("alice", y.HiredAt, m.Epoch, m.Used, m.Forgettable); err != nil {
		t.Fatal(err)
	}
	ask("AFTER-RESET the same request")
	if r := result("toolu_full"); len(r) != 2 || r[1].IsError || !strings.Contains(string(r[1].Content), "Registered") {
		t.Fatalf("the call after the reset: %+v", r)
	}
	if open := pending(); len(open) != 2 || open[1].ToolCall != "toolu_full" {
		t.Fatalf("after the reset: %+v", open)
	}
}
