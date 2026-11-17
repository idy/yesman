package agent

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestManagedInputIdentityAndIdleResume(t *testing.T) {
	f := newPromotionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ids := []string{deliveryTestID(63001), deliveryTestID(63002)}
	for _, id := range ids {
		if _, err := f.m.submit(ctx, f.info.ID, id, "same", nil, "followUp", nil, f.info.Since); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.m.Abort(ctx, f.info.ID); err != nil {
		t.Fatal(err)
	}
	// Session is alive and still owns both native occurrences. Refresh does not
	// equate idle with an empty queue, and an explicit action may resume this owner.
	conv, err := f.m.Messages(ctx, f.info.ID)
	if err != nil {
		t.Fatal(err)
	}
	eligible := 0
	for _, d := range conv.Deliveries {
		if d.CanPromote {
			eligible++
		}
	}
	if eligible != 2 {
		t.Fatalf("idle confirmed pending lost authority: %d", eligible)
	}
	if err = os.WriteFile(f.gate, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := f.m.PromoteSubmission(ctx, f.info.ID, ids[1], f.info.Since)
	if err != nil || result.Outcome != "promoted" {
		t.Fatalf("idle resume %+v %v", result, err)
	}
	f.delivery(t, ids[0], "dispatched")
	f.delivery(t, ids[1], "dispatched")
	waitStatus(t, f.m, f.info.ID, Idle)
	assertHistory := func(m *Manager) {
		t.Helper()
		conv, err := m.Messages(ctx, f.info.ID)
		if err != nil {
			t.Fatal(err)
		}
		var messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Origin  Origin          `json:"yesman"`
		}
		if err = json.Unmarshal(conv.Messages, &messages); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, msg := range messages {
			if msg.Role == "user" && firstText(msg.Content) == "same" {
				got = append(got, msg.Origin.Submission)
			}
		}
		if !reflect.DeepEqual(got, []string{ids[1], ids[0]}) {
			t.Fatalf("equal messages lost/smeared occurrence identity: %v", got)
		}
		for _, d := range conv.Deliveries {
			if d.ID == ids[0] || d.ID == ids[1] {
				if d.State != "dispatched" || d.NativePending || d.CanPromote || d.CanCancel {
					t.Fatalf("history revived queue: %+v", d)
				}
			}
		}
	}
	assertHistory(f.m)
	f.m.StopAll()
	m := New(f.cfg)
	defer m.StopAll()
	assertHistory(m)
	// A native recorded association can reconcile an interrupted receipt after a
	// crash even when its user input is no longer in the current model projection.
	m.mu.Lock()
	a := m.agents[f.info.ID]
	p := a.proc
	d := a.deliveries[ids[0]]
	d.State = "interrupted"
	a.deliveries[d.ID] = d
	m.mu.Unlock()
	m.reconcileRecordedInputs(a, p, p.piSession.Load().(string), []hostInputRef{{Native: true, EntryID: "synthetic-recorded-entry", Identity: &hostIdentity{SubmissionID: ids[0], Origin: Origin{Source: OriginUser}}}})
	m.mu.Lock()
	d = a.deliveries[ids[0]]
	m.mu.Unlock()
	if d.State != "dispatched" {
		t.Fatal("recorded intake did not reconcile after missing projection")
	}
}
