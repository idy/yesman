package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// This test controls transport timing, not the native promotion primitive.
// The actual primitive and provider delivery are checked by the managed-pi tests.
func TestPromotionRPCReleasesLeaseAndFencesArchive(t *testing.T) {
	for _, mode := range []string{"promote", "taken", "archive"} {
		t.Run(mode, func(t *testing.T) {
			m, store := newTestManager(t, 0, "FAKE_PI_RECALL_LIFECYCLE=1", "FAKE_PI_PROMOTE_LIFECYCLE=1")
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			seen := make(chan struct{}, 2)
			m.cfg.OnEvent = func(e Event) {
				var h struct{ Type string }
				_ = json.Unmarshal(e.Data, &h)
				if h.Type == "test_promote_received" {
					seen <- struct{}{}
				}
			}
			id := deliveryTestID(62001)
			d, err := m.submit(ctx, "alice/lead", id, "original", nil, "followUp")
			if err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			oldA, oldP := m.agents["alice/lead"], m.agents["alice/lead"].proc
			d = oldA.deliveries[id]
			m.mu.Unlock()
			done := make(chan error, 1)
			go func() { _, err := m.PromoteSubmission(ctx, "alice/lead", id, d.AgentSince); done <- err }()
			select {
			case <-seen:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			probe := make(chan error, 1)
			sentinel := errors.New("probe keeps workspace")
			go func() { _, err := store.Archive("alice", func(yesmen.Yesman) error { return sentinel }); probe <- err }()
			select {
			case err = <-probe:
				if !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("held promotion blocks Archive")
			}
			sent := make(chan error, 1)
			go func() { _, err := m.Submit(ctx, "alice/lead", deliveryTestID(62002), "other input", nil); sent <- err }()
			select {
			case err = <-sent:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("held promotion blocks other workspace input")
			}
			if mode == "archive" {
				archived := make(chan string, 1)
				go func() {
					path, err := store.Archive("alice", func(yesmen.Yesman) error { return m.StopYesman("alice") })
					if err != nil {
						path = "ERROR: " + err.Error()
					}
					archived <- path
				}()
				var path string
				select {
				case path = <-archived:
				case <-time.After(3 * time.Second):
					t.Fatal("Archive waits for frozen promotion")
				}
				file := filepath.Join(path, ".yesman", "sessions", "lead", "delivery.json")
				before, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				select {
				case err = <-done:
					if err == nil {
						t.Fatal("retired operation succeeded")
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				m.ForgetYesman("alice", oldA.workspaceSince)
				if _, err = store.Hire("alice"); err != nil {
					t.Fatal(err)
				}
				fresh, err := m.Submit(ctx, "alice/lead", deliveryTestID(62003), "new owner", nil)
				if err != nil {
					t.Fatal(err)
				}
				m.syncPending(oldA, oldP, pendingQueue{Capability: 2, Epoch: d.queueEpoch, Revision: 999, PromoteAllowed: true, Items: []pendingItem{{QueueID: d.queueID, SubmissionID: id, Kind: "steer", ItemRevision: 1}}})
				if _, err = m.PromoteSubmission(ctx, "alice/lead", id, d.AgentSince); err == nil {
					t.Fatal("old incarnation promoted")
				}
				m.mu.Lock()
				_, oldPresent := m.agents["alice/lead"].deliveries[id]
				got := m.agents["alice/lead"].deliveries[fresh.ID]
				m.mu.Unlock()
				if oldPresent || got.State != "steering" {
					t.Fatal("late callback changed replacement")
				}
				after, err := os.ReadFile(file)
				if err != nil || string(after) != string(before) {
					t.Fatal("late callback changed archived receipts")
				}
				return
			}
			if mode == "taken" {
				if _, err = oldP.call(ctx, map[string]any{"type": "test_take", "queueId": d.queueID}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = oldP.call(ctx, map[string]any{"type": "test_release_mutations"}); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				if (err != nil) != (mode == "taken") {
					t.Fatalf("winner %s: %v", mode, err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}
