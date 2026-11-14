package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// A follow-up from the app keeps its origin and pi's acceptance in the
// receipt, also after the run settles and after a restart; lookups tell a
// retained, a never-registered and an unretained ID apart without sending.
func TestSubmitFollowUpKeepsOriginAndAcceptance(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=started")
	id := ID("alice", yesmen.LeadName)
	ctx := context.Background()
	y, _ := store.Get("alice")
	messageID := deliveryTestID(1)
	if _, found, err := m.LookupDelivery(ctx, id, messageID, y.HiredAt); err != nil || found != DeliveryAbsent {
		t.Fatalf("before: %v %v", found, err)
	}
	origin := Origin{Kind: OriginSchedule, Plan: "s1", Run: "s1-20261116t0900"}
	d, err := m.SubmitFollowUp(ctx, id, messageID, "[yesman] Scheduled task s1 is due.", origin, y.HiredAt)
	if err != nil || d.Ack != "accepted" || d.Origin == nil || *d.Origin != origin {
		t.Fatalf("submit: %+v %v", d, err)
	}
	m.mu.Lock()
	p := m.agents[id].proc
	m.mu.Unlock()
	if _, err := p.call(ctx, map[string]any{"type": "test_settle"}); err != nil {
		t.Fatal(err)
	}
	d = deliveryState(t, m, id, messageID, "unconfirmed")
	if d.Ack != "accepted" {
		t.Fatalf("settled: %+v", d)
	}
	if _, err := m.SubmitFollowUp(ctx, id, messageID, "different text", origin, y.HiredAt); err == nil {
		t.Fatal("an ID was reused for other text")
	}
	m.StopAll()

	again := New(Config{Pi: m.cfg.Pi, Extension: m.cfg.Extension, Env: m.cfg.Env, Yesmen: yesmen.New(filepath.Join(filepath.Dir(y.Path)), filepath.Join(filepath.Dir(filepath.Dir(y.Path)), "archive"))})
	defer again.StopAll()
	got, found, err := again.LookupDelivery(ctx, id, messageID, y.HiredAt)
	if err != nil || found != DeliveryFound || got.Ack != "accepted" || got.Origin == nil || got.Origin.Run != origin.Run {
		t.Fatalf("after restart: %+v %v %v", got, found, err)
	}
	old := deliveryTestIDAt(time.Now().Add(-25*time.Hour), 2)
	if _, found, err := again.LookupDelivery(ctx, id, old, y.HiredAt); err != nil || found != DeliveryUnknown {
		t.Fatalf("expired: %v %v", found, err)
	}
	if again.running(id) != nil {
		t.Fatal("a lookup started pi")
	}
}

func deliveryTestIDAt(at time.Time, n int) string { return fmtID(at.UnixMilli(), n) }

func fmtID(ms int64, n int) string {
	const hex = "0123456789abcdef"
	b := make([]byte, 48)
	for i := 11; i >= 0; i-- {
		b[i] = hex[ms&15]
		ms >>= 4
	}
	for i := 47; i >= 12; i-- {
		b[i] = hex[n&15]
		n >>= 4
	}
	return string(b)
}
