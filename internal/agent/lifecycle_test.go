package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

type archiveResult struct {
	path string
	err  error
}

func archiveManaged(ctx context.Context, m *Manager, store *yesmen.Store, name string) (string, error) {
	var since time.Time
	path, err := store.Archive(name, func(y yesmen.Yesman) error { since = y.HiredAt; return m.StopYesmanContext(ctx, y.Name) })
	if err == nil {
		m.ForgetYesman(name, since)
	}
	return path, err
}
func archivedDelivery(t *testing.T, path, id string) Delivery {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(path, ".yesman", "sessions", "lead", "delivery.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk deliveryDisk
	if err = json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	for _, d := range disk.Records {
		if d.ID == id {
			return d.Delivery
		}
	}
	t.Fatal("archived receipt missing")
	return Delivery{}
}
func awaitArchive(t *testing.T, ctx context.Context, result <-chan archiveResult) string {
	t.Helper()
	select {
	case r := <-result:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.path
	case <-ctx.Done():
		t.Fatal("archive did not finish after writer release")
		return ""
	}
}

func TestStopAllDoesNotInitializeUnscopedReceiptPaths(t *testing.T) {
	m, _ := newTestManager(t, 0)
	var reads, writes atomic.Int32
	m.cfg.deliveryRead = func(string, time.Time) (map[string]Delivery, int64, error) {
		reads.Add(1)
		return map[string]Delivery{}, 0, nil
	}
	m.cfg.deliveryWrite = func(string, []byte) error { writes.Add(1); return nil }
	m.mu.Lock()
	m.agents["alice/unscoped"] = &agent{info: Info{ID: "alice/unscoped", Yesman: "alice", Name: "unscoped", Role: RoleWorker}}
	m.mu.Unlock()
	m.StopAll()
	if reads.Load() != 0 || writes.Load() != 0 {
		t.Fatal("unscoped bookkeeping agent attempted relative receipt I/O")
	}
}

func TestDeliveryArchiveDrainsLastSnapshotBeforeRenameAndRehire(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer")
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	y, _ := store.Get("alice")
	id := deliveryTestID(801)
	if d, err := m.Submit(ctx, "alice/lead", id, "archive fixture", nil); err != nil || d.State != "steering" {
		t.Fatalf("submit %+v %v", d, err)
	}
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("second write was not gated")
	}
	m.mu.Lock()
	old := m.agents["alice/lead"]
	writer := old.deliveryWriter
	token := old.token
	m.mu.Unlock()
	result := make(chan archiveResult, 1)
	go func() { path, err := archiveManaged(ctx, m, store, "alice"); result <- archiveResult{path, err} }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		sealed := old.deliverySealed
		m.mu.Unlock()
		if sealed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("archive did not reach writer barrier")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case r := <-result:
		t.Fatalf("archive passed an in-flight write: %+v", r)
	default:
	}
	if _, err := os.Stat(y.Path); err != nil {
		t.Fatal("workspace moved before final metadata was durable")
	}
	// Global manager operations and a different workspace remain live.
	if _, err := store.Hire("bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Submit(ctx, "bob/lead", deliveryTestID(802), "independent while archive waits", nil); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(gate) })
	dest := awaitArchive(t, ctx, result)
	if !writer.finished() {
		t.Fatal("archive returned before writer goroutine exited")
	}
	if d := archivedDelivery(t, dest, id); d.State != "interrupted" {
		t.Fatalf("archive retained stale state: %+v", d)
	}
	if _, err := os.Stat(y.Path); !os.IsNotExist(err) {
		t.Fatalf("old workspace recreated after archive: %v", err)
	}
	fresh, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	info, err := m.Get("alice/lead")
	if err != nil || !info.Since.Equal(fresh.HiredAt) || info.Status != Stopped || info.Detail != "" {
		t.Fatalf("old agent leaked into new hire: %+v %v", info, err)
	}
	conv, err := m.Messages(ctx, "alice/lead")
	if err != nil || len(conv.Deliveries) != 0 {
		t.Fatalf("old receipts leaked: %+v %v", conv.Deliveries, err)
	}
	if _, err = m.Authenticate(token); err == nil {
		t.Fatal("archived process credential remained valid")
	}
	newID := deliveryTestID(803)
	if _, err = m.Submit(ctx, "alice/lead", newID, "fresh workspace intent", nil); err != nil {
		t.Fatal(err)
	}
	flushDeliveries(t, m, "alice/lead")
	m.ForgetYesman("alice", y.HiredAt) // delayed old service cleanup must not remove the new incarnation
	if current, err := m.Get("alice/lead"); err != nil || !current.Since.Equal(fresh.HiredAt) {
		t.Fatal("late cleanup removed the new hire")
	}
	// Simulate delayed old submission/model callbacks after the name is reused.
	m.mu.Lock()
	m.persistDeliveriesLocked(old)
	m.mu.Unlock()
	if err = m.promptDelivery(ctx, "alice/lead", "old intent", "steer", id, old, &y.HiredAt); !errors.Is(err, errStopped) {
		t.Fatalf("old submission could resume new workspace: %v", err)
	}
	if d := archivedDelivery(t, dest, id); d.State != "interrupted" {
		t.Fatal("archive changed after rehire")
	}
	current := archivedDelivery(t, fresh.Path, newID)
	if current.Text != "fresh workspace intent" {
		t.Fatal("old writer overwrote new workspace")
	}
}

