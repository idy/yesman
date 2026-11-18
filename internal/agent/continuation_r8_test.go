package agent

import (
	"context"
	"testing"

	"github.com/idy/yesman/internal/yesmen"
)

// A scheduled task's run is sent to a lead the user stopped as before, but
// it is not the user writing now: their Stop stays, durably, and yesman's
// own messages stay held. The user's own message still lifts it.
func TestR8ScheduledRunKeepsUserStop(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=started")
	id := ID("alice", yesmen.LeadName)
	ctx := context.Background()
	y, _ := store.Get("alice")
	if _, _, err := m.ensure(id); err != nil {
		t.Fatal(err)
	}
	m.StopByUser(id)
	if c, _ := store.Continuation("alice"); c.LeadPausedAt.IsZero() {
		t.Fatal("Stop did not persist")
	}
	origin := Origin{Source: OriginSchedule, Plan: "s1", Run: "s1-20261118t0900"}
	if _, err := m.SubmitFollowUp(ctx, id, deliveryTestID(1), "[yesman] Scheduled task s1 is due.", origin, y.HiredAt); err != nil {
		t.Fatal(err)
	}
	if c, _ := store.Continuation("alice"); c.LeadPausedAt.IsZero() {
		t.Fatal("a scheduled run lifted the user's Stop")
	}
	m.mu.Lock()
	paused := m.leadPausedLocked(y)
	m.mu.Unlock()
	if !paused {
		t.Fatal("a scheduled run lifted the user's pause in memory")
	}
	info, _ := m.Get(id)
	if _, err := m.Submit(ctx, id, deliveryTestID(2), "go on", nil, info.Since); err != nil {
		t.Fatal(err)
	}
	if c, _ := store.Continuation("alice"); !c.LeadPausedAt.IsZero() {
		t.Fatal("the user's own message did not lift the Stop")
	}
}
