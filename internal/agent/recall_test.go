package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecallPayloadBoundsAndEviction(t *testing.T) {
	m := &Manager{}
	chunk := strings.Repeat("x", maxAgentRecallBytes/4)
	agents := make([]*agent, 5)
	for i := range agents {
		agents[i] = &agent{}
		for n := 0; n < 5; n++ {
			m.retainRecallLocked(agents[i], deliveryTestID(i*10+n), chunk, nil)
		}
	}
	if m.recallBytes != maxRecallBytes || len(agents[4].recallPayloads) != 0 {
		t.Fatalf("global cap: %d", m.recallBytes)
	}
	for _, a := range agents {
		if a.recallBytes > maxAgentRecallBytes {
			t.Fatal("agent cap exceeded")
		}
		m.clearRecallLocked(a)
	}
	if m.recallBytes != 0 {
		t.Fatal("payloads leaked after retirement")
	}
	a := &agent{deliveries: map[string]Delivery{}}
	for n := 0; n < maxRecallRecords+10; n++ {
		m.retainRecallLocked(a, deliveryTestID(n), "text", nil)
	}
	if len(a.recallPayloads) != maxRecallRecords {
		t.Fatal("record cap not applied")
	}
	for id := range a.recallPayloads {
		a.deliveries[id] = Delivery{ID: id, State: "cancelled", Order: 1, created: time.Now().UnixMilli(), expires: time.Now().Add(-time.Second).UnixMilli()}
	}
	removed := m.pruneDeliveriesLocked(a)
	if len(removed) != maxRecallRecords || m.recallBytes != 0 || len(a.recallPayloads) != 0 {
		t.Fatal("receipt eviction retained full payload")
	}
	if err := newDeliveryIDAllowed(removed[0], a.deliveryFloor, time.Now()); err == nil {
		t.Fatal("cancelled ID revived after eviction")
	}
}

func TestRecallUnsupportedRuntimeNeverClearsQueue(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id := deliveryTestID(12000)
	d, err := m.Submit(ctx, "alice/lead", id, "same", []Image{testImage()})
	if err != nil {
		t.Fatal(err)
	}
	if d.CanCancel || d.NativePending {
		t.Fatalf("inferred pending membership: %+v", d)
	}
	if _, err = m.CancelSubmission(ctx, "alice/lead", id, d.AgentSince); err == nil {
		t.Fatal("unsupported runtime accepted recall")
	}
	conv, err := m.Messages(ctx, "alice/lead")
	if err != nil {
		t.Fatal(err)
	}
	if conv.Queue.Steering != 1 || conv.Queue.RecallSupported {
		t.Fatalf("native queue was changed: %+v", conv.Queue)
	}
}

func TestCancelledReceiptRestartAndFloor(t *testing.T) {
	now := time.Now()
	id := deliveryTestID(13000)
	path := filepath.Join(t.TempDir(), "delivery.json")
	d := Delivery{ID: id, State: "cancelled", Text: "preview", CanCancel: true, CanRestore: true, OriginalText: true, NativePending: true}
	fingerprint := deliveryFingerprint("complete", nil)
	disk := deliveryDisk{Version: 2, Records: []storedDelivery{{Delivery: d, Fingerprint: fingerprint, Created: now.UnixMilli(), Expires: now.Add(time.Minute).UnixMilli()}}}
	bytes, _ := json.Marshal(disk)
	if err := os.WriteFile(path, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	records, floor, err := readDeliveries(path, now)
	if err != nil {
		t.Fatal(err)
	}
	got := records[id]
	if got.State != "cancelled" || got.CanCancel || got.CanRestore || got.NativePending || got.OriginalText {
		t.Fatalf("restart invented capabilities: %+v", got)
	}
	pruneDeliveryRecords(records, &floor, now.Add(time.Hour))
	if err = newDeliveryIDAllowed(id, floor, now.Add(time.Hour)); err == nil {
		t.Fatal("expired cancellation dispatched again")
	}
}
