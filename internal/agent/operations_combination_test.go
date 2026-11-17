package agent

import (
	"context"
	"testing"
	"time"
)

func TestAppOperationSharesExactProcessRetirement(t *testing.T) {
	m, store := newTestManager(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := m.Messages(ctx, "alice/lead"); err != nil {
		t.Fatal(err)
	}
	y, _ := store.Get("alice")
	want, _ := m.Get("alice/lead")
	token, p := credentialOf(t, m, want.ID)
	opCtx, finish, err := m.BeginOperation(ctx, token, want, y.HiredAt)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if p.changes.TryLock() {
		p.changes.Unlock()
		t.Fatal("App operation is outside the process change gate")
	}
	change, err := m.BeginChange(token, y)
	if err != nil {
		t.Fatal(err)
	}
	defer change()
	entered, release := make(chan struct{}), make(chan struct{})
	callerDone := make(chan error, 1)
	go func() {
		callerDone <- m.CallerOperation(token, want, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	m.mu.Lock()
	a := m.agents[want.ID]
	m.mu.Unlock()
	p.stop(time.Second) // actual natural exit: no Manager.Stop call
	waitRetiring(t, p)
	refusedWhileDraining(t, m, token, y)
	select {
	case <-opCtx.Done():
	case <-ctx.Done():
		t.Fatal("natural exit did not cancel App before gate drain")
	}
	finish()
	restarted := make(chan error, 1)
	go func() { _, err := m.Messages(ctx, want.ID); restarted <- err }()
	select {
	case <-restarted:
		t.Fatal("restart passed active Board/CallerOperation gate")
	default:
	}
	change()
	close(release)
	if err := <-callerDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-restarted:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("restart did not finish")
	}
	freshToken, newProc := credentialOf(t, m, want.ID)
	fresh, _ := m.Get(want.ID)
	newCtx, newFinish, err := m.BeginOperation(ctx, freshToken, fresh, y.HiredAt)
	if err != nil {
		t.Fatal(err)
	}
	defer newFinish()
	m.retireCredential(a, p) // delayed old watcher must leave replacement intact
	if newCtx.Err() != nil || !credentialLive(m, freshToken) || newProc == p {
		t.Fatal("old revocation canceled/revoked replacement")
	}
	if err := m.CallerOperation(freshToken, fresh, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
