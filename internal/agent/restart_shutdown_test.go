package agent

import (
	"sync"
	"testing"
	"time"
)

func TestRestartCompositionShutdownJoinsWaitPersistence(t *testing.T) {
	store, tmp, y := restartStore(t)
	m, _ := restartInstance(t, store, tmp)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	m.reviewBeforePersistPause = func(bool) { close(entered); <-release }
	held := make(chan error, 1)
	go func() { held <- m.HoldAfterRestart(restartLead(y)) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("hold did not reach persistence")
	}
	stopped := make(chan struct{})
	go func() { m.StopAll(); close(stopped) }()
	eventually(t, "shutdown closes admission", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.closed })
	select {
	case <-stopped:
		t.Fatal("shutdown returned before recovery write")
	default:
	}
	if err := m.HoldAfterRestart(restartLead(y)); err == nil {
		t.Fatal("closed manager admitted another write")
	}
	once.Do(func() { close(release) })
	select {
	case err := <-held:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hold did not finish")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not join")
	}
	c, err := store.Continuation("alice")
	if err != nil || c.LeadPausedAt.IsZero() {
		t.Fatalf("pause not durable before shutdown: %+v %v", c, err)
	}
}
