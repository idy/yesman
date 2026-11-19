package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// The browser fixture covers the desktop service and real UI. This test
// covers the actual Pi RPC and extension with a deterministic local provider.
func TestNeedsYouResponsesWithPiFixture(t *testing.T) {
	if os.Getenv("YESMAN_TEST_PI_FIXTURE") == "" {
		t.Skip("set YESMAN_TEST_PI_FIXTURE=1")
	}
	ext := filepath.Join(t.TempDir(), "fixture-dialogs.ts")
	if err := os.WriteFile(ext, []byte(`export default function (pi) {
  pi.registerTool({ name: "fixture_dialogs", label: "Fixture dialogs", description: "Ask three synthetic questions", parameters: { type: "object", properties: {} },
    async execute(_id, _args, _signal, _update, ctx) {
      const value = await ctx.ui.input("Fixture text", "", { timeout: 60000 });
      const option = await ctx.ui.select("Fixture choice", ["alpha", "beta"], { timeout: 60000 });
      const ack = await ctx.ui.confirm("Fixture confirmation", "Did you check?", { timeout: 60000 });
      return { content: [{type: "text", text: "DIALOG-RESULT " + JSON.stringify({ value, option, ack })}] };
    }
  });
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	call := func(id, tool string, arguments map[string]any) []any {
		return []any{
			map[string]any{"type": "toolcall_start", "contentIndex": 0, "id": id, "toolName": tool},
			map[string]any{"type": "toolcall_end", "contentIndex": 0, "toolCall": map[string]any{"type": "toolCall", "id": id, "name": tool, "arguments": arguments}},
		}
	}
	answered := func(body, id string) bool { return strings.Contains(body, `"toolCallId":"`+id+`"`) }
	handover := regexp.MustCompile(`FIXTURE-HANDOVER ([a-zA-Z0-9-]+)`)
	f := startPiFixture(t, func(body string) []any {
		for _, match := range handover.FindAllStringSubmatch(body, -1) {
			id := "hand_" + match[1]
			if !answered(body, id) {
				return call(id, "yesman_needs_you", map[string]any{"handOver": map[string]any{"worker": "w1", "requestId": match[1]}})
			}
		}
		switch {
		case strings.Contains(body, "FIXTURE-ASK-SET") && !answered(body, "fixture_ask"):
			return call("fixture_ask", "fixture_dialogs", map[string]any{})
		case strings.Contains(body, "FIXTURE-ADD-CARD") && !answered(body, "fixture_add"):
			return call("fixture_add", "yesman_needs_you", map[string]any{"add": map[string]any{
				"type": "decision", "title": "Synthetic choice", "task": "t1", "response": map[string]any{"kind": "choice", "options": []string{"alpha", "beta"}, "allowNote": true},
			}})
		case strings.Contains(body, "whose requests are closed (n1 done)") && !answered(body, "fixture_read"):
			return call("fixture_read", "yesman_needs_you", map[string]any{"id": "n1"})
		}
		return []any{map[string]any{"type": "text_start", "contentIndex": 0}, map[string]any{"type": "text_end", "contentIndex": 0, "content": "fixture reply"}}
	}, ext)
	ctx := context.Background()
	y, err := f.store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	lead, worker := "alice/lead", "alice/w1"
	task := "Check the fixture"
	if _, err := f.store.ChangeBoard(y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: &task}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Prompt(ctx, lead, "FIXTURE-ADD-CARD"); err != nil {
		t.Fatal(err)
	}
	f.wait("durable choice registered by the real tool", func() bool {
		hs, err := f.store.Handoffs(y.Name)
		return err == nil && len(hs.Open()) == 1 && hs.Open()[0].Response != nil && hs.Open()[0].Response.AllowNote
	})
	if _, err := f.m.StartWorker(ctx, y.Name, "w1", "Ask fixture dialogs", "FIXTURE-ASK-SET"); err != nil {
		t.Fatal(err)
	}
	value, choice, ack := "汉字 response", "beta", true
	for _, scenario := range []struct {
		method string
		answer agent.DialogReply
	}{
		{"input", agent.DialogReply{Value: &value}},
		{"select", agent.DialogReply{Value: &choice}},
		{"confirm", agent.DialogReply{Confirmed: &ack}},
	} {
		var info agent.Info
		var request struct{ ID, Method string }
		f.wait("live Pi "+scenario.method+" dialog", func() bool {
			info, _ = f.m.Get(worker)
			return info.Status == agent.Waiting && json.Unmarshal(info.Dialog, &request) == nil && request.Method == scenario.method
		})
		scope := agent.DialogScope{HiredAt: y.HiredAt, Since: info.Since, AskedAt: info.DialogAt, HandedOverOnly: true}
		scenario.answer.RequestID = request.ID
		if err := f.m.AnswerWindowDialog(worker, scope, scenario.answer); err == nil {
			t.Fatal("user replied to an unhanded question from Needs You")
		}
		if err := f.m.Prompt(ctx, lead, "FIXTURE-HANDOVER "+request.ID); err != nil {
			t.Fatal(err)
		}
		f.wait("explicit handOver through the real tool", func() bool {
			i, _ := f.m.Get(worker)
			return i.HandedOver && i.DialogAt.Equal(scope.AskedAt)
		})
		if err := f.m.AnswerWindowDialog(worker, scope, scenario.answer); err != nil {
			t.Fatal(err)
		}
		if err := f.m.AnswerWindowDialog(worker, scope, scenario.answer); err == nil {
			t.Fatal("a second click answered the same live question")
		}
	}
	f.wait("all exact replies in the worker's next actual model request", func() bool {
		for _, body := range f.requests() {
			if strings.Contains(body, `DIALOG-RESULT {\"value\":\"汉字 response\",\"option\":\"beta\",\"ack\":true}`) {
				return true
			}
		}
		return false
	})

	// Leave the task waiting across a whole Manager restart. The next lead
	// can recover the stored answer even if the direct close notice was lost.
	status := yesmen.TaskBlocked
	if _, err := f.store.ChangeBoard(y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: &status, Wait: &yesmen.WaitEdit{Kind: yesmen.WaitUser, Handoff: "n1"}}}}); err != nil {
		t.Fatal(err)
	}
	f.m.StopAll()
	answer := &yesmen.HandoffAnswer{Choice: "beta", Note: "SAVED-FIXTURE-NOTE"}
	if _, _, err := f.store.RespondHandoff(y.Name, "n1", yesmen.HandoffDone, answer); err != nil {
		t.Fatal(err)
	}
	f.m = agent.New(f.config)
	f.srv.Agents = f.m
	t.Cleanup(f.m.StopAll)
	f.m.Resume()
	f.wait("the restarted lead reads the exact saved answer after its wait notice", func() bool {
		for _, body := range f.requests() {
			if answered(body, "fixture_read") && strings.Contains(body, "SAVED-FIXTURE-NOTE") && strings.Contains(body, "whose requests are closed (n1 done)") {
				return true
			}
		}
		return false
	})
}
