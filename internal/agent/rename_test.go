package agent

import (
	"context"
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
