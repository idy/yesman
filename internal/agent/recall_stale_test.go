package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStaleCancelDoesNotClaimConsumption(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_RECALL_LIFECYCLE=1", "FAKE_PI_PROMOTE_LIFECYCLE=1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seen := make(chan struct{}, 2)
	m.cfg.OnEvent = func(e Event) {
		var h struct{ Type string }
		_ = json.Unmarshal(e.Data, &h)
		if h.Type == "test_cancel_received" {
			seen <- struct{}{}
		}
	}
	id := deliveryTestID(64001)
	d, err := m.submit(ctx, "alice/lead", id, "original", nil, "followUp")
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	a := m.agents["alice/lead"]
	p := a.proc
	d = a.deliveries[id]
	m.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := m.CancelSubmission(ctx, "alice/lead", id, d.AgentSince); done <- err }()
	select {
	case <-seen:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err = p.call(ctx, map[string]any{"type": "test_promote_now", "queueId": d.queueID}); err != nil {
		t.Fatal(err)
	}
	if _, err = p.call(ctx, map[string]any{"type": "test_release_mutations"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err == nil || !strings.Contains(err.Error(), "changed") || strings.Contains(err.Error(), "took") {
		t.Fatalf("stale observation claimed consumption: %v", err)
	}
	m.mu.Lock()
	current := a.deliveryView(a.deliveries[id])
	m.mu.Unlock()
	if current.State != "steering" || !current.NativePending || !current.CanCancel {
		t.Fatalf("stale action corrupted current authority: %+v", current)
	}
	restored := make(chan RecallResult, 1)
	go func() { r, _ := m.CancelSubmission(ctx, "alice/lead", id, d.AgentSince); restored <- r }()
	select {
	case <-seen:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err = p.call(ctx, map[string]any{"type": "test_release_mutations"}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-restored:
		if r.Delivery.State != "cancelled" || r.Payload.Text != "original" {
			t.Fatal("fresh explicit recall failed")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestQueueCapabilityNegotiationRejectsFutureVersions(t *testing.T) {
	for _, capability := range []int{-1, 0, 1, 2, 3, 4, 99} {
		q := pendingQueue{Capability: capability}
		s := pendingStatus(q)
		if s.RecallSupported != (capability == 1 || capability == 2 || capability == 3) || s.PromoteSupported != (capability == 2 || capability == 3) {
			t.Fatalf("unexpected negotiated capability %d: %+v", capability, s)
		}
	}
}
