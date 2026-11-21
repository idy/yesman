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
	// The answer lets w1's run settle; its report and close then race any
	// question made up for its old process. Stopping is shown with a second
	// worker, which waits on its own first question.
	w2 := ID("alice", "w2")
	if _, err := m.StartWorker(context.Background(), "alice", "w2", "Lint", "a task"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, w2, Waiting)
	if err := m.HandOver(w2, "q1"); err != nil {
		t.Fatal(err)
	}
	// Stopping the worker ends a handed-over question too.
	m.Stop(w2)
	if i, _ := m.Get(w2); i.HandedOver || i.Dialog != nil {
		t.Fatalf("a stopped worker's question still shows: %+v", i)
	}
	if err := m.HandOver(w2, "q1"); err == nil {
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
	fast, _ := m.Get(id)
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
	m.expireDialog(a, p, "q4", fast.DialogAt)
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

// A caller operation runs only for the process that holds the credential, in
// its generation; Stop waits for one already admitted and admits no more, and
// a restart's new credential does not revive the old one.
func TestCallerOperationFencesStop(t *testing.T) {
	m, _ := newTestManager(t, 0)
	id := ID("alice", yesmen.LeadName)
	if _, err := m.Messages(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	info, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	token := m.agents[id].token
	m.mu.Unlock()
	run := func(token string, want Info) error {
		return m.CallerOperation(token, want, func() error { return nil })
	}
	if err := run(token, info); err != nil {
		t.Fatalf("current caller: %v", err)
	}
	for name, c := range map[string]struct {
		token string
		want  Info
	}{
		"unknown credential": {"not-a-token", info},
		"other agent":        {token, Info{ID: ID("alice", "w1"), Since: info.Since}},
		"older generation":   {token, Info{ID: id, Since: info.Since.Add(-time.Hour)}},
	} {
		if err := run(c.token, c.want); !errors.Is(err, errCallerGone) {
			t.Errorf("%s: %v", name, err)
		}
	}

	admitted, release := make(chan struct{}), make(chan struct{})
	opDone := make(chan error, 1)
	go func() {
		opDone <- m.CallerOperation(token, info, func() error {
			close(admitted)
			<-release
			return nil
		})
	}()
	<-admitted
	stopped := make(chan struct{})
	go func() { m.Stop(id); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop returned while an admitted operation was running")
	case <-time.After(100 * time.Millisecond):
	}
	// Stopping admits nothing new, even with the still-valid credential.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := run(token, info); errors.Is(err, errCallerGone) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a stopping agent still admitted operations")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	if err := <-opDone; err != nil {
		t.Fatalf("the admitted operation: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return after the operation ended")
	}
	if _, err := m.Messages(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	fresh := m.agents[id].token
	m.mu.Unlock()
	if fresh == "" || fresh == token {
		t.Fatalf("restart credential %q", fresh)
	}
	if err := run(token, info); !errors.Is(err, errCallerGone) {
		t.Errorf("the old credential after restart: %v", err)
	}
	if err := run(fresh, info); err != nil {
		t.Errorf("the new credential: %v", err)
	}
}

// A Needs you change is admitted as a board change is: a process that exits
// by itself waits, before its restart, for the operation it admitted, and
// admits none after; the old credential stays refused once the new one runs.
func TestCallerOperationIsRetiredWithItsProcess(t *testing.T) {
	m, _ := newTestManager(t, 0)
	ctx := context.Background()
	id := ID("alice", yesmen.LeadName)
	if err := m.Prompt(ctx, id, "hi"); err != nil {
		t.Fatal(err)
	}
	info, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	token, p := credentialOf(t, m, id)
	admitted, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	t.Cleanup(free) // before the manager's own cleanup stops the agent
	opDone := make(chan error, 1)
	go func() {
		opDone <- m.CallerOperation(token, info, func() error {
			close(admitted)
			<-release
			return nil
		})
	}()
	<-admitted
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitRetiring(t, p)
	// Retiring the process waits for the operation: a moment later, it
	// still does.
	time.Sleep(50 * time.Millisecond)
	if p.changes.TryRLock() {
		p.changes.RUnlock()
		t.Fatal("the exited process's retirement did not wait for its admitted operation")
	}
	restarted := make(chan struct{})
	go func() {
		if err := m.Prompt(ctx, id, "after the crash"); err != nil {
			t.Error(err)
		}
		close(restarted)
	}()
	if err := m.CallerOperation(token, info, func() error { return nil }); !errors.Is(err, errCallerGone) {
		t.Fatalf("the exited process admitted another operation: %v", err)
	}
	notReturned(t, restarted, "the restart")
	free()
	if err := <-opDone; err != nil {
		t.Fatalf("the admitted operation: %v", err)
	}
	returns(t, restarted, "the restart")
	renewed, _ := credentialOf(t, m, id)
	if renewed == token {
		t.Fatal("the restart kept the old credential")
	}
	if err := m.CallerOperation(token, info, func() error { return nil }); !errors.Is(err, errCallerGone) {
		t.Errorf("the old credential after the restart: %v", err)
	}
	current, _ := m.Get(id)
	if err := m.CallerOperation(renewed, current, func() error { return nil }); err != nil {
		t.Errorf("the new credential: %v", err)
	}
}