func TestDeliveryArchiveFencesSubmitWaitingBeforeDispatch(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer")
	var prompts atomic.Int32
	m.cfg.OnEvent = func(e Event) {
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(e.Data, &head)
		if head.Type == "test_prompt_received" {
			prompts.Add(1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	old, _, err := m.ensure("alice/lead")
	if err != nil {
		t.Fatal(err)
	}
	old.modelMu.Lock()
	locked := true
	defer func() {
		if locked {
			old.modelMu.Unlock()
		}
	}()
	id := deliveryTestID(805)
	submitted := make(chan Delivery, 1)
	go func() { d, _ := m.Submit(ctx, "alice/lead", id, "stale before dispatch", nil); submitted <- d }()
	deliveryState(t, m, "alice/lead", id, "sending")
	flushDeliveries(t, m, "alice/lead")
	result := make(chan archiveResult, 1)
	go func() { path, err := archiveManaged(ctx, m, store, "alice"); result <- archiveResult{path, err} }()
	dest := awaitArchive(t, ctx, result) // must not wait for modelMu/the old caller
	if archivedDelivery(t, dest, id).State != "failed" {
		t.Fatal("undispatched input did not get a definitive stopped result")
	}
	if _, err = store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	newID := deliveryTestID(806)
	if _, err = m.Submit(ctx, "alice/lead", newID, "new generation", nil); err != nil {
		t.Fatal(err)
	}
	old.modelMu.Unlock()
	locked = false
	select {
	case d := <-submitted:
		if d.State != "failed" {
			t.Fatalf("old caller changed archive result: %+v", d)
		}
	case <-ctx.Done():
		t.Fatal("old caller did not leave its dispatch gate")
	}
	if prompts.Load() != 1 {
		t.Fatalf("old caller dispatched into the new workspace: %d", prompts.Load())
	}
}

func TestDeliveryWorkerRemovalCannotOutliveWorkspaceArchive(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer")
	if err := store.AddWorker("alice", yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id := deliveryTestID(808)
	if _, err := m.Submit(ctx, "alice/w1", id, "remove while archiving", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("writer gate not reached")
	}
	removed := make(chan error, 1)
	go func() { removed <- m.RemoveWorker("alice", "w1") }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		a := m.agents["alice/w1"]
		sealed := a != nil && a.deliverySealed
		m.mu.Unlock()
		if sealed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker removal did not seal metadata")
		}
		time.Sleep(time.Millisecond)
	}
	result := make(chan archiveResult, 1)
	go func() { path, err := archiveManaged(ctx, m, store, "alice"); result <- archiveResult{path, err} }()
	select {
	case r := <-result:
		t.Fatalf("archive passed worker removal writer: %+v", r)
	default:
	}
	release.Do(func() { close(gate) })
	select {
	case err := <-removed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("worker removal stuck")
	}
	dest := awaitArchive(t, ctx, result)
	data, err := os.ReadFile(filepath.Join(dest, ".yesman", "sessions", "w1", "delivery.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk deliveryDisk
	if err = json.Unmarshal(data, &disk); err != nil || len(disk.Records) != 1 || disk.Records[0].State != "interrupted" {
		t.Fatalf("removed worker archived stale metadata: %v %s", err, data)
	}
	if _, err = store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryArchiveWaitsForInitialLoadAndRegistration(t *testing.T) {
	for _, phase := range []string{"load", "registration"} {
		t.Run(phase, func(t *testing.T) {
			m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer")
			gate, blocked := make(chan struct{}), make(chan struct{})
			var release sync.Once
			defer release.Do(func() { close(gate) })
			var first sync.Once
			if phase == "load" {
				m.cfg.deliveryRead = func(path string, now time.Time) (map[string]Delivery, int64, error) {
					first.Do(func() { close(blocked); <-gate })
					return readDeliveries(path, now)
				}
			} else {
				m.cfg.deliveryWrite = func(path string, data []byte) error {
					first.Do(func() { close(blocked); <-gate })
					return writeDeliveryFile(path, data)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			y, _ := store.Get("alice")
			id := deliveryTestID(810)
			submitted := make(chan Delivery, 1)
			go func() { d, _ := m.Submit(ctx, "alice/lead", id, "old initial intent", nil); submitted <- d }()
			select {
			case <-blocked:
			case <-ctx.Done():
				t.Fatal("initialization gate not reached")
			}
			result := make(chan archiveResult, 1)
			entered := make(chan struct{})
			go func() {
				path, err := store.Archive("alice", func(current yesmen.Yesman) error { close(entered); return m.StopYesmanContext(ctx, current.Name) })
				result <- archiveResult{path, err}
			}()
			// While the initial load/write owns its Use lease, Archive cannot enter its
			// stopping callback. The positive outcome below also checks both race orders.
			select {
			case <-entered:
				t.Fatal("archive entered during initial workspace use")
			case <-time.After(20 * time.Millisecond):
			}
			release.Do(func() { close(gate) })
			dest := awaitArchive(t, ctx, result)
			if _, err := os.Stat(y.Path); !os.IsNotExist(err) {
				t.Fatal("initial submit recreated archived workspace")
			}
			fresh, err := store.Hire("alice")
			if err != nil {
				t.Fatal(err)
			}
			select {
			case d := <-submitted:
				if d.State == "sending" || d.State == "steering" || d.State == "accepted" {
					t.Fatalf("old submit returned stale archive result: %+v", d)
				}
			case <-ctx.Done():
				t.Fatal("old submit did not return")
			}
			saved := archivedDelivery(t, dest, id)
			if saved.State != "failed" && saved.State != "interrupted" && !(saved.State == "unconfirmed" && saved.Error != "") {
				t.Fatalf("initial archive state: %+v", saved)
			}
			conv, err := m.Messages(ctx, "alice/lead")
			if err != nil || len(conv.Deliveries) != 0 {
				t.Fatalf("new hire inherited initialization: %+v %v", conv.Deliveries, err)
			}
			info, _ := m.Get("alice/lead")
			if !info.Since.Equal(fresh.HiredAt) {
				t.Fatal("new workspace used old generation")
			}
		})
	}
}

func TestDeliveryArchiveWriteFailureAndTimeoutLeaveWorkspaceRecoverable(t *testing.T) {
	for _, mode := range []string{"write-error", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer")
			gate, blocked := make(chan struct{}), make(chan struct{})
			var release sync.Once
			defer release.Do(func() { close(gate) })
			var writes atomic.Int32
			var fail atomic.Bool
			fail.Store(mode == "write-error")
			m.cfg.deliveryWrite = func(path string, data []byte) error {
				n := writes.Add(1)
				if n == 2 {
					close(blocked)
					<-gate
				}
				if n >= 2 && fail.Load() {
					return fmt.Errorf("fixture final write failed")
				}
				return writeDeliveryFile(path, data)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			y, _ := store.Get("alice")
			id := deliveryTestID(820)
			if _, err := m.Submit(ctx, "alice/lead", id, "recover archive", nil); err != nil {
				t.Fatal(err)
			}
			select {
			case <-blocked:
			case <-ctx.Done():
				t.Fatal("writer gate not reached")
			}
			stopCtx := ctx
			var stopCancel context.CancelFunc
			if mode == "timeout" {
				stopCtx, stopCancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer stopCancel()
			}
			result := make(chan archiveResult, 1)
			go func() { path, err := archiveManaged(stopCtx, m, store, "alice"); result <- archiveResult{path, err} }()
			if mode == "write-error" {
				release.Do(func() { close(gate) })
			}
			select {
			case r := <-result:
				if r.err == nil || r.path != "" {
					t.Fatal("archive ignored final persistence failure")
				}
			case <-ctx.Done():
				t.Fatal("failed archive did not return")
			}
			if _, err := os.Stat(y.Path); err != nil {
				t.Fatal("failed archive moved workspace")
			}
			release.Do(func() { close(gate) })
			fail.Store(false)
			m.mu.Lock()
			oldWriter := m.agents["alice/lead"].deliveryWriter
			m.mu.Unlock()
			select {
			case <-oldWriter.done:
			case <-ctx.Done():
				t.Fatal("sealed writer did not exit")
			}
			if d, err := m.Submit(ctx, "alice/lead", id, "recover archive", nil); err != nil || d.State != "interrupted" {
				t.Fatalf("retry lost interrupted identity: %+v %v", d, err)
			}
			flushDeliveries(t, m, "alice/lead")
			m.StopAll()
			restored := New(Config{Pi: m.cfg.Pi, Extension: m.cfg.Extension, Env: m.cfg.Env, Yesmen: store})
			defer restored.StopAll()
			if d, err := restored.Submit(ctx, "alice/lead", id, "recover archive", nil); err != nil || d.State != "interrupted" || restored.running("alice/lead") != nil {
				t.Fatalf("restart redispatched unknown input: %+v %v", d, err)
			}
			dest, err := archiveManaged(ctx, restored, store, "alice")
			if err != nil {
				t.Fatal(err)
			}
			if archivedDelivery(t, dest, id).State != "interrupted" {
				t.Fatal("recovered archive lost final state")
			}
			if _, err = store.Hire("alice"); err != nil {
				t.Fatal("same-name hire remained blocked")
			}
		})
	}
}
