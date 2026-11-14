package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// The native primitive is tested with real pi elsewhere. This controllable RPC
// peer holds mutation replies, allowing lifecycle interleavings without
// stopping a real process or sleeping through the production 15-second bound.
func fakeRecallLifecyclePi() {
	type cmd struct {
		ID, Type, Message, SubmissionID, Epoch, QueueID, StreamingBehavior string
		ExpectedRevision                                                   uint64
	}
	promotion := os.Getenv("FAKE_PI_PROMOTE_LIFECYCLE") == "1"
	var pending []pendingItem
	var held []cmd
	revision := uint64(0)
	write := func(v any) { b, _ := json.Marshal(v); _, _ = os.Stdout.Write(append(b, '\n')) }
	snapshot := func() pendingQueue {
		q := pendingQueue{Capability: 1, Epoch: "fixture-epoch", Revision: revision, Items: pending}
		if promotion {
			q.Capability, q.PromoteAllowed = 2, true
		}
		return q
	}
	reply := func(c cmd, data any) {
		write(map[string]any{"type": "response", "id": c.ID, "command": c.Type, "success": true, "data": data})
	}
	remove := func(id, state string) bool {
		for i, item := range pending {
			if item.QueueID == id {
				pending = append(pending[:i], pending[i+1:]...)
				revision++
				q := snapshot()
				item.State = state
				q.Changes = []pendingItem{item}
				write(map[string]any{"type": "queue_update", "pending": q})
				return true
			}
		}
		return false
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var c cmd
		_ = json.Unmarshal(scanner.Bytes(), &c)
		switch c.Type {
		case "get_state":
			reply(c, map[string]any{"sessionId": "fixture-session", "pendingQueue": snapshot(), "model": Model{ID: "m", Provider: "p"}})
		case "prompt":
			revision++
			kind := c.StreamingBehavior
			if kind == "" {
				kind = "steer"
			}
			item := pendingItem{QueueID: fmt.Sprintf("item-%d", revision), SubmissionID: c.SubmissionID, Kind: kind}
			pending = append(pending, item)
			q := snapshot()
			item.State = "pending"
			q.Changes = []pendingItem{item}
			write(map[string]any{"type": "queue_update", "pending": q})
			reply(c, map[string]string{"disposition": "queued"})
		case "cancel_queued_message", "promote_queued_message":
			held = append(held, c)
			event := "test_cancel_received"
			if c.Type == "promote_queued_message" {
				event = "test_promote_received"
			}
			write(map[string]string{"type": event, "queueId": c.QueueID})
		case "test_release_cancels", "test_release_mutations":
			for _, h := range held {
				outcome := "not_pending"
				if h.Type == "promote_queued_message" {
					for i := range pending {
						if pending[i].QueueID == h.QueueID {
							if pending[i].Kind == "steer" {
								outcome = "already_steering"
								break
							}
							pending[i].Kind = "steer"
							pending[i].ItemRevision++
							revision++
							outcome = "promoted"
							q := snapshot()
							item := pending[i]
							item.State = "pending"
							q.Changes = []pendingItem{item}
							write(map[string]any{"type": "queue_update", "pending": q})
							break
						}
					}
				} else if remove(h.QueueID, "cancelled") {
					outcome = "cancelled"
				}
				b, _ := json.Marshal(snapshot())
				data := map[string]any{}
				_ = json.Unmarshal(b, &data)
				data["outcome"] = outcome
				reply(h, data)
			}
			held = nil
			reply(c, nil)
		case "test_take":
			remove(c.QueueID, "taken")
			reply(c, nil)
		default:
			reply(c, map[string]any{"messages": []any{}})
		}
	}
}

func TestRecallReleasesWorkspaceWhileRPCWaits(t *testing.T) {
	for _, take := range []bool{false, true} {
		t.Run(fmt.Sprintf("take=%v", take), func(t *testing.T) {
			m, store := newTestManager(t, 0, "FAKE_PI_RECALL_LIFECYCLE=1")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			seen := make(chan struct{}, 4)
			m.cfg.OnEvent = func(e Event) {
				var h struct{ Type string }
				_ = json.Unmarshal(e.Data, &h)
				if h.Type == "test_cancel_received" {
					seen <- struct{}{}
				}
			}
			aID, bID := deliveryTestID(14000), deliveryTestID(14001)
			a, err := m.Submit(ctx, "alice/lead", aID, "first", nil)
			if err != nil || !a.CanCancel {
				t.Fatalf("submit: %+v %v", a, err)
			}
			done := make(chan error, 1)
			go func() { _, err := m.CancelSubmission(ctx, "alice/lead", aID, a.AgentSince); done <- err }()
			select {
			case <-seen:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			entered, allowArchive, archived := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			releaseArchive := func() { once.Do(func() { close(allowArchive) }) }
			t.Cleanup(releaseArchive)
			go func() {
				_, _ = store.Archive("alice", func(yesmen.Yesman) error {
					close(entered)
					<-allowArchive
					return errors.New("fixture keeps workspace")
				})
				close(archived)
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("Archive is blocked by the pending cancel RPC")
			}
			releaseArchive()
			select {
			case <-archived:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			submitted := make(chan Delivery, 1)
			go func() { d, _ := m.Submit(ctx, "alice/lead", bID, "second", nil); submitted <- d }()
			var b Delivery
			select {
			case b = <-submitted:
				if !b.CanCancel {
					t.Fatalf("other submission failed: %+v", b)
				}
			case <-time.After(time.Second):
				t.Fatal("same workspace submission is blocked by cancel")
			}
			second := make(chan RecallResult, 1)
			go func() { result, _ := m.CancelSubmission(ctx, "alice/lead", bID, b.AgentSince); second <- result }()
			select {
			case <-seen:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case <-done:
				t.Fatal("first cancel unexpectedly completed before release")
			default:
			}
			p := m.running("alice/lead")
			if take {
				if _, err = p.call(ctx, map[string]any{"type": "test_take", "queueId": a.queueID}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = p.call(ctx, map[string]any{"type": "test_release_cancels"}); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				if (err != nil) != take {
					t.Fatalf("cancel/consume winner wrong: %v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case result := <-second:
				if result.Delivery.State != "cancelled" || result.Payload.Text != "second" {
					t.Fatalf("second recall: %+v", result)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			m.StopAll()
			restored := New(m.cfg)
			defer restored.StopAll()
			retry, err := restored.Submit(ctx, "alice/lead", bID, "second", nil, b.AgentSince)
			if err != nil || retry.State != "cancelled" || restored.running("alice/lead") != nil {
				t.Fatalf("restart revived cancelled intent: %+v %v", retry, err)
			}
		})
	}
}
