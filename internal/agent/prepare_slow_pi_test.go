package agent

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Admission's 15s auth deadline must not become an implicit Stop for a run
// already accepted. Each isolated case crosses the real unchanged deadline;
// only production yesman.ts waits on the app API, not a third-party hook.
func TestIdlePreparationBeyondAdmissionDeadline(t *testing.T) {
	for _, action := range []string{"run", "stop", "cancel"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			gate := newPreparationGate()
			f := newPromotionFixtureWithContext(t, nil, gate.serve)
			t.Cleanup(gate.open)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			id := deliveryTestID(73000)
			text := "slow production context 中文\n original"
			images := []Image{testImage(), testImage()}
			var survivors []string
			if action != "run" {
				survivors = []string{deliveryTestID(73001), deliveryTestID(73002)}
			}
			for _, other := range survivors {
				if _, err := f.m.submit(ctx, f.info.ID, other, "other queued input", nil, "followUp", f.info.Since); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.m.submit(ctx, f.info.ID, id, text, images, "followUp", f.info.Since); err != nil {
				t.Fatal(err)
			}
			if err := f.m.Abort(ctx, f.info.ID); err != nil {
				t.Fatal(err)
			}
			prepareEventually(t, func() bool { return prepareQueue(f).PromoteAllowed })
			if err := os.WriteFile(f.gate, []byte("release"), 0600); err != nil {
				t.Fatal(err)
			}
			audit := prepareAudit(f)
			gate.armed.Store(true)
			if r, err := f.m.PromoteSubmission(ctx, f.info.ID, id, f.info.Since); err != nil || r.Outcome != "promoted" {
				t.Fatalf("admission: %+v %v", r, err)
			}
			select {
			case <-gate.entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			before := prepareReceipt(f, id)
			select {
			case <-time.After(16 * time.Second):
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			d := prepareReceipt(f, id)
			payload, err := f.m.SubmissionPayload(f.info.ID, id, f.info.Since)
			if !d.NativePending || !d.CanCancel || d.queueID != before.queueID || err != nil || payload.Text != text || !reflect.DeepEqual(payload.Images, images) {
				t.Fatalf("slow preparation lost original: %+v %v", d, err)
			}
			if prepareAudit(f) != audit {
				t.Fatal("provider ran before context was ready")
			}
			if r, err := f.m.PromoteSubmission(ctx, f.info.ID, id, f.info.Since); err != nil || r.Outcome != "already_steering" {
				t.Fatalf("duplicate admission: %+v %v", r, err)
			}
			if action == "stop" {
				generation := prepareQueue(f).ControlGeneration
				stopping := make(chan error, 1)
				go func() { stopping <- f.m.Abort(ctx, f.info.ID) }()
				prepareEventually(t, func() bool { return prepareQueue(f).ControlGeneration > generation })
				gate.open()
				if err := <-stopping; err != nil {
					t.Fatal(err)
				}
			} else if action == "cancel" {
				if _, err := f.m.CancelSubmission(ctx, f.info.ID, id, f.info.Since); err != nil {
					t.Fatal(err)
				}
				gate.open()
			} else {
				gate.open()
			}
			prepareEventually(t, func() bool { i, _ := f.m.Get(f.info.ID); return i.Status == Idle })
			d = prepareReceipt(f, id)
			f.mu.Lock()
			taken := 0
			for _, it := range f.taken {
				if it == id {
					taken++
				}
			}
			f.mu.Unlock()
			if action == "run" {
				if d.State != "dispatched" || prepareHistory(t, f.m, ctx, f.info.ID, id) != 1 || strings.Count(prepareAudit(f), "\n")-strings.Count(audit, "\n") != 1 || taken != 1 {
					t.Fatalf("accepted Send now abandoned/duplicated after slow context: %+v taken=%d", d, taken)
				}
			} else {
				r, err := f.m.CancelSubmission(ctx, f.info.ID, id, f.info.Since)
				if err != nil || r.Payload.Text != text || !reflect.DeepEqual(r.Payload.Images, images) || taken != 0 || prepareHistory(t, f.m, ctx, f.info.ID, id) != 0 || prepareAudit(f) != audit {
					t.Fatalf("late %s did not preserve exact cancellation: %+v %v taken=%d", action, r, err, taken)
				}
				var pending []string
				for _, it := range prepareQueue(f).Items {
					pending = append(pending, it.SubmissionID)
				}
				if !reflect.DeepEqual(pending, survivors) {
					t.Fatalf("other queue order changed: %v", pending)
				}
			}
			t.Logf("16s production context/%s: same pending original before commit; duplicate is no-op; exact intake or cancellation, survivors preserved", action)
		})
	}
}
