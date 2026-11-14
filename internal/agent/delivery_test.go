package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Separate RPC acceptance, native queue snapshots, actual user events, and
// settle. The fake never receives the application's receipt ID, matching pi's
// native protocol. Identical text/images can be consumed out of order without
// giving the manager enough evidence to label either receipt consumed.
func fakeDeliveryPi() {
	type command struct {
		ID       string  `json:"id"`
		Type     string  `json:"type"`
		Message  string  `json:"message"`
		Images   []Image `json:"images"`
		Behavior string  `json:"streamingBehavior"`
		Index    int     `json:"index"`
	}
	var queue []command
	var messages []any
	session := "fixture-session"
	write := func(v any) { data, _ := json.Marshal(v); os.Stdout.Write(append(data, '\n')) }
	response := func(c command, data any) {
		write(map[string]any{"type": "response", "id": c.ID, "command": c.Type, "success": true, "data": data})
	}
	mode := os.Getenv("FAKE_PI_DELIVERY_MODE")
	queueUpdate := func() {
		steering, followUp := []string{}, []string{}
		for _, c := range queue {
			if mode == "followUp" {
				followUp = append(followUp, c.Message)
			} else if mode == "steer" {
				steering = append(steering, c.Message)
			}
		}
		write(map[string]any{"type": "queue_update", "steering": steering, "followUp": followUp})
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 20<<20)
	for scanner.Scan() {
		var c command
		if json.Unmarshal(scanner.Bytes(), &c) != nil {
			continue
		}
		switch c.Type {
		case "get_state":
			response(c, map[string]any{"model": Model{ID: "m", Provider: "p"}, "sessionId": session})
		case "get_messages":
			response(c, map[string]any{"messages": messages})
		case "prompt":
			write(map[string]any{"type": "test_prompt_received", "message": c.Message, "images": c.Images, "streamingBehavior": c.Behavior})
			if os.Getenv("FAKE_PI_FAIL_PROMPTS") != "" {
				write(map[string]any{"type": "response", "id": c.ID, "command": c.Type, "success": false, "error": "rejected"})
				continue
			}
			if mode == "handled" {
				response(c, map[string]any{"disposition": "handled"})
				continue
			}
			queue = append(queue, c)
			disposition := "started"
			if mode != "started" {
				queueUpdate()
				disposition = "queued"
			}
			if os.Getenv("FAKE_PI_DELIVERY_LATE_ACK") == "" {
				response(c, map[string]any{"disposition": disposition})
			}
		case "test_consume":
			if c.Index < len(queue) {
				q := queue[c.Index]
				queue = append(queue[:c.Index], queue[c.Index+1:]...)
				queueUpdate()
				content := []any{map[string]any{"type": "text", "text": q.Message}}
				for _, im := range q.Images {
					content = append(content, im)
				}
				message := map[string]any{"role": "user", "content": content, "timestamp": 123}
				write(map[string]any{"type": "message_start", "message": message})
				write(map[string]any{"type": "message_end", "message": message})
				messages = append(messages, message)
				if os.Getenv("FAKE_PI_EXIT_AFTER_USER") != "" {
					return
				}
				if os.Getenv("FAKE_PI_DELIVERY_LATE_ACK") != "" {
					response(q, map[string]any{"disposition": "queued"})
				}
			}
			response(c, nil)
		case "clear_queue":
			queue = nil
			queueUpdate()
			response(c, nil)
		case "abort":
			write(map[string]any{"type": "agent_settled", "aborted": true})
			response(c, nil)
		case "test_settle":
			write(map[string]any{"type": "agent_settled", "aborted": false})
			response(c, nil)
		case "test_switch_session":
			session = "next-session"
			queue = nil
			response(c, nil)
		case "test_exit":
			return
		default:
			response(c, nil)
		}
	}
}

func deliveryTestID(n int) string { return fmt.Sprintf("%012x%036x", time.Now().UnixMilli(), n) }

// Synchronize against pi's actual queue_update, never Idle or a receipt's
// Sending state: both can precede dispatch while modelMu/input work is pending.
func waitDeliveryNativeQueue(t *testing.T, m *Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		a := m.agents[id]
		q := QueueStatus{}
		if a != nil {
			q = a.queue
		}
		m.mu.Unlock()
		if q.Known && q.Steering+q.FollowUp > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("pi did not report a queued prompt before the native consumption step")
}

func deliveryState(t *testing.T, m *Manager, id, messageID, want string) Delivery {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		d := m.agents[id].deliveries[messageID]
		m.mu.Unlock()
		if d.State == want {
			return d
		}
		time.Sleep(time.Millisecond)
	}
	m.mu.Lock()
	d := m.agents[id].deliveries[messageID]
	m.mu.Unlock()
	t.Fatalf("receipt = %+v, want %s", d, want)
	return d
}

