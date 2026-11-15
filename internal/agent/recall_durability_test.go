package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func TestRecallDurabilityWaitReleasesWorkspace(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_RECALL_LIFECYCLE=1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	writing, releaseWrite := make(chan struct{}), make(chan struct{})
	var blockOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWrite) }) }
	t.Cleanup(release)
	m.cfg.deliveryWrite = func(path string, data []byte) error {
		var disk deliveryDisk
		_ = json.Unmarshal(data, &disk)
		for _, record := range disk.Records {
			if record.State == "cancelled" {
				blockOnce.Do(func() { close(writing); <-releaseWrite })
				break
			}
		}
		return writeDeliveryFile(path, data)
	}
	seen := make(chan struct{}, 1)
	m.cfg.OnEvent = func(e Event) {
		var h struct{ Type string }
		_ = json.Unmarshal(e.Data, &h)
		if h.Type == "test_cancel_received" {
			seen <- struct{}{}
		}
	}
	id := deliveryTestID(14200)
	d, err := m.Submit(ctx, "alice/lead", id, "durable original", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.CancelSubmission(ctx, "alice/lead", id, d.AgentSince); done <- err }()
	<-seen
	if _, err = m.running("alice/lead").call(ctx, map[string]any{"type": "test_release_cancels"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-writing:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	acquired := make(chan struct{})
	go func() {
		_, _ = store.Archive("alice", func(yesmen.Yesman) error { close(acquired); return errors.New("fixture keeps workspace") })
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("durability wait retained workspace lease")
	}
	select {
	case err = <-done:
		t.Fatalf("payload returned before durability: %v", err)
	default:
	}
	release()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
