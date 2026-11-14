package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

func TestContextDocumentsAndWorktreePoliciesTogether(t *testing.T) {
	f := newCleanupFixture(t)
	y, err := f.s.Yesmen.Rename(f.y.Name, "项目 .28")
	if err != nil {
		t.Fatal(err)
	}
	for path, text := range map[string]string{
		filepath.Join(y.KnowledgeDir(), "INDEX.md"): "# Knowledge\n- [Durable guidance](rules.md) — reference only\n",
		filepath.Join(y.KnowledgeDir(), "rules.md"): "NOTE_BODY_MUST_NOT_BE_INJECTED",
		y.TaskLogPath(): "TASKLOG_BODY_MUST_NOT_BE_INJECTED",
	} {
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, me := range []agent.Info{f.owner.agent, f.lead.agent} {
		text := BuildContext(context.Background(), f.s.Pool, f.s.Agents, y, me, yesmen.Board{})
		for _, want := range []string{"项目 .28", "Knowledge holds reusable", ".yesman/task-log.md", "Durable guidance", "Finishing and recovering worktrees", "lifecycle ownership", "purpose (review, author or release)", "cleanupId", "private metadata nonce", "exact full HEAD", "completion", "same-owner", "checkpoint"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks combined policy %q", me.Role, want)
			}
		}
		for _, body := range []string{"NOTE_BODY_MUST_NOT_BE_INJECTED", "TASKLOG_BODY_MUST_NOT_BE_INJECTED"} {
			if strings.Contains(text, body) {
				t.Errorf("%s injected document body", me.Role)
			}
		}
		if me.Role == agent.RoleLead {
			for _, want := range []string{"one batch cleanup worker", "handoff", "You coordinate and append task-log", "only files you may write are Knowledge notes"} {
				if !strings.Contains(text, want) {
					t.Errorf("lead lacks %q", want)
				}
			}
		} else if !strings.Contains(text, "your lead, which appends the task log") {
			t.Error("worker task-log responsibility changed")
		}
	}
}
