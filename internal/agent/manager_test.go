package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// The test binary doubles as a fake pi in RPC mode when YESMAN_FAKE_PI is
// set: it answers every command, delays get_state by FAKE_PI_DELAY, runs a
// trivial turn for each prompt, and exits when its input closes.
func TestMain(m *testing.M) {
	if os.Getenv("YESMAN_FAKE_PI") != "" {
		fakePi()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakePi() {
	delay, _ := time.ParseDuration(os.Getenv("FAKE_PI_DELAY"))
	out := bufio.NewWriter(os.Stdout)
	write := func(v any) {
		data, _ := json.Marshal(v)
		out.Write(append(data, '\n'))
		out.Flush()
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var cmd struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if json.Unmarshal(sc.Bytes(), &cmd) != nil {
			continue
		}
		if cmd.Type == "get_state" {
			time.Sleep(delay)
		}
		write(map[string]any{"type": "response", "id": cmd.ID, "command": cmd.Type, "success": true, "data": map[string]any{"messages": []any{}}})
		if cmd.Type == "prompt" {
			write(map[string]any{"type": "agent_start"})
			write(map[string]any{"type": "agent_settled", "aborted": false})
		}
	}
}

func newTestManager(t *testing.T, delay time.Duration) (*Manager, *yesmen.Store) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	store := yesmen.New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	if _, err := store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	m := New(Config{
		Pi:        exe,
		Extension: filepath.Join(tmp, "none.ts"),
		Env:       []string{"YESMAN_FAKE_PI=1", "FAKE_PI_DELAY=" + delay.String()},
		Yesmen:    store,
	})
	t.Cleanup(m.StopAll)
	return m, store
}

func waitStatus(t *testing.T, m *Manager, id string, want Status) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if i, err := m.Get(id); err == nil && i.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	i, _ := m.Get(id)
	t.Fatalf("%s is %s, want %s", id, i.Status, want)
}

func TestStopDuringStart(t *testing.T) {
	m, _ := newTestManager(t, 2*time.Second)
	id := ID("alice", yesmen.LeadName)
	errc := make(chan error, 1)
	go func() { errc <- m.Prompt(context.Background(), id, "hi") }()
	waitStatus(t, m, id, Starting)

	m.Stop(id)
	if p := m.running(id); p != nil {
		t.Fatal("a process still runs after Stop returned")
	}
	if i, _ := m.Get(id); i.Status != Stopped {
		t.Errorf("status after Stop = %s", i.Status)
	}
	if err := <-errc; !errors.Is(err, errStopped) {
		t.Errorf("the interrupted prompt returned %v", err)
	}
	m.mu.Lock()
	tokens := len(m.tokens)
	m.mu.Unlock()
	if tokens != 0 {
		t.Errorf("%d credentials outlive their processes", tokens)
	}
}

func TestRestartAfterStop(t *testing.T) {
	m, _ := newTestManager(t, 0)
	id := ID("alice", yesmen.LeadName)
	ctx := context.Background()
	for i := range 3 {
		if err := m.Prompt(ctx, id, fmt.Sprint("turn ", i)); err != nil {
			t.Fatalf("prompt %d: %v", i, err)
		}
		m.Stop(id)
	}
	if err := m.Prompt(ctx, id, "again"); err != nil {
		t.Fatal(err)
	}
}

func TestCredentials(t *testing.T) {
	m, store := newTestManager(t, 0)
	if err := store.AddWorker("alice", yesmen.Worker{Name: "w1", Task: "t", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lead, worker := ID("alice", yesmen.LeadName), ID("alice", "w1")
	for _, id := range []string{lead, worker} {
		if err := m.Prompt(ctx, id, "hi"); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	leadToken, workerToken := m.agents[lead].token, m.agents[worker].token
	m.mu.Unlock()
	if leadToken == "" || leadToken == workerToken {
		t.Fatalf("agents share or lack credentials: %q %q", leadToken, workerToken)
	}
	if i, err := m.Authenticate(workerToken); err != nil || i.ID != worker || i.Role != RoleWorker {
		t.Errorf("worker credential = %+v, %v", i, err)
	}
	if i, err := m.Authenticate(leadToken); err != nil || i.ID != lead {
		t.Errorf("lead credential = %+v, %v", i, err)
	}
	if _, err := m.Authenticate("forged"); err == nil {
		t.Error("an unknown credential authenticated")
	}
	m.Stop(worker)
	if _, err := m.Authenticate(workerToken); err == nil {
		t.Error("a stopped worker's credential still authenticates")
	}
	if i, _ := m.Get(worker); !strings.HasPrefix(i.ID, "alice/") || i.Since.IsZero() {
		t.Errorf("worker identity %+v", i)
	}
}
