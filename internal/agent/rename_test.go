package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/yesmen"
)

func TestRenameKeepsRunningAgentsAndCredentials(t *testing.T) {
	m, store := newTestManager(t, 0)
	if err := store.AddWorker("alice", yesmen.Worker{Name: "worker-one", Task: "synthetic task"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"alice/lead", "alice/worker-one"} {
		if _, err := m.Messages(ctx, id); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, m, id, Idle)
	}
	m.mu.Lock()
	leadProc, workerProc := m.agents["alice/lead"].proc, m.agents["alice/worker-one"].proc
	leadToken, workerToken := m.agents["alice/lead"].token, m.agents["alice/worker-one"].token
	m.mu.Unlock()
	if _, err := store.Rename("alice", "项目 Alpha"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alice/lead", "alice/worker-one"} {
		if info, err := m.Get(id); err != nil || info.ID != id || info.Yesman != "alice" || info.Status != Idle {
			t.Fatalf("agent changed: %+v %v", info, err)
		}
		if _, err := m.Messages(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := m.Authenticate(leadToken); err != nil || got.ID != "alice/lead" {
		t.Fatalf("lead credential lost: %v", err)
	}
	if got, err := m.Authenticate(workerToken); err != nil || got.ID != "alice/worker-one" {
		t.Fatalf("worker credential lost: %v", err)
	}
	if m.running("alice/lead") != leadProc || m.running("alice/worker-one") != workerProc {
		t.Fatal("rename replaced a running process")
	}
	if err := m.Prompt(ctx, "alice/lead", "synthetic message after rename"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "alice/lead", Idle)
	if m.running("alice/lead") != leadProc {
		t.Fatal("prompt after rename started another process")
	}
}

func TestMetadataRejectedRenameKeepsAgentsAndWorkspace(t *testing.T) {
	m, store := newTestManager(t, 0)
	if err := store.AddWorker("alice", yesmen.Worker{Name: "worker-one", Task: "synthetic task"}); err != nil {
		t.Fatal(err)
	}
	title, owner := "Preserved task", "worker-one"
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: &title, Owner: &owner}}}); err != nil {
		t.Fatal(err)
	}
	y, err := store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ids := []string{"alice/lead", "alice/worker-one"}
	before := make([]Info, len(ids))
	for i, id := range ids {
		if _, err := m.Messages(ctx, id); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, m, id, Idle)
		before[i], err = m.Get(id)
		if err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	leadProc, workerProc := m.agents[ids[0]].proc, m.agents[ids[1]].proc
	leadToken, workerToken := m.agents[ids[0]].token, m.agents[ids[1]].token
	m.mu.Unlock()
	session := filepath.Join(y.SessionDir("worker-one"), "kept-session.jsonl")
	if err := os.WriteFile(session, []byte("synthetic kept session\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, path := range []string{filepath.Join(y.MetaDir(), "yesman.json"), filepath.Join(y.MetaDir(), "workers.json"), filepath.Join(y.MetaDir(), "board.json"), session} {
		files[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Rename("alice", strings.Repeat("A", 1<<20)); err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
		t.Fatalf("oversized Rename did not explicitly refuse: %v", err)
	}
	for i, id := range ids {
		after, err := m.Get(id)
		if err != nil || after.Status != Idle || !after.Since.Equal(before[i].Since) {
			t.Fatalf("refused Rename changed live generation for %s: %v", id, err)
		}
		if _, err := m.Messages(ctx, id); err != nil {
			t.Fatalf("refused Rename blocked Messages for %s: %v", id, err)
		}
	}
	for i, token := range []string{leadToken, workerToken} {
		if got, err := m.Authenticate(token); err != nil || got.ID != ids[i] {
			t.Fatalf("refused Rename lost credential: %v", err)
		}
	}
	if m.running(ids[0]) != leadProc || m.running(ids[1]) != workerProc {
		t.Fatal("refused Rename replaced a running process")
	}
	for path, original := range files {
		if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, original) {
			t.Fatalf("refused Rename changed %s: %v", path, err)
		}
	}
	if got, err := store.Get("alice"); err != nil || got != y {
		t.Fatalf("refused Rename changed persisted identity: %v", err)
	}
}
