package server

import (
	"context"
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

func TestOneLineKeepsRunes(t *testing.T) {
	if got := oneLine("修复登录超时问题", 4); got != "修复登录…" {
		t.Errorf("oneLine = %q", got)
	}
}
