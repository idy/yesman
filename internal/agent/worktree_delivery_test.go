package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func TestWorktreeCleanupRejectsSubmitBeforeRegistration(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=handled")
	if err := store.AddWorker("alice", yesmen.Worker{Name: "owner", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, text, images := deliveryTestID(910), "original\n图文 body", []Image{testImage()}
	received := make(chan struct {
		Type   string  `json:"type"`
		Text   string  `json:"message"`
		Images []Image `json:"images"`
	}, 2)
	m.cfg.OnEvent = func(e Event) {
		var v struct {
			Type   string  `json:"type"`
			Text   string  `json:"message"`
			Images []Image `json:"images"`
		}
		if json.Unmarshal(e.Data, &v) == nil && v.Type == "test_prompt_received" {
			received <- v
		}
	}
	err := m.WithWorktreeOwner("alice/owner", "alice/cleanup", func() error {
		if d, err := m.Submit(ctx, "alice/owner", id, text, images); !errors.Is(err, errWorktreeCleanup) {
			t.Fatalf("cleanup allowed receipt registration: %+v %v", d, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	y, _ := store.Get("alice")
	if _, err := os.Stat(filepath.Join(y.SessionDir("owner"), "delivery.json")); !os.IsNotExist(err) {
		t.Fatal("blocked intent was persisted", err)
	}
	for range 2 {
		if d, err := m.Submit(ctx, "alice/owner", id, text, images); err != nil || d.State != "handled" {
			t.Fatalf("same ID retry: %+v %v", d, err)
		}
	}
	select {
	case got := <-received:
		if got.Text != text || !reflect.DeepEqual(got.Images, images) {
			t.Fatal("payload changed across cleanup/retry", got)
		}
	case <-ctx.Done():
		t.Fatal("retry never reached native RPC")
	}
	select {
	case <-received:
		t.Fatal("duplicate native dispatch")
	default:
	}
}

func TestWorktreeCleanupRetainsInitialSubmission(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=handled")
	if err := store.AddWorker("alice", yesmen.Worker{Name: "owner", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	loadGate, loading, writeGate, writing := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var loadOnce, writeOnce sync.Once
	defer loadOnce.Do(func() { close(loadGate) })
	defer writeOnce.Do(func() { close(writeGate) })
	m.cfg.deliveryRead = func(path string, now time.Time) (map[string]Delivery, int64, error) {
		close(loading)
		<-loadGate
		return readDeliveries(path, now)
	}
	var first sync.Once
	m.cfg.deliveryWrite = func(path string, data []byte) error {
		first.Do(func() { close(writing); <-writeGate })
		return writeDeliveryFile(path, data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.Submit(ctx, "alice/owner", deliveryTestID(911), "resume", nil); done <- err }()
	assertActive := func(stage string) {
		t.Helper()
		called := false
		err := m.WithWorktreeOwner("alice/owner", "alice/cleanup", func() error { called = true; return nil })
		if called || err == nil || !strings.Contains(err.Error(), "active") {
			t.Fatalf("cleanup passed %s submission: called=%t err=%v", stage, called, err)
		}
	}
	select {
	case <-loading:
	case <-ctx.Done():
		t.Fatal("load did not begin")
	}
	assertActive("initial load")
	loadOnce.Do(func() { close(loadGate) })
	select {
	case <-writing:
	case <-ctx.Done():
		t.Fatal("registration did not begin")
	}
	assertActive("durable registration")
	writeOnce.Do(func() { close(writeGate) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("submission stuck")
	}
	m.Stop("alice/owner")
	if err := m.WithWorktreeOwner("alice/owner", "alice/cleanup", func() error { return nil }); err != nil {
		t.Fatal("stopped handled owner never became reclaimable", err)
	}
}

func TestWorktreeCleanupWaitsForStoppedOwnerDrain(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=handled")
	if err := store.AddWorker("alice", yesmen.Worker{Name: "owner", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	gate, blocked := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(gate) })
	var writes atomic.Int32
	m.cfg.deliveryWrite = func(path string, data []byte) error {
		if writes.Add(1) == 2 {
			close(blocked)
			<-gate
		}
		return writeDeliveryFile(path, data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := m.Submit(ctx, "alice/owner", deliveryTestID(912), "finished review", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("status writer did not block")
	}
	done := make(chan error, 1)
	go func() { done <- m.StopYesmanContext(ctx, "alice") }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		a := m.agents["alice/owner"]
		stopped := a.retiring && a.proc == nil && a.deliverySealed
		m.mu.Unlock()
		if stopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stop did not reach drain barrier")
		}
		time.Sleep(time.Millisecond)
	}
	if err := m.WithWorktreeOwner("alice/owner", "alice/cleanup", func() error { t.Error("cleanup raced retiring writer"); return nil }); err == nil {
		t.Fatal("draining owner was treated as idle")
	}
	release.Do(func() { close(gate) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("stop remained blocked")
	}
	if err := m.WithWorktreeOwner("alice/owner", "alice/cleanup", func() error { return nil }); err != nil {
		t.Fatal("fully drained owner stayed protected", err)
	}
}
