package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// credentialOf returns the credential and process an agent runs with now.
func credentialOf(t *testing.T, m *Manager, id string) (string, *proc) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[id]
	if a == nil || a.token == "" || a.proc == nil || a.tokenProc != a.proc {
		t.Fatalf("%s has no running process with a credential", id)
	}
	return a.token, a.proc
}

func credentialLive(m *Manager, token string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.tokens[token]
	return ok
}

// waitRetiring waits until something retires p's changes: Stop, the exit
// watcher or a restart has asked for the gate a held change keeps. It
// observes that state, rather than sleeping and hoping it was reached.
func waitRetiring(t *testing.T, p *proc) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the process did not exit")
	}
	deadline := time.Now().Add(10 * time.Second)
	for p.changes.TryRLock() {
		p.changes.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("nothing waits to retire the credential's changes")
		}
		time.Sleep(time.Millisecond)
	}
}

func notReturned(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
		t.Fatalf("%s returned while a change admitted before it was still being written", what)
	default:
	}
}

func returns(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not return once the change ended", what)
	}
}

func TestBeginChangeIsBoundToProcessAndWorkspace(t *testing.T) {
	m, store := newTestManager(t, 0)
	ctx := context.Background()
	id := ID("alice", yesmen.LeadName)
	if err := m.Prompt(ctx, id, "hi"); err != nil {
		t.Fatal(err)
	}
	y, err := store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := credentialOf(t, m, id)
	release, err := m.BeginChange(token, y)
	if err != nil {
		t.Fatalf("current process refused: %v", err)
	}
	release()
	release() // idempotent

	rehired := y
	rehired.HiredAt = y.HiredAt.Add(time.Second)
	other := y
	other.Name = "bob"
	for name, c := range map[string]struct {
		token string
		y     yesmen.Yesman
	}{
		"empty":           {"", y},
		"unknown":         {"not-a-credential", y},
		"other hire":      {token, rehired},
		"other workspace": {token, other},
	} {
		if _, err := m.BeginChange(c.token, c.y); !errors.Is(err, ErrRetired) {
			t.Errorf("%s: BeginChange = %v, want ErrRetired", name, err)
		}
	}

	m.Stop(id)
	if _, err := m.BeginChange(token, y); !errors.Is(err, ErrRetired) {
		t.Fatalf("stopped process admitted: %v", err)
	}
	if err := m.Prompt(ctx, id, "again"); err != nil {
		t.Fatal(err)
	}
	renewed, _ := credentialOf(t, m, id)
	if renewed == token {
		t.Fatal("restart reused the credential")
	}
	if _, err := m.BeginChange(token, y); !errors.Is(err, ErrRetired) {
		t.Fatalf("restart readmitted the old credential: %v", err)
	}
	release, err = m.BeginChange(renewed, y)
	if err != nil {
		t.Fatalf("restarted process refused: %v", err)
	}
	release()
}

// A change admitted before Stop ends before Stop returns, and the
// credential is forgotten only after it; none is admitted afterwards.
func TestStopWaitsForAdmittedChange(t *testing.T) {
	m, store := newTestManager(t, 0)
	id := ID("alice", yesmen.LeadName)
	if err := m.Prompt(context.Background(), id, "hi"); err != nil {
		t.Fatal(err)
	}
	y, _ := store.Get("alice")
	token, p := credentialOf(t, m, id)
	release, err := m.BeginChange(token, y)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	stopped := make(chan struct{})
	go func() { m.Stop(id); close(stopped) }()
	waitRetiring(t, p)
	notReturned(t, stopped, "Stop")
	if !credentialLive(m, token) {
		t.Fatal("the credential was forgotten before its admitted change ended")
	}
	// A manager-wide read is not held up by the change or the waiting Stop.
	if _, err := m.Get(id); err != nil {
		t.Fatal(err)
	}
	release()
	returns(t, stopped, "Stop")
	if credentialLive(m, token) {
		t.Fatal("the credential outlived Stop")
	}
	if _, err := m.BeginChange(token, y); !errors.Is(err, ErrRetired) {
		t.Fatalf("admitted after Stop: %v", err)
	}
}

// A process that exits by itself retires its changes too: a restart waits
// for the admitted one, and the old credential is refused afterwards.
func TestNaturalExitWaitsForAdmittedChangeBeforeRestart(t *testing.T) {
	m, store := newTestManager(t, 0)
	ctx := context.Background()
	id := ID("alice", yesmen.LeadName)
	if err := m.Prompt(ctx, id, "hi"); err != nil {
		t.Fatal(err)
	}
	y, _ := store.Get("alice")
	token, p := credentialOf(t, m, id)
	release, err := m.BeginChange(token, y)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitRetiring(t, p)
	restarted := make(chan struct{})
	go func() {
		if err := m.Prompt(ctx, id, "after the crash"); err != nil {
			t.Error(err)
		}
		close(restarted)
	}()
	if !credentialLive(m, token) {
		t.Fatal("the exited process's credential was forgotten before its admitted change ended")
	}
	notReturned(t, restarted, "the restart")
	release()
	returns(t, restarted, "the restart")
	renewed, _ := credentialOf(t, m, id)
	if renewed == token || credentialLive(m, token) {
		t.Fatal("the exited process's credential outlived it")
	}
	if _, err := m.BeginChange(token, y); !errors.Is(err, ErrRetired) {
		t.Fatalf("exited process admitted: %v", err)
	}
}

// Removing a worker and quitting stop processes through Stop, and so wait
// for admitted changes and refuse later ones.
func TestRemoveWorkerAndStopAllWaitForAdmittedChange(t *testing.T) {
	// The worker's run waits on a dialog, so it neither finishes nor is
	// closed as finished while the test holds its change.
	m, store := newTestManager(t, 0, "FAKE_PI_ASK_WORKERS=1")
	ctx := context.Background()
	if _, err := m.StartWorker(ctx, "alice", "helper", "Helper", "help"); err != nil {
		t.Fatal(err)
	}
	if err := m.Prompt(ctx, ID("alice", yesmen.LeadName), "hi"); err != nil {
		t.Fatal(err)
	}

	// As the server does: the workspace's Use, then the process's admission.
	y, leave, err := store.BeginUse("alice")
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	worker, wp := credentialOf(t, m, ID("alice", "helper"))
	release, err := m.BeginChange(worker, y)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	removed := make(chan struct{})
	go func() {
		// RemoveWorker stops the worker within its own Use of the workspace.
		if err := m.RemoveWorker("alice", "helper"); err != nil {
			t.Error(err)
		}
		close(removed)
	}()
	waitRetiring(t, wp)
	notReturned(t, removed, "RemoveWorker")
	release()
	leave()
	returns(t, removed, "RemoveWorker")
	if _, err := m.BeginChange(worker, y); !errors.Is(err, ErrRetired) {
		t.Fatalf("removed worker admitted: %v", err)
	}
	if ws, _ := store.Workers("alice"); len(ws) != 0 {
		t.Fatalf("worker still listed: %+v", ws)
	}

	lead, lp := credentialOf(t, m, ID("alice", yesmen.LeadName))
	y, leave, err = store.BeginUse("alice")
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	release, err = m.BeginChange(lead, y)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	quit := make(chan struct{})
	go func() { m.StopAll(); close(quit) }()
	waitRetiring(t, lp)
	notReturned(t, quit, "StopAll")
	release()
	leave()
	returns(t, quit, "StopAll")
	if _, err := m.BeginChange(lead, y); !errors.Is(err, ErrRetired) {
		t.Fatalf("admitted after quitting: %v", err)
	}
}
