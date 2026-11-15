package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

// The real Manager starts this test binary as a deterministic RPC child.
// It reads no pi configuration and makes no model/provider request.
func TestMain(m *testing.M) {
	if os.Getenv("YESMAN_NEEDS_YOU_FAKE_PI") != "" {
		needsYouFakePi() // the scripted pi of needsyou_browser_test.go
		os.Exit(0)
	}
	if os.Getenv("YESMAN_TEST_METADATA_RPC_CHILD") == "1" {
		in, out := bufio.NewScanner(os.Stdin), json.NewEncoder(os.Stdout)
		for in.Scan() {
			var cmd struct{ ID, Type string }
			if json.Unmarshal(in.Bytes(), &cmd) != nil {
				continue
			}
			data := map[string]any{}
			if cmd.Type == "get_state" {
				data = map[string]any{"sessionId": "metadata-fixture", "model": map[string]any{"id": "fixture", "provider": "synthetic"}}
			} else if cmd.Type == "get_messages" {
				data["messages"] = []any{}
			}
			_ = out.Encode(map[string]any{"type": "response", "id": cmd.ID, "command": cmd.Type, "success": true, "data": data})
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func metadataServiceFixture(t *testing.T) (*service, yesmen.Yesman, yesmen.Yesman, []byte) {
	t.Helper()
	a := usageApp(t)
	a.pool = pool.New(a.home.Repos())
	alice, err := a.yesmen.HireAs("alice", "synthetic/alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := a.yesmen.HireAs("bob", "synthetic/bob")
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a.agents = agent.New(agent.Config{Pi: exe, Extension: filepath.Join(a.home.Root, "none.ts"), Yesmen: a.yesmen,
		Env: []string{"YESMAN_TEST_METADATA_RPC_CHILD=1", "GORACE=atexit_sleep_ms=0"}})
	t.Cleanup(a.agents.StopAll)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := a.agents.Messages(ctx, "bob/lead"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(alice.MetaDir(), "yesman.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(original, &meta); err != nil {
		t.Fatal(err)
	}
	meta["future"] = json.RawMessage(`{"large":9007199254740993,"exponent":1e400,"private":"fixture-only-do-not-report"}`)
	original, err = json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	original = append(original, '\n')
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	return &service{app: a}, alice, bob, original
}

func TestMetadataMixedListStateAndRepair(t *testing.T) {
	s, alice, bob, original := metadataServiceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	before, err := s.app.agents.Get("bob/lead")
	if err != nil || before.Status != agent.Idle || before.StartedAt.IsZero() {
		t.Fatalf("Bob not actually running: %+v %v", before, err)
	}
	path := filepath.Join(alice.MetaDir(), "yesman.json")
	oversize := append(append([]byte{}, original...), bytes.Repeat([]byte(" "), (1<<20)+1-len(original))...)
	if err := os.WriteFile(path, oversize, 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := s.app.yesmen.List()
	var issues yesmen.MetadataErrors
	if !errors.As(err, &issues) || len(list) != 1 || list[0] != bob || len(issues) != 1 || issues[0].Name != "alice" || issues[0].Path != path || issues[0].Size != 1048577 || issues[0].Limit != 1048576 {
		t.Fatalf("List did not isolate Alice: list=%+v issues=%+v err=%v", list, issues, err)
	}
	st, err := s.State(ctx)
	if err != nil || len(st.Yesmen) != 1 || st.Yesmen[0].Yesman != bob || len(st.WorkspaceErrors) != 1 || st.WorkspaceErrors[0] != issues[0] || len(st.Agents) != 1 || st.Agents[0].ID != "bob/lead" || st.Agents[0].Status != agent.Idle || !st.Agents[0].Since.Equal(bob.HiredAt) {
		t.Fatalf("State admitted invalid membership or lost Bob: %+v %v", st, err)
	}
	encoded, err := json.Marshal(st)
	if err != nil || bytes.Contains(encoded, []byte("fixture-only-do-not-report")) {
		t.Fatalf("State diagnostic leaked metadata content: %v", err)
	}
	called := false
	if _, err := s.app.yesmen.Get("alice"); err == nil || !strings.Contains(err.Error(), "1048577") || !strings.Contains(err.Error(), path) {
		t.Fatalf("Alice lacks actionable error: %v", err)
	}
	if err := s.app.yesmen.Use("alice", func(yesmen.Yesman) error { called = true; return nil }); err == nil || called {
		t.Fatalf("Alice's diagnostic bypassed Get: %v", err)
	}
	if _, err := s.Rename("alice", "Recovered"); err == nil {
		t.Fatal("historical oversized source was silently renamed")
	}
	if _, err := s.app.agents.Messages(ctx, "alice/lead"); err == nil {
		t.Fatal("unavailable Alice started an agent")
	}
	if got, err := s.app.yesmen.Get("bob"); err != nil || got != bob {
		t.Fatalf("Bob Get changed: %v", err)
	}
	if err := s.app.yesmen.Use("bob", func(y yesmen.Yesman) error { called = y == bob; return nil }); err != nil || !called {
		t.Fatalf("Bob Use unavailable: %v", err)
	}
	if _, err := s.app.agents.Messages(ctx, "bob/lead"); err != nil {
		t.Fatalf("Bob Messages unavailable: %v", err)
	}
	if after, err := s.app.agents.Get("bob/lead"); err != nil || after.Status != agent.Idle || !after.Since.Equal(before.Since) || !after.StartedAt.Equal(before.StartedAt) {
		t.Fatalf("Bob's process/generation changed: %+v %v", after, err)
	}
	// Existing diagnostics channels also keep normal startup and usage usable.
	if err := os.Remove(bob.TaskLogPath()); err != nil {
		t.Fatal(err)
	}
	if err := s.app.yesmen.EnsureTaskLogs(); !errors.As(err, &issues) {
		t.Fatalf("startup did not report Alice: %v", err)
	}
	if _, err := os.Stat(bob.TaskLogPath()); err != nil {
		t.Fatalf("Alice prevented Bob's task log initialization: %v", err)
	}
	if _, err := s.app.modelUsage(ctx, ModelUsageQuery{Yesman: "bob"}, time.Now()); err != nil {
		t.Fatalf("Alice prevented Bob's usage: %v", err)
	}
	usage, err := s.app.modelUsage(ctx, ModelUsageQuery{}, time.Now())
	if err != nil || len(usage.Coverage.Errors) != 1 || !strings.Contains(usage.Coverage.Errors[0], path) {
		t.Fatalf("global usage omitted diagnostic: %+v %v", usage.Coverage, err)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, oversize) {
		t.Fatalf("mixed operations rewrote Alice: %v", err)
	}
	// The repair is explicit, with a backup, only in this owned fixture.
	if err := os.WriteFile(path+".backup", oversize, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.app.yesmen.Get("alice"); err != nil || got != alice {
		t.Fatalf("repair changed Alice's original HiredAt: %+v %v", got, err)
	}
	st, err = s.State(ctx)
	if err != nil || len(st.Yesmen) != 2 || st.Yesmen[0].Name != "alice" || st.Yesmen[1].Name != "bob" || len(st.WorkspaceErrors) != 0 {
		t.Fatalf("repaired State did not recover normal order: %+v %v", st, err)
	}
}
