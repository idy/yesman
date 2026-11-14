package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

func TestContextBoardAndRoles(t *testing.T) {
	tmp := t.TempDir()
	store := yesmen.New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	y, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	title := strings.Repeat("修", 250)
	s := func(v string) *string { return &v }
	board, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{
		{Title: s("Fix the login timeout"), Status: s("blocked"), Owner: s("e2e"), Note: s("waiting for\ncredentials")},
		{Title: s("Update the install notes"), Status: s("done")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: s(title)}}}); err == nil {
		t.Error("a title over 200 characters was accepted")
	}
	p := pool.New(filepath.Join(tmp, "repos"))
	m := agent.New(agent.Config{Yesmen: store})
	ctx := context.Background()

	lead := BuildContext(ctx, p, m, y, agent.Info{ID: "alice/lead", Name: "lead", Role: agent.RoleLead}, board)
	for _, want := range []string{
		"never do the work yourself",
		"You have no shell",
		"- t1 [blocked] Fix the login timeout (owner e2e): waiting for credentials\n",
		"1 done or dropped",
		"set its owner when you hand it to a worker",
		"what you take on, you see through to the end",
		"yesman_worker_answer",
		"put the open decisions to the user together",
	} {
		if !strings.Contains(lead, want) {
			t.Errorf("the lead's context lacks %q:\n%s", want, lead)
		}
	}
	if strings.Contains(lead, "Update the install notes") {
		t.Error("the context lists a done task")
	}

	worker := BuildContext(ctx, p, m, y, agent.Info{ID: "alice/e2e", Name: "e2e", Role: agent.RoleWorker, Task: "Fix it"}, board)
	if !strings.Contains(worker, `Keep the tasks you own (owner "e2e") current`) || !strings.Contains(worker, "Do not stop to ask") || strings.Contains(worker, "You have no shell") {
		t.Errorf("worker context:\n%s", worker)
	}

	empty := BuildContext(ctx, p, m, y, agent.Info{ID: "alice/lead", Name: "lead", Role: agent.RoleLead}, yesmen.Board{})
	if !strings.Contains(empty, "The board is empty.") {
		t.Errorf("empty board:\n%s", empty)
	}
}

func TestContextSharedDocumentsAreReadOnDemand(t *testing.T) {
	tmp := t.TempDir()
	store := yesmen.New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	y, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	y.DisplayName = "Renamed project 工程"
	index := "# Knowledge\n- [Icon policy](icons.md) — when adding icons\n"
	for path, text := range map[string]string{
		filepath.Join(y.KnowledgeDir(), "INDEX.md"): index,
		filepath.Join(y.KnowledgeDir(), "icons.md"): "NOTE_BODY_SENTINEL: use local fonts",
		y.TaskLogPath(): strings.Repeat("LOG_BODY_SENTINEL: old PR/CI/release history\n", 10000),
	} {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := pool.New(filepath.Join(tmp, "repos"))
	m := agent.New(agent.Config{Yesmen: store})
	defer m.StopAll()
	for _, role := range []string{agent.RoleLead, agent.RoleWorker} {
		t.Run(role, func(t *testing.T) {
			text := BuildContext(context.Background(), p, m, y, agent.Info{ID: "alice/" + role, Name: role, Role: role}, yesmen.Board{})
			for _, want := range []string{
				`named "Renamed project 工程"`,
				".yesman/knowledge/", ".yesman/task-log.md", "Icon policy",
				"Phosphor icon helper and local font assets", "head abc123 passed CI",
				"Finishing a task alone is not a reason to add Knowledge",
				"historical facts, not current instructions", "Read relevant entries on demand",
				"release timelines", "temporary blockers", "Use the board for current status",
			} {
				if !strings.Contains(text, want) {
					t.Errorf("%s context lacks %q", role, want)
				}
			}
			for _, unwanted := range []string{"LOG_BODY_SENTINEL", "NOTE_BODY_SENTINEL", "the only files you may write are knowledge notes"} {
				if strings.Contains(text, unwanted) {
					t.Errorf("%s context contains %q", role, unwanted)
				}
			}
			if len(text) > 20000 {
				t.Fatalf("history inflated context: %d bytes", len(text))
			}
			if role == agent.RoleLead {
				for _, want := range []string{"only files you may write are Knowledge notes", "coordinate and append task-log entries from worker reports", "never do the work yourself", "does not permit doing code work yourself"} {
					if !strings.Contains(text, want) {
						t.Errorf("lead context lacks %q", want)
					}
				}
			} else if !strings.Contains(text, "Report task results, PR/commit links") || !strings.Contains(text, "do not rewrite the shared task log") || !strings.Contains(text, "Report reusable discoveries to your lead") {
				t.Error("worker has incorrect document responsibilities")
			}
		})
	}
	if got, err := os.ReadFile(filepath.Join(y.KnowledgeDir(), "INDEX.md")); err != nil || string(got) != index {
		t.Fatalf("context changed existing index: %q, %v", got, err)
	}
}

func TestOneLineKeepsRunes(t *testing.T) {
	if got := oneLine("修复登录超时问题", 4); got != "修复登录…" {
		t.Errorf("oneLine = %q", got)
	}
}
