package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func TestRecallPendingRPCDoesNotOutliveArchiveGeneration(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_RECALL_LIFECYCLE=1")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	seen := make(chan struct{}, 1)
	m.cfg.OnEvent = func(e Event) {
		var h struct{ Type string }
		_ = json.Unmarshal(e.Data, &h)
		if h.Type == "test_cancel_received" {
			seen <- struct{}{}
		}
	}
	oldID := deliveryTestID(14100)
	d, err := m.Submit(ctx, "alice/lead", oldID, "old generation", nil)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	oldA, oldP := m.agents["alice/lead"], m.agents["alice/lead"].proc
	m.mu.Unlock()
	recalled := make(chan error, 1)
	go func() { _, err := m.CancelSubmission(ctx, "alice/lead", oldID, d.AgentSince); recalled <- err }()
	select {
	case <-seen:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	archived := make(chan string, 1)
	go func() {
		path, err := store.Archive("alice", func(yesmen.Yesman) error { return m.StopYesman("alice") })
		if err != nil {
			path = "ERROR: " + err.Error()
		}
		archived <- path
	}()
	var archive string
	select {
	case archive = <-archived:
	case <-time.After(3 * time.Second):
		t.Fatal("Archive waited for held cancellation response")
	}
	if _, err = os.Stat(archive); err != nil {
		t.Fatalf("archive %s: %v", archive, err)
	}
	select {
	case err = <-recalled:
		if err == nil {
			t.Fatal("retired cancellation reported payload success")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	oldFile := filepath.Join(archive, ".yesman", "sessions", "lead", "delivery.json")
	before, err := os.ReadFile(oldFile)
	if err != nil {
		t.Fatal(err)
	}
	m.ForgetYesman("alice", oldA.workspaceSince)
	if _, err = store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	freshID := deliveryTestID(14101)
	fresh, err := m.Submit(ctx, "alice/lead", freshID, "replacement", nil)
	if err != nil || !fresh.CanCancel {
		t.Fatalf("replacement: %+v %v", fresh, err)
	}
	// A callback holding the old object/process cannot affect the new agent or
	// recreate archived storage, even with a larger native revision.
	q := pendingQueue{Capability: 1, Epoch: d.queueEpoch, Revision: 999, Changes: []pendingItem{{QueueID: d.queueID, SubmissionID: oldID, Kind: "steer", State: "cancelled"}}}
	m.syncPending(oldA, oldP, q)
	if _, err = m.CancelSubmission(ctx, "alice/lead", oldID, d.AgentSince); err == nil {
		t.Fatal("stale generation recalled")
	}
	m.mu.Lock()
	a := m.agents["alice/lead"]
	got := a.deliveryView(a.deliveries[freshID])
	_, oldPresent := a.deliveries[oldID]
	m.mu.Unlock()
	if oldPresent || !got.CanCancel || got.State != "steering" {
		t.Fatalf("old callback altered replacement: %+v", got)
	}
	after, err := os.ReadFile(oldFile)
	if err != nil || string(after) != string(before) {
		t.Fatalf("late callback rewrote archive: %v", err)
	}
}
