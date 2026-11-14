package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// This fake deliberately separates command acceptance, queue changes, and user
// message events. Tests can consume identical inputs in either order by ID.
func fakeDeliveryPi() {
	type command struct {
		ID      string  `json:"id"`
		Type    string  `json:"type"`
		Message string  `json:"message"`
		Images  []Image `json:"images"`
	}
	var queue []command
	var messages []any
	write := func(v any) { data, _ := json.Marshal(v); os.Stdout.Write(append(data, '\n')) }
	response := func(c command, data any) {
		write(map[string]any{"type": "response", "id": c.ID, "command": c.Type, "success": true, "data": data})
	}
	mode := os.Getenv("FAKE_PI_DELIVERY_MODE")
	queueUpdate := func() {
		texts := []string{}
		for _, c := range queue {
			texts = append(texts, c.Message)
		}
		steering, followUp := []string{}, []string{}
		if mode == "followUp" {
			followUp = texts
		} else if mode == "steer" {
			steering = texts
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
			if os.Getenv("FAKE_PI_DELIVERY_NO_EXTENSION") == "" {
				write(map[string]any{"type": "extension_ui_request", "method": "setStatus", "statusKey": "yesman.delivery", "statusText": "{\"version\":1,\"session\":\"fixture-session\"}"})
			}
			response(c, map[string]any{"model": Model{ID: "m", Provider: "p"}})
		case "get_messages":
			response(c, map[string]any{"messages": messages})
		case "prompt":
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
			for i, q := range queue {
				if taggedDelivery(q.Message) != c.Message {
					continue
				}
				queue = append(queue[:i], queue[i+1:]...)
				queueUpdate()
				content := []any{map[string]any{"type": "text", "text": q.Message}}
				for _, im := range q.Images {
					content = append(content, im)
				}
				message := map[string]any{"role": "user", "content": content, "timestamp": 123}
				write(map[string]any{"type": "message_start", "message": message})
				// Equivalent to yesman's supported pi message_end transform.
				content[0] = map[string]any{"type": "text", "text": cleanDeliveryText(q.Message)}
				message["yesmanDeliveryId"] = taggedDelivery(q.Message)
				write(map[string]any{"type": "message_end", "message": message})
				messages = append(messages, message)
				if os.Getenv("FAKE_PI_DELIVERY_LATE_ACK") != "" {
					response(q, map[string]any{"disposition": "queued"})
				}
				break
			}
			response(c, nil)
		case "clear_queue":
			queue = nil
			queueUpdate()
			response(c, nil)
		case "abort":
			// pi abort does not clear its queues.
			write(map[string]any{"type": "agent_settled", "aborted": true})
			response(c, nil)
		case "test_settle":
			write(map[string]any{"type": "agent_settled", "aborted": false})
			response(c, nil)
		case "test_switch_session":
			write(map[string]any{"type": "extension_ui_request", "method": "setStatus", "statusKey": "yesman.delivery", "statusText": "{\"version\":1,\"session\":\"next-session\"}"})
			response(c, nil)
		case "test_exit":
			return
		default:
			response(c, nil)
		}
	}
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
	t.Fatalf("delivery %s = %+v, want %s", messageID, d, want)
	return d
}

