package agent

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func TestHumanSteerPreparingAcrossTakeAndStop(t *testing.T) {
	for _, scenario := range []string{"merge", "take", "stop"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPromotionFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			idA, idB := deliveryTestID(91001), deliveryTestID(91002)
			aImage, bImage := testPNG(255, 0, 0), testPNG(0, 0, 255)
			a, err := f.m.Submit(ctx, f.info.ID, idA, "first", []Image{aImage}, f.info.Since)
			if err != nil || !a.CanCancel || a.State != "steering" {
				t.Fatalf("first was not admitted to the native steer slot: %+v %v", a, err)
			}
			done := make(chan Delivery, 1)
			go func() {
				d, _ := f.m.Submit(ctx, f.info.ID, idB, "fixture-delay", []Image{bImage}, f.info.Since)
				done <- d
			}()
			waitFor(t, "B running actual input hook as steer", func() bool {
				b, _ := os.ReadFile(f.gate + ".input-entered")
				return string(b) == "steer\n"
			})
			if items := f.pendingItems(); len(items) != 1 || items[0].QueueID != a.queueID || items[0].Preparing {
				t.Fatalf("B's hook disturbed A: %+v", items)
			}
			if scenario == "take" {
				f.release(t)
				f.awaitProvider(t, "first")
			}
			if scenario == "stop" {
				if err = f.m.Abort(ctx, f.info.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err = os.WriteFile(f.gate+".input-release", nil, 0600); err != nil {
				t.Fatal(err)
			}
			var b Delivery
			select {
			case b = <-done:
			case <-ctx.Done():
				t.Fatal("input did not finish")
			}
			switch scenario {
			case "merge":
				if b.MergedInto != idA || b.State != "steering" {
					t.Fatalf("prepared B did not join A: %+v", b)
				}
				result, err := f.m.CancelSubmission(ctx, f.info.ID, idA, f.info.Since)
				if err != nil || result.Payload.Text != "first\nfixture-delay" || !reflect.DeepEqual(result.Payload.Images, []Image{aImage, bImage}) {
					t.Fatalf("recall did not restore raw inputs: %+v %v", result, err)
				}
			case "take":
				calls, _ := f.awaitProvider(t, "first", "fixture-delay transformed")
				if b.MergedInto != "" || len(introduced(calls, "first")) != 1 || len(introduced(calls, "fixture-delay transformed")) != 1 {
					t.Fatalf("take and append did not linearize: %+v", b)
				}
			case "stop":
				if b.State != "failed" || !strings.Contains(b.Error, "stopped") || len(f.pendingItems()) != 1 {
					t.Fatalf("late B crossed Stop: %+v", b)
				}
				if a := f.receipt(idA); !a.CanCancel || !a.NativePending {
					t.Fatalf("Stop discarded A: %+v", a)
				}
			}
		})
	}
}

func TestHumanSteerPreservesResourcePreparation(t *testing.T) {
	f := newPromotionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	idA, idB := deliveryTestID(95001), deliveryTestID(95002)
	skill := "/skill:fixture-steer sample arguments"
	a, err := f.m.Submit(ctx, f.info.ID, idA, skill, nil, f.info.Since)
	if err != nil || a.State != "steering" || !a.CanCancel {
		t.Fatalf("skill queue: %+v %v", a, err)
	}
	templ := `/fixture-expand "two words"`
	b, err := f.m.Submit(ctx, f.info.ID, idB, templ, nil, f.info.Since)
	if err != nil || b.State != "failed" || !b.CanRecover || len(f.pendingItems()) != 1 {
		t.Fatalf("incompatible command: %+v %v", b, err)
	}
	if d, err := f.m.Submit(ctx, f.info.ID, deliveryTestID(95003), "fixture-ping", nil, f.info.Since); err != nil || d.State != "handled" || len(f.pendingItems()) != 1 {
		t.Fatalf("handled input changed the slot: %+v %v", d, err)
	}
	f.release(t)
	waitFor(t, "expanded skill reached the provider", func() bool {
		calls, _ := f.providerUsers(t)
		for _, call := range calls {
			for _, user := range call {
				text := user.text()
				if strings.Contains(text, `<skill name="fixture-steer"`) && strings.Contains(text, "Fixture skill body.") && strings.HasSuffix(text, "sample arguments") {
					return true
				}
			}
		}
		return false
	})
	for i, text := range []string{templ, "fixture-hook-error"} {
		waitFor(t, "idle before the next prompt", func() bool { a, _ := f.m.Get(f.info.ID); return a.Status == Idle })
		if d, err := f.m.Submit(ctx, f.info.ID, deliveryTestID(95004+i), text, nil, f.info.Since); err != nil || d.State == "failed" {
			t.Fatalf("original Pi input semantics changed: %+v %v", d, err)
		}
	}
	f.awaitProvider(t, "Fixture template argument two words", "fixture-hook-error")
}

func TestHumanSteerSlotsArePerAgent(t *testing.T) {
	f := newPromotionFixture(t)
	worker := yesmen.Worker{Name: "worker", StartedAt: time.Now().UTC()}
	if err := f.store.AddWorker("alice", worker); err != nil {
		t.Fatal(err)
	}
	other, err := f.m.Get("alice/" + worker.Name)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if d, err := f.m.Submit(ctx, other.ID, deliveryTestID(93000), "worker hold", nil, other.Since); err != nil || d.State == "failed" {
		t.Fatalf("worker seed: %+v %v", d, err)
	}
	waitFor(t, "worker reached the provider", func() bool {
		_, raw := f.providerUsers(t)
		return strings.Contains(raw, "worker hold")
	})
	for i, agent := range []Info{f.info, other} {
		base := 93001 + i*2
		a, err := f.m.Submit(ctx, agent.ID, deliveryTestID(base), agent.Name+" A", nil, agent.Since)
		if err != nil || !a.CanCancel {
			t.Fatalf("%s first slot: %+v %v", agent.ID, a, err)
		}
		b, err := f.m.Submit(ctx, agent.ID, deliveryTestID(base+1), agent.Name+" B", nil, agent.Since)
		if err != nil || b.MergedInto != a.ID {
			t.Fatalf("%s merge: %+v %v", agent.ID, b, err)
		}
		result, err := f.m.CancelSubmission(ctx, agent.ID, a.ID, agent.Since)
		if err != nil || result.Payload.Text != agent.Name+" A\n"+agent.Name+" B" {
			t.Fatalf("%s recall: %+v %v", agent.ID, result, err)
		}
	}
}
