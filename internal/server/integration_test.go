package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

// TestWithPi runs real pi agents against the configured model. It costs
// model calls, so it runs only with YESMAN_IT=1:
//
//	YESMAN_IT=1 go test ./internal/server -run WithPi -v
func TestWithPi(t *testing.T) {
	if os.Getenv("YESMAN_IT") == "" {
		t.Skip("set YESMAN_IT=1 to run pi agents")
	}
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	ext, err := filepath.Abs("../../pi/yesman.ts")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "src", "demo")
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch=main", origin},
		{"-C", origin, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "first"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	p := pool.New(filepath.Join(tmp, "repos"))
	if _, err := p.Add(context.Background(), origin, ""); err != nil {
		t.Fatal(err)
	}
	store := yesmen.New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))

	var mu sync.Mutex
	settled := map[string]int{}
	var log []string
	srv := &Server{Pool: p, Yesmen: store, Token: "test-token"}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	m := agent.New(agent.Config{
		Pi:        piPath,
		Extension: ext,
		Env:       []string{"YESMAN_API=" + srv.URL(), "YESMAN_TOKEN=test-token", "YESMAN_HOME=" + tmp},
		Yesmen:    store,
		OnEvent: func(e agent.Event) {
			var head struct {
				Type     string `json:"type"`
				ToolName string `json:"toolName"`
				IsError  bool   `json:"isError"`
			}
			_ = json.Unmarshal(e.Data, &head)
			mu.Lock()
			defer mu.Unlock()
			switch head.Type {
			case "agent_settled":
				settled[e.Agent]++
			case "tool_execution_end", "extension_error":
				log = append(log, e.Agent+" "+head.Type+" "+head.ToolName+" error="+boolString(head.IsError))
			}
		},
	})
	srv.Agents = m
	defer m.StopAll()

	if _, err := store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	lead := agent.ID("alice", yesmen.LeadName)
	waitSettled := func(id string, n int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Minute)
		for time.Now().Before(deadline) {
			mu.Lock()
			got := settled[id]
			mu.Unlock()
			if got >= n {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("%s did not settle %d times; tool log:\n%s", id, n, strings.Join(log, "\n"))
	}

	ctx := context.Background()
	if err := m.Prompt(ctx, lead, "Use yesman_checkout to check out the pool repository demo on a new branch named try. "+
		"Then write a knowledge note .yesman/knowledge/demo.md saying the demo repo is a test fixture, and add it to .yesman/knowledge/INDEX.md. "+
		"Do not start workers. Reply with the word DONE."); err != nil {
		t.Fatal(err)
	}
	waitSettled(lead, 1)
	y, _ := store.Get("alice")
	wts, err := p.Worktrees(ctx, y.Path)
	if err != nil || len(wts) != 1 || wts[0].Branch != "try" {
		t.Errorf("worktrees after the lead's turn = %+v, %v", wts, err)
	}
	if _, err := os.Stat(filepath.Join(y.KnowledgeDir(), "demo.md")); err != nil {
		t.Errorf("no knowledge note: %v", err)
	}
	index, _ := os.ReadFile(filepath.Join(y.KnowledgeDir(), "INDEX.md"))
	if !strings.Contains(string(index), "demo.md") {
		t.Errorf("INDEX.md does not list the note:\n%s", index)
	}

	// A worker's final message reaches the lead, which starts another run.
	if err := m.Prompt(ctx, lead, "Start one worker named echo with yesman_worker_start whose task is: reply with the single word PONG and nothing else. "+
		"Then stop and wait; do nothing else."); err != nil {
		t.Fatal(err)
	}
	waitSettled(agent.ID("alice", "echo"), 1)
	waitSettled(lead, 3) // its own turn, then the run the report started
	data, err := m.Messages(ctx, lead)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `Worker \"echo\" finished`) {
		t.Errorf("the lead never received the worker's report")
	}

	// A restarted lead resumes its session.
	m.Stop(lead)
	data, err = m.Messages(ctx, lead)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "DONE") {
		t.Errorf("the restarted lead lost its conversation")
	}
	mu.Lock()
	t.Logf("tool log:\n%s", strings.Join(log, "\n"))
	mu.Unlock()
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
