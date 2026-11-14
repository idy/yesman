package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func largeDeliveryImage(t *testing.T) Image {
	t.Helper()
	image := testImage()
	raw, err := base64.StdEncoding.DecodeString(image.Data)
	if err != nil {
		t.Fatal(err)
	}
	image.Data = base64.StdEncoding.EncodeToString(append(raw, make([]byte, 1<<20)...))
	return image
}
func flushDeliveries(t *testing.T, m *Manager, id string) {
	t.Helper()
	m.mu.Lock()
	a := m.agents[id]
	writer, revision := a.deliveryWriter, a.deliveryVersion
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := writer.wait(ctx, revision, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryConcurrentRegistrationIsDurableBeforeReturn(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer")
	gate, blocked := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(gate) })
	var writes, prompts atomic.Int32
	m.cfg.deliveryWrite = func(path string, data []byte) error {
		if writes.Add(1) == 1 {
			close(blocked)
			<-gate
		}
		return writeDeliveryFile(path, data)
	}
	m.cfg.OnEvent = func(e Event) {
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(e.Data, &head)
		if head.Type == "test_prompt_received" {
			prompts.Add(1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := deliveryTestID(90)
	results := make(chan error, 32)
	for i := 0; i < 32; i++ {
		go func() {
			d, err := m.Submit(ctx, "alice/lead", id, "one intent", []Image{testImage()})
			if err == nil && d.ID != id {
				err = fmt.Errorf("wrong identity")
			}
			results <- err
		}()
	}
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("registration writer did not gate")
	}
	select {
	case err := <-results:
		t.Fatalf("registration returned before durability: %v", err)
	default:
	}
	if m.running("alice/lead") != nil {
		t.Fatal("RPC began before registration was durable")
	}
	release.Do(func() { close(gate) })
	for i := 0; i < 32; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("concurrent registration stuck")
		}
	}
	if prompts.Load() != 1 {
		t.Fatalf("duplicate prompt dispatches: %d", prompts.Load())
	}
}

