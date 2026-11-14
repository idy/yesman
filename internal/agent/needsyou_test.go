package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func waitInfo(t *testing.T, m *Manager, id string, ok func(Info) bool) Info {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if i, err := m.Get(id); err == nil && ok(i) {
			return i
		}
		time.Sleep(10 * time.Millisecond)
	}
	i, _ := m.Get(id)
	t.Fatalf("%s never reached the expected state: %+v", id, i)
	return i
}

// A worker's question reaches the user only when its lead hands it over, and
// only that question: a newer one, an answer or a stop takes it off again.
func TestHandOverWorkerDialog(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_ASK_WORKERS=1")
	if _, err := m.StartWorker(context.Background(), "alice", "w1", "Clean the build", "a task"); err != nil {
		t.Fatal(err)
	}
	id, lead := ID("alice", "w1"), ID("alice", yesmen.LeadName)
	waitStatus(t, m, id, Waiting)
	if i, _ := m.Get(id); i.HandedOver || i.DialogAt.IsZero() {
		t.Fatalf("before handing over: %+v", i)
	}
	if err := m.HandOver(id, "q9"); err == nil {
		t.Error("handed over a question the worker is not waiting on")
	}
	if err := m.HandOver(lead, "q1"); err == nil {
		t.Error("handed over the lead's own dialog")
	}
	if err := m.HandOver(id, "q1"); err != nil {
		t.Fatal(err)
	}
	if err := m.HandOver(id, "q1"); err != nil {
		t.Fatalf("handing over again: %v", err)
	}
	if i, _ := m.Get(id); !i.HandedOver {
		t.Fatalf("after handing over: %+v", i)
	}
	// A newer question is not handed over with it.
	m.mu.Lock()
	a, p := m.agents[id], m.agents[id].proc
	m.mu.Unlock()
	m.record(a, p, "extension_ui_request", json.RawMessage(`{"type":"extension_ui_request","id":"q2","method":"confirm","title":"Another?"}`))
	if i, _ := m.Get(id); i.HandedOver || dialogID(i.Dialog) != "q2" {
		t.Fatalf("a newer question inherited the handover: %+v", i)
	}
	if err := m.HandOver(id, "q2"); err != nil {
		t.Fatal(err)
	}
	if err := m.Answer(id, "q2", map[string]any{"confirmed": true}); err != nil {
		t.Fatal(err)
	}
	if i, _ := m.Get(id); i.HandedOver || i.Dialog != nil || !i.DialogAt.IsZero() {
		t.Fatalf("an answered question still shows: %+v", i)
	}
	// Stopping the worker ends a handed-over question too.
	m.record(a, p, "extension_ui_request", json.RawMessage(`{"type":"extension_ui_request","id":"q3","method":"input","title":"Name?"}`))
	if err := m.HandOver(id, "q3"); err != nil {
		t.Fatal(err)
	}
	m.Stop(id)
	if i, _ := m.Get(id); i.HandedOver || i.Dialog != nil {
		t.Fatalf("a stopped worker's question still shows: %+v", i)
	}
	if err := m.HandOver(id, "q3"); err == nil {
		t.Error("handed over the question of a stopped worker")
	}
}

// pi answers a dialog itself at its timeout without saying so: the dialog
// stops being offered then, and a late answer is refused.
func TestDialogExpiresAtItsTimeout(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_ASK_WORKERS=1")
	if _, err := m.StartWorker(context.Background(), "alice", "w1", "Clean", "a task"); err != nil {
		t.Fatal(err)
	}
	id := ID("alice", "w1")
	waitStatus(t, m, id, Waiting)
	m.mu.Lock()
	a, p := m.agents[id], m.agents[id].proc
	m.mu.Unlock()
	m.record(a, p, "extension_ui_request", json.RawMessage(`{"type":"extension_ui_request","id":"q4","method":"confirm","title":"Quick?","timeout":80}`))
	if err := m.HandOver(id, "q4"); err != nil {
		t.Fatal(err)
	}
	i := waitInfo(t, m, id, func(i Info) bool { return i.Dialog == nil })
	if i.Status != Working || i.HandedOver {
		t.Fatalf("after the timeout: %+v", i)
	}
	if err := m.Answer(id, "q4", map[string]any{"confirmed": true}); err == nil {
		t.Error("answered an expired dialog")
	}
	// A timer for an answered or replaced dialog leaves the current one alone.
	m.record(a, p, "extension_ui_request", json.RawMessage(`{"type":"extension_ui_request","id":"q5","method":"confirm","title":"Slow?","timeout":60000}`))
	m.expireDialog(a, p, "q4")
	if i, _ := m.Get(id); dialogID(i.Dialog) != "q5" || i.Status != Waiting {
		t.Fatalf("a stale timer expired the current dialog: %+v", i)
	}
}

// What the user did reaches the lead of that workspace only, as yesman's
// notice, never the lead of a new yesman with the same name.
func TestTellLeadAboutUserIsScoped(t *testing.T) {
	m, store := newTestManager(t, 0)
	prompts := promptsTo(m)
	y, err := store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := m.TellLeadAboutUser(ctx, "alice", y.HiredAt.Add(-time.Hour), "[yesman] stale"); !errors.Is(err, errStopped) {
		t.Fatalf("an old workspace's notice = %v", err)
	}
	if err := m.TellLeadAboutUser(ctx, "alice", y.HiredAt, "[yesman] The user marked n1 as done."); err != nil {
		t.Fatal(err)
	}
	lead := ID("alice", yesmen.LeadName)
	waitPrompt(t, prompts, lead, "[yesman] The user marked n1 as done.")
	for _, p := range prompts(lead) {
		if p == "[yesman] stale" {
			t.Fatal("the stale notice was delivered")
		}
	}
}