func TestDeliveryAcceptanceConsumptionAndReload(t *testing.T) {
	for _, mode := range []string{"steer", "followUp", "started"} {
		t.Run(mode, func(t *testing.T) {
			m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE="+mode)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			id := "alice/lead"
			one, two, three := strings.Repeat("1", 48), strings.Repeat("2", 48), strings.Repeat("3", 48)
			state := map[string]string{"steer": "steering", "followUp": "queued", "started": "accepted"}[mode]
			for _, messageID := range []string{one, two, three} {
				text := "same"
				images := []Image{testImage(), testImage()}
				if messageID == three {
					text = ""
				}
				d, err := m.Submit(ctx, id, messageID, text, images)
				if err != nil || d.State != state {
					t.Fatalf("submit = %+v, %v", d, err)
				}
			}
			conv, err := m.Messages(ctx, id)
			if err != nil || len(conv.Deliveries) != 3 || string(conv.Messages) != "null" {
				t.Fatalf("ack incorrectly became history: %+v %v", conv, err)
			}
			p := m.running(id)
			if _, err := p.call(ctx, map[string]any{"type": "abort"}); err != nil {
				t.Fatal(err)
			}
			wantAfterAbort := state
			if mode == "started" {
				wantAfterAbort = "unconfirmed"
			}
			deliveryState(t, m, id, one, wantAfterAbort)
			// Consume the second identical message first, with the same timestamp.
			if _, err := p.call(ctx, map[string]any{"type": "test_consume", "message": two}); err != nil {
				t.Fatal(err)
			}
			deliveryState(t, m, id, two, "consumed")
			deliveryState(t, m, id, one, wantAfterAbort)
			if _, err := p.call(ctx, map[string]any{"type": "test_consume", "message": three}); err != nil {
				t.Fatal(err)
			}
			conv, err = m.Messages(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			var history []map[string]any
			_ = json.Unmarshal(conv.Messages, &history)
			if len(history) != 2 || history[0]["yesmanDeliveryId"] != two || history[1]["yesmanDeliveryId"] != three {
				t.Fatalf("wrong identities: %s", conv.Messages)
			}
			if strings.Contains(string(conv.Messages), "<!--yesman-delivery:") {
				t.Fatal("transport marker leaked to history")
			}
			// Repeating a network request does not create a second pi prompt.
			duplicate, err := m.Submit(ctx, id, two, "same", []Image{testImage(), testImage()})
			if err != nil || duplicate.State != "consumed" {
				t.Fatalf("duplicate=%+v %v", duplicate, err)
			}
			if _, err := p.call(ctx, map[string]any{"type": "clear_queue"}); err != nil {
				t.Fatal(err)
			}
			if mode != "started" {
				deliveryState(t, m, id, one, "unconfirmed")
			}
			m.StopAll()
			deliveryState(t, m, id, one, "interrupted")
			restored := New(Config{Pi: m.cfg.Pi, Extension: m.cfg.Extension, Env: m.cfg.Env, Yesmen: store})
			defer restored.StopAll()
			if _, err := restored.Messages(ctx, id); err != nil {
				t.Fatal(err)
			}
			deliveryState(t, restored, id, one, "interrupted")
		})
	}
}

func TestDeliveryHandledAndStaleAgent(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=handled")
	ctx := context.Background()
	id, messageID := "alice/lead", strings.Repeat("d", 48)
	info, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Submit(ctx, id, messageID, "stale", nil, info.Since.Add(time.Second)); err == nil {
		t.Fatal("stale agent accepted")
	}
	if m.running(id) != nil {
		t.Fatal("stale agent started pi")
	}
	d, err := m.Submit(ctx, id, messageID, "handled", nil, info.Since)
	if err != nil || d.State != "handled" {
		t.Fatalf("handled became consumed: %+v %v", d, err)
	}
	conv, err := m.Messages(ctx, id)
	if err != nil || string(conv.Messages) != "null" {
		t.Fatalf("handled started model work: %+v %v", conv, err)
	}
}

func TestDeliveryExtensionGuardAndProcessExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, messageID := "alice/lead", strings.Repeat("c", 48)
	missing, _ := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_NO_EXTENSION=1")
	d, err := missing.Submit(ctx, id, messageID, "never leak a tag", nil)
	if err != nil || d.State != "failed" || !strings.Contains(d.Error, "extension did not load") {
		t.Fatalf("unguarded send: %+v %v", d, err)
	}
	for _, ending := range []string{"test_exit", "test_switch_session"} {
		t.Run(ending, func(t *testing.T) {
			m, _ := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer")
			d, err := m.Submit(ctx, id, messageID, "pending", nil)
			if err != nil || d.State != "steering" {
				t.Fatalf("submit %+v %v", d, err)
			}
			_, _ = m.running(id).call(ctx, map[string]any{"type": ending})
			deliveryState(t, m, id, messageID, "interrupted")
		})
	}
}

func TestDeliveryBeforeAckFailureAndIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, store := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_DELIVERY_MODE=steer", "FAKE_PI_DELIVERY_LATE_ACK=1")
	id, messageID := "alice/lead", strings.Repeat("a", 48)
	done := make(chan Delivery, 1)
	go func() { d, _ := m.Submit(ctx, id, messageID, "same", nil); done <- d }()
	waitStatus(t, m, id, Idle)
	deliveryState(t, m, id, messageID, "steering")
	p := m.running(id)
	if _, err := p.call(ctx, map[string]any{"type": "test_consume", "message": messageID}); err != nil {
		t.Fatal(err)
	}
	if d := <-done; d.State != "consumed" {
		t.Fatalf("late ack regressed delivery: %+v", d)
	}
	// A duplicate/later queue event cannot resurrect a consumed receipt.
	raw, _ := json.Marshal(map[string]any{"type": "queue_update", "steering": []string{deliveryText("same", messageID)}})
	m.record(m.agents[id], p, "queue_update", raw)
	deliveryState(t, m, id, messageID, "consumed")
	if _, err := store.Hire("bob"); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = m.Submit(ctx, "bob/lead", messageID, "same", nil) }()
	waitStatus(t, m, "bob/lead", Idle)
	deliveryState(t, m, "bob/lead", messageID, "steering")
	deliveryState(t, m, id, messageID, "consumed")
	m.Stop("bob/lead")
	deliveryState(t, m, "bob/lead", messageID, "interrupted")

	rejected, _ := newTestManager(t, 0, "FAKE_PI_DELIVERIES=1", "FAKE_PI_FAIL_PROMPTS=1")
	d, err := rejected.Submit(ctx, id, messageID, "retry me", nil)
	if err != nil || d.State != "failed" || !strings.Contains(d.Error, "rejected") {
		t.Fatalf("rejection: %+v %v", d, err)
	}
	retry, err := rejected.Submit(ctx, id, strings.Repeat("b", 48), "retry me", nil)
	if err != nil || retry.ID == d.ID || retry.State != "failed" {
		t.Fatalf("retry: %+v %v", retry, err)
	}
}