func TestDeliveryAcceptanceNeverClaimsConsumption(t *testing.T) {
	for _, mode := range []string{"steer", "followUp", "started"} {
		t.Run(mode, func(t *testing.T) {
			m, _ := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE="+mode)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			received := make(chan struct {
				Text     string  `json:"message"`
				Images   []Image `json:"images"`
				Behavior string  `json:"streamingBehavior"`
			}, 4)
			m.cfg.OnEvent = func(e Event) {
				var v struct {
					Type     string  `json:"type"`
					Text     string  `json:"message"`
					Images   []Image `json:"images"`
					Behavior string  `json:"streamingBehavior"`
				}
				if json.Unmarshal(e.Data, &v) == nil && v.Type == "test_prompt_received" {
					received <- struct {
						Text     string  `json:"message"`
						Images   []Image `json:"images"`
						Behavior string  `json:"streamingBehavior"`
					}{v.Text, v.Images, v.Behavior}
				}
			}
			id := "alice/lead"
			ids := []string{deliveryTestID(1), deliveryTestID(2), deliveryTestID(3)}
			state := map[string]string{"steer": "steering", "followUp": "queued", "started": "accepted"}[mode]
			behavior := "steer"
			if mode == "followUp" {
				behavior = "followUp"
			}
			originalText := strings.Repeat("同文🌱", 200)
			for i, messageID := range ids {
				text := originalText
				if i == 2 {
					text = ""
				}
				images := []Image{testImage(), testImage()}
				d, err := m.submit(ctx, id, messageID, text, images, behavior)
				if err != nil || d.State != state {
					t.Fatalf("submit %+v %v", d, err)
				}
				if len(d.Text) > maxDeliveryPreview || !utf8.ValidString(d.Text) || d.ImageCount != 2 || len(d.Images) != 0 {
					t.Fatal("receipt retained the original input payload")
				}
				got := <-received
				if got.Text != text || got.Behavior != behavior || !reflect.DeepEqual(got.Images, images) {
					t.Fatalf("native payload changed: %+v", got)
				}
			}
			conv, err := m.Messages(ctx, id)
			if err != nil || len(conv.Deliveries) != 3 || string(conv.Messages) != "null" {
				t.Fatalf("ack became history: %+v %v", conv, err)
			}
			if mode != "started" && (!conv.Queue.Known || conv.Queue.Steering+conv.Queue.FollowUp != 3) {
				t.Fatalf("queue snapshot: %+v", conv.Queue)
			}
			p := m.running(id)
			// Consume a duplicate second input first, retaining two native queue items.
			if _, err = p.call(ctx, map[string]any{"type": "test_consume", "index": 1}); err != nil {
				t.Fatal(err)
			}
			for _, messageID := range ids {
				deliveryState(t, m, id, messageID, state)
			}
			conv, err = m.Messages(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			var history []map[string]any
			_ = json.Unmarshal(conv.Messages, &history)
			if len(history) != 1 || history[0]["yesmanDeliveryId"] != nil {
				t.Fatalf("invented transcript identity: %s", conv.Messages)
			}
			// A retried bridge request returns the original receipt, without dispatch.
			d, err := m.Submit(ctx, id, ids[0], originalText, []Image{testImage(), testImage()})
			if err != nil || d.State != state {
				t.Fatalf("duplicate: %+v %v", d, err)
			}
			if _, err = m.Submit(ctx, id, ids[0], "changed after an uncertain request", nil); err == nil {
				t.Fatal("same ID reused for a different payload")
			}
			select {
			case <-received:
				t.Fatal("duplicate RPC dispatched")
			default:
			}
			if _, err = p.call(ctx, map[string]any{"type": "abort"}); err != nil {
				t.Fatal(err)
			}
			for _, messageID := range ids {
				deliveryState(t, m, id, messageID, "unconfirmed")
			}
			conv, err = m.Messages(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "started" && conv.Queue.Steering+conv.Queue.FollowUp != 2 {
				t.Fatal("abort implicitly cleared native queue")
			}
			if _, err = p.call(ctx, map[string]any{"type": "clear_queue"}); err != nil {
				t.Fatal(err)
			}
			conv, err = m.Messages(ctx, id)
			if err != nil || !conv.Queue.Known || conv.Queue.Steering+conv.Queue.FollowUp != 0 {
				t.Fatalf("clear snapshot %+v %v", conv.Queue, err)
			}
		})
	}
}

func TestDeliveryLateAckExitRefreshAndIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer", "FAKE_PI_DELIVERY_LATE_ACK=1")
	id, messageID := "alice/lead", deliveryTestID(10)
	a, p, err := m.ensure(id)
	if err != nil {
		t.Fatal(err)
	}
	a.modelMu.Lock()
	locked := true
	defer func() {
		if locked {
			a.modelMu.Unlock()
		}
	}()
	done := make(chan Delivery, 1)
	go func() { d, _ := m.Submit(ctx, id, messageID, "same", nil); done <- d }()
	waitStatus(t, m, id, Idle)
	deliveryState(t, m, id, messageID, "sending")
	m.mu.Lock()
	queuedEarly := a.queue.Known && a.queue.Steering+a.queue.FollowUp > 0
	m.mu.Unlock()
	if queuedEarly {
		t.Fatal("model gate unexpectedly allowed native dispatch")
	}
	a.modelMu.Unlock()
	locked = false
	waitDeliveryNativeQueue(t, m, id)
	if _, err := p.call(ctx, map[string]any{"type": "test_consume"}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-done:
		if d.State != "unconfirmed" || d.Error != "" {
			t.Fatalf("native late ack did not confirm acceptance: %+v", d)
		}
	case <-ctx.Done():
		t.Fatal("prompt acceptance did not finish after native consume")
	}
	if _, err := store.Hire("bob"); err != nil {
		t.Fatal(err)
	}
	bobDone := make(chan Delivery, 1)
	go func() { d, _ := m.Submit(ctx, "bob/lead", messageID, "same", nil); bobDone <- d }()
	waitDeliveryNativeQueue(t, m, "bob/lead")
	m.Stop("bob/lead")
	bobState := ""
	select {
	case d := <-bobDone:
		if d.State != "interrupted" && !(d.State == "unconfirmed" && d.Error != "") {
			t.Fatalf("stopped unacknowledged prompt got definite outcome: %+v", d)
		}
		bobState = d.State
	case <-ctx.Done():
		t.Fatal("stopped prompt did not return")
	}
	deliveryState(t, m, "bob/lead", messageID, bobState)
	deliveryState(t, m, id, messageID, "unconfirmed")
	// Restore a pending receipt from disk as interrupted, never processed.
	m.StopAll()
	restored := New(Config{Pi: m.cfg.Pi, Extension: m.cfg.Extension, Env: m.cfg.Env, Yesmen: store})
	defer restored.StopAll()
	if _, err := restored.Messages(ctx, "bob/lead"); err != nil {
		t.Fatal(err)
	}
	deliveryState(t, restored, "bob/lead", messageID, bobState)
}

func TestDeliveryRejectedHandledSessionAndStaleAgent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, messageID := "alice/lead", deliveryTestID(14)
	for _, mode := range []string{"handled", "steer"} {
		t.Run(mode, func(t *testing.T) {
			m, _ := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE="+mode)
			info, _ := m.Get(id)
			if _, err := m.Submit(ctx, id, messageID, "stale", nil, info.Since.Add(time.Second)); err == nil {
				t.Fatal("stale agent accepted")
			}
			if m.running(id) != nil {
				t.Fatal("stale submission started pi")
			}
			d, err := m.Submit(ctx, id, messageID, "/original command", nil, info.Since)
			want := "handled"
			if mode == "steer" {
				want = "steering"
			}
			if err != nil || d.State != want {
				t.Fatalf("submit %+v %v", d, err)
			}
			if mode == "steer" {
				if _, err = m.running(id).call(ctx, map[string]any{"type": "test_switch_session"}); err != nil {
					t.Fatal(err)
				}
				if _, err = m.Messages(ctx, id); err != nil {
					t.Fatal(err)
				}
				deliveryState(t, m, id, messageID, "interrupted")
			}
		})
	}
	rejected, _ := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_FAIL_PROMPTS=1")
	d, err := rejected.Submit(ctx, id, messageID, "retry", nil)
	if err != nil || d.State != "failed" || !strings.Contains(d.Error, "rejected") {
		t.Fatalf("rejection %+v %v", d, err)
	}
	retry, err := rejected.Submit(ctx, id, deliveryTestID(15), "retry", nil)
	if err != nil || retry.ID == d.ID || retry.State != "failed" {
		t.Fatalf("retry %+v %v", retry, err)
	}
	crashed, _ := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer")
	_, err = crashed.Submit(ctx, id, messageID, "pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = crashed.running(id).call(ctx, map[string]any{"type": "test_exit"})
	deliveryState(t, crashed, id, messageID, "interrupted")
}