func TestDeliveryDurabilitySurvivesLaterStatusWriteFailure(t *testing.T) {
	var attempts atomic.Int32
	writer := newDeliveryWriter("fixture", func(string, []byte) error {
		if attempts.Add(1) == 2 {
			return fmt.Errorf("later status failure")
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer writer.close(ctx)
	writer.request(1, deliveryDisk{Version: 2})
	if err := writer.wait(ctx, 1, nil); err != nil {
		t.Fatal(err)
	}
	writer.request(2, deliveryDisk{Version: 2})
	if err := writer.wait(ctx, 2, nil); err == nil {
		t.Fatal("expected controlled status write failure")
	}
	if err := writer.wait(ctx, 1, nil); err != nil {
		t.Fatalf("durable registration was forgotten: %v", err)
	}
}

func TestDeliverySlowPersistenceDoesNotBlockReaderOrOtherAgents(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer")
	gate, blocked := make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(gate) })
	var aliceWrites atomic.Int32
	var largest atomic.Int64
	m.cfg.deliveryWrite = func(path string, data []byte) error {
		for {
			old := largest.Load()
			if int64(len(data)) <= old || largest.CompareAndSwap(old, int64(len(data))) {
				break
			}
		}
		if strings.Contains(path, filepath.Join("alice", ".yesman")) && aliceWrites.Add(1) == 2 {
			close(blocked)
			<-gate
		}
		return writeDeliveryFile(path, data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	images := []Image{largeDeliveryImage(t), largeDeliveryImage(t)}
	id, messageID := "alice/lead", deliveryTestID(100)
	d, err := m.Submit(ctx, id, messageID, "two large images", images)
	if err != nil || d.State != "steering" {
		t.Fatalf("submit %+v %v", d, err)
	}
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("status writer did not reach gate")
	}
	// The status writer is still blocked. These require the same pi reader and
	// the global manager lock that synchronous full-history persistence held.
	if _, err = m.running(id).call(ctx, map[string]any{"type": "test_settle"}); err != nil {
		t.Fatal(err)
	}
	deliveryState(t, m, id, messageID, "unconfirmed")
	conv, err := m.Messages(ctx, id)
	if err != nil || len(conv.Deliveries) != 1 {
		t.Fatalf("reader blocked: %+v %v", conv, err)
	}
	if _, err = store.Hire("bob"); err != nil {
		t.Fatal(err)
	}
	other, err := m.Submit(ctx, "bob/lead", deliveryTestID(101), "independent", nil)
	if err != nil || other.State != "steering" {
		t.Fatalf("other agent blocked: %+v %v", other, err)
	}
	if _, err = m.Get("bob/lead"); err != nil {
		t.Fatal(err)
	}
	if largest.Load() > 4096 {
		t.Fatalf("attachment bytes entered metadata writes: %d", largest.Load())
	}
	t.Logf("reader RPC, snapshot, and bob submission completed while alice writer was gated; largest metadata write=%d bytes for >2 MiB image payload", largest.Load())
	release.Do(func() { close(gate) })
	flushDeliveries(t, m, id)
	m.mu.Lock()
	path := m.agents[id].deliveryWriter.path
	m.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk deliveryDisk
	if err = json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Records) != 1 || disk.Records[0].State != "unconfirmed" || disk.Records[0].ImageCount != 2 || len(disk.Records[0].Images) != 0 {
		t.Fatalf("stale or image-bearing snapshot: %+v", disk)
	}
}

func TestDeliveryRetentionAndPayloadBounds(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=handled")
	var calls atomic.Int32
	m.cfg.OnEvent = func(e Event) {
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(e.Data, &head)
		if head.Type == "test_prompt_received" {
			calls.Add(1)
		}
	}
	var diskMu sync.Mutex
	var saved []byte
	var largest int
	m.cfg.deliveryWrite = func(_ string, data []byte) error {
		diskMu.Lock()
		defer diskMu.Unlock()
		saved = append(saved[:0], data...)
		if len(data) > largest {
			largest = len(data)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	image := largeDeliveryImage(t)
	text := strings.Repeat("large preview ", 400)
	base := time.Now().Add(-time.Hour).UnixMilli()
	ids := make([]string, maxDeliveryRecords+16)
	for i := range ids {
		ids[i] = fmt.Sprintf("%012x%036x", base+int64(i), i+1)
		var images []Image
		if i < 16 {
			images = []Image{image, image}
		}
		if _, err := m.Submit(ctx, "alice/lead", ids[i], text, images); err != nil {
			t.Fatal(err)
		}
	}
	flushDeliveries(t, m, "alice/lead")
	m.mu.Lock()
	a := m.agents["alice/lead"]
	count, floor := len(a.deliveries), a.deliveryFloor
	receipts := a.deliverySnapshot()
	m.mu.Unlock()
	if count != maxDeliveryRecords || floor == 0 {
		t.Fatalf("records were not bounded: %d floor=%d", count, floor)
	}
	for _, d := range receipts {
		if len(d.Text) > maxDeliveryPreview || len(d.Images) != 0 || len(d.Error) > maxDeliveryError {
			t.Fatal("unbounded receipt payload")
		}
	}
	before := calls.Load()
	if _, err := m.Submit(ctx, "alice/lead", ids[0], text, []Image{image, image}); err == nil || !strings.Contains(err.Error(), "no longer retained") {
		t.Fatalf("evicted request could dispatch: %v", err)
	}
	last := ids[len(ids)-1]
	if _, err := m.Submit(ctx, "alice/lead", last, text, nil); err != nil {
		t.Fatalf("retained retry failed: %v", err)
	}
	expired := fmt.Sprintf("%012x%036x", time.Now().Add(-deliveryRetention-time.Second).UnixMilli(), 1)
	if _, err := m.Submit(ctx, "alice/lead", expired, "expired", nil); err == nil {
		t.Fatal("expired ID accepted")
	}
	if calls.Load() != before {
		t.Fatal("unknown old/duplicate ID dispatched")
	}
	diskMu.Lock()
	defer diskMu.Unlock()
	if largest > maxDeliveryFile || len(saved) > maxDeliveryFile {
		t.Fatal("persisted metadata byte limit exceeded")
	}
	if strings.Contains(string(saved), image.Data[:128]) {
		t.Fatal("full attachment persisted")
	}
	t.Logf("%d submissions (16 with two 1 MiB images): %d retained metadata records; largest write=%d bytes; evicted retry dispatched zero prompts", len(ids), count, largest)
}

func TestDeliveryPendingLimitDoesNotClearQueue(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer")
	m.cfg.deliveryWrite = func(string, []byte) error { return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for i := 0; i < maxPendingDeliveries; i++ {
		if _, err := m.Submit(ctx, "alice/lead", deliveryTestID(1000+i), "queued", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Submit(ctx, "alice/lead", deliveryTestID(9999), "over limit", nil); err == nil {
		t.Fatal("pending capacity ignored")
	}
	conv, err := m.Messages(ctx, "alice/lead")
	if err != nil || len(conv.Deliveries) != maxPendingDeliveries || conv.Queue.Steering != maxPendingDeliveries {
		t.Fatalf("limit altered native queue: %+v %v", conv.Queue, err)
	}
}

func TestDeliveryLegacyMigrationAndExpiry(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer")
	info, _ := m.Get("alice/lead")
	workspace, _ := store.Get("alice")
	path := filepath.Join(workspace.SessionDir("lead"), "delivery.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	image := largeDeliveryImage(t)
	text := strings.Repeat("legacy body ", 400)
	oldID := strings.Repeat("a", 48)
	legacy := []Delivery{{ID: oldID, Text: text, Images: []Image{image}, State: "steering", Revision: 3, Order: 1, AgentSince: info.Since}}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conv, err := m.Messages(ctx, "alice/lead")
	if err != nil || len(conv.Deliveries) != 1 {
		t.Fatalf("migration %+v %v", conv, err)
	}
	migrated := conv.Deliveries[0]
	if migrated.State != "interrupted" || migrated.ImageCount != 1 || len(migrated.Images) != 0 || len(migrated.Text) > maxDeliveryPreview {
		t.Fatalf("legacy payload retained: %+v", migrated)
	}
	if duplicate, err := m.Submit(ctx, "alice/lead", oldID, text, []Image{image}); err != nil || duplicate.State != "interrupted" {
		t.Fatalf("legacy retry lost identity: %+v %v", duplicate, err)
	}
	flushDeliveries(t, m, "alice/lead")
	compact, err := os.ReadFile(path)
	if err != nil || len(compact) > 4096 {
		t.Fatalf("legacy file not compacted: %d %v", len(compact), err)
	}
	expired, floor, err := readDeliveries(path, time.Now().Add(deliveryRetention+time.Hour))
	if err != nil || len(expired) != 0 || floor == 0 || floor > time.Now().Add(deliveryRetention+2*time.Hour).UnixMilli() {
		t.Fatalf("expiry watermark invalid: records=%d floor=%d %v", len(expired), floor, err)
	}
}

func TestDeliveryInitialWriteFailureNeverDispatches(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer")
	m.cfg.deliveryWrite = func(string, []byte) error { return fmt.Errorf("fixture storage unavailable") }
	id := deliveryTestID(501)
	receipt, err := m.Submit(context.Background(), "alice/lead", id, "not dispatched", nil)
	if err != nil || receipt.State != "failed" || m.running("alice/lead") != nil {
		t.Fatalf("failed registration dispatched: %+v %v", receipt, err)
	}
	duplicate, err := m.Submit(context.Background(), "alice/lead", id, "not dispatched", nil)
	if err != nil || duplicate.State != "failed" || m.running("alice/lead") != nil {
		t.Fatalf("failed registration retry dispatched: %+v %v", duplicate, err)
	}
}

// The browser fixture consumes actual Go JSON ordering rather than rebuilding
// Image or Delivery objects in JavaScript (the previous mock missed that bug).
func TestDeliveryInterruptedAfterNativeUserKeepsID(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer", "FAKE_PI_DELIVERY_LATE_ACK=1", "FAKE_PI_EXIT_AFTER_USER=1")
	var prompts, users atomic.Int32
	m.cfg.OnEvent = func(e Event) {
		var record struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(e.Data, &record) == nil {
			if record.Type == "test_prompt_received" {
				prompts.Add(1)
			}
			if record.Type == "message_end" {
				var message struct {
					Role string `json:"role"`
				}
				if json.Unmarshal(record.Message, &message) == nil && message.Role == "user" {
					users.Add(1)
				}
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := deliveryTestID(510)
	done := make(chan Delivery, 1)
	go func() {
		d, _ := m.Submit(ctx, "alice/lead", id, "may have been processed", []Image{testImage()})
		done <- d
	}()
	waitDeliveryNativeQueue(t, m, "alice/lead")
	_, _ = m.running("alice/lead").call(ctx, map[string]any{"type": "test_consume"})
	var d Delivery
	select {
	case d = <-done:
	case <-ctx.Done():
		t.Fatal("native user/exit did not release the prompt call")
	}
	if d.State != "interrupted" && d.State != "unconfirmed" {
		t.Fatalf("unknown outcome became definite rejection: %+v", d)
	}
	for i := 0; i < 3; i++ {
		retry, err := m.Submit(ctx, "alice/lead", id, "may have been processed", []Image{testImage()})
		if err != nil || retry.ID != id || retry.State == "failed" {
			t.Fatalf("unknown retry: %+v %v", retry, err)
		}
	}
	if prompts.Load() != 1 || users.Load() != 1 {
		t.Fatalf("unknown retry dispatched: prompts=%d users=%d", prompts.Load(), users.Load())
	}
}

func TestDeliveryBridgeFixture(t *testing.T) {
	path := os.Getenv("YESMAN_TEST_DELIVERY_DTO")
	if path == "" {
		t.Skip("browser fixture generator")
	}
	image := testImage()
	image.Data = "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg=="
	fixture := struct {
		Images  []Image  `json:"images"`
		Receipt Delivery `json:"receipt"`
	}{[]Image{image}, Delivery{ID: deliveryTestID(700), State: "interrupted", Text: "image retry", ImageCount: 1, Revision: 2, Order: 1}}
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
