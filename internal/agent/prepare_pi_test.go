package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type preparationGate struct {
	armed            atomic.Bool
	entered, release chan struct{}
	once             sync.Once
}

func (g *preparationGate) serve() {
	if g.armed.Swap(false) {
		close(g.entered)
		<-g.release
	}
}
func (g *preparationGate) open() { g.once.Do(func() { close(g.release) }) }
func newPreparationGate() *preparationGate {
	return &preparationGate{entered: make(chan struct{}), release: make(chan struct{})}
}
func prepareEventually(t *testing.T, check func() bool) {
	t.Helper()
	end := time.Now().Add(8 * time.Second)
	for !check() {
		if time.Now().After(end) {
			t.Fatal("preparation fixture did not reach barrier")
		}
		time.Sleep(time.Millisecond)
	}
}
func prepareQueue(f *promotionFixture) pendingQueue {
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	return f.m.agents[f.info.ID].nativeQueue
}
func prepareReceipt(f *promotionFixture, id string) Delivery {
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	a := f.m.agents[f.info.ID]
	return a.deliveryView(a.deliveries[id])
}
func prepareAudit(f *promotionFixture) string {
	data, _ := os.ReadFile(filepath.Join(filepath.Dir(f.gate), "audit"))
	return string(data)
}
func prepareHistory(t *testing.T, m *Manager, ctx context.Context, agentID, id string) int {
	t.Helper()
	c, err := m.Messages(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	var messages []struct {
		Yesman *Origin `json:"yesman"`
	}
	if err = json.Unmarshal(c.Messages, &messages); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, msg := range messages {
		if msg.Yesman != nil && msg.Yesman.Submission == id {
			n++
		}
	}
	return n
}

// The sole asynchronous barrier is production yesman.ts's GET /v1/context.
// No third-party input/before_agent_start hook is installed.
func TestIdlePrepareProductionContextPreservesOriginal(t *testing.T) {
	for _, action := range []string{"stop", "cancel"} {
		t.Run(action, func(t *testing.T) {
			gate := newPreparationGate()
			f := newPromotionFixtureWithContext(t, nil, gate.serve)
			t.Cleanup(gate.open)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			first, target, last := deliveryTestID(72001), deliveryTestID(72002), deliveryTestID(72003)
			text := "  queued 中文\ncomplete original  "
			x := testImage()
			y := x
			y.Data = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
			images := []Image{y, x}
			for _, id := range []string{first, target, last} {
				if _, err := f.m.submit(ctx, f.info.ID, id, text, images, "followUp", f.info.Since); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.m.Abort(ctx, f.info.ID); err != nil {
				t.Fatal(err)
			}
			prepareEventually(t, func() bool {
				q := prepareQueue(f)
				i, _ := f.m.Get(f.info.ID)
				return i.Status == Idle && q.PromoteAllowed
			})
			before := prepareQueue(f)
			var original pendingItem
			for _, i := range before.Items {
				if i.SubmissionID == target {
					original = i
				}
			}
			audit := prepareAudit(f)
			gate.armed.Store(true)
			if result, err := f.m.PromoteSubmission(ctx, f.info.ID, target, f.info.Since); err != nil || result.Outcome != "promoted" {
				t.Fatalf("promote: %+v %v", result, err)
			}
			select {
			case <-gate.entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			d := prepareReceipt(f, target)
			if d.State != "steering" || !d.NativePending || !d.CanCancel || d.queueID != original.QueueID {
				t.Fatalf("preparation took or hid original: %+v", d)
			}
			full, err := f.m.SubmissionPayload(f.info.ID, target, f.info.Since)
			if err != nil || full.Text != text || !reflect.DeepEqual(full.Images, images) {
				t.Fatalf("original changed: %+v %v", full, err)
			}
			if prepareHistory(t, f.m, ctx, f.info.ID, target) != 0 {
				t.Fatal("preparation invented history")
			}
			if info, _ := f.m.Get(f.info.ID); info.Status != Working || info.Detail != "" {
				t.Fatal("native preparation did not expose Stop without a false error")
			}
			if action == "stop" {
				stopping := make(chan error, 1)
				go func() { stopping <- f.m.Abort(ctx, f.info.ID) }()
				prepareEventually(t, func() bool { return prepareQueue(f).ControlGeneration > before.ControlGeneration })
				if !prepareReceipt(f, target).CanCancel {
					t.Fatal("Stop removed original before context returned")
				}
				gate.open()
				if err := <-stopping; err != nil {
					t.Fatal(err)
				}
			} else {
				// Discard the first reply, then recover the same durable cancellation.
				if _, err := f.m.CancelSubmission(ctx, f.info.ID, target, f.info.Since); err != nil {
					t.Fatal(err)
				}
				gate.open()
			}
			prepareEventually(t, func() bool { i, _ := f.m.Get(f.info.ID); return i.Status == Idle })
			if prepareAudit(f) != audit {
				t.Fatal("stopped/cancelled preparation called provider")
			}
			if prepareHistory(t, f.m, ctx, f.info.ID, target) != 0 {
				t.Fatal("stopped/cancelled original reached history")
			}
			for retry := 0; retry < 2; retry++ {
				r, err := f.m.CancelSubmission(ctx, f.info.ID, target, f.info.Since)
				if err != nil || r.Delivery.State != "cancelled" || r.Payload.Text != text || !reflect.DeepEqual(r.Payload.Images, images) {
					t.Fatalf("recall retry lost original: %+v %v", r, err)
				}
			}
			q := prepareQueue(f)
			var survivors []string
			for _, item := range q.Items {
				survivors = append(survivors, item.SubmissionID)
			}
			if !reflect.DeepEqual(survivors, []string{first, last}) {
				t.Fatalf("other queue order changed: %v", survivors)
			}
			if d, err := f.m.Submit(ctx, f.info.ID, target, text, images, f.info.Since); err != nil || d.State != "cancelled" {
				t.Fatalf("old intent revived: %+v %v", d, err)
			}
			edited := deliveryTestID(72004)
			if _, err := f.m.Submit(ctx, f.info.ID, edited, text+" edited", images, f.info.Since); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.gate, []byte("release"), 0600); err != nil {
				t.Fatal(err)
			}
			prepareEventually(t, func() bool { return prepareReceipt(f, last).State == "dispatched" })
			prepareEventually(t, func() bool { i, _ := f.m.Get(f.info.ID); return i.Status == Idle })
			if prepareHistory(t, f.m, ctx, f.info.ID, target) != 0 || prepareHistory(t, f.m, ctx, f.info.ID, edited) != 1 {
				t.Fatal("edited resubmission duplicated or original revived")
			}
			f.m.StopAll()
			again := New(f.cfg)
			t.Cleanup(again.StopAll)
			if d, err := again.Submit(ctx, f.info.ID, target, text, images, f.info.Since); err != nil || d.State != "cancelled" || d.CanRestore || d.CanCancel {
				t.Fatalf("restart revived old intent/payload: %+v %v", d, err)
			}
			if prepareHistory(t, again, ctx, f.info.ID, target) != 0 || prepareHistory(t, again, ctx, f.info.ID, edited) != 1 {
				t.Fatal("restart history changed")
			}
			t.Logf("%s at production context barrier: same handle/payload remained native pending; recover/retry/new intent/restart exact", action)
		})
	}
}
