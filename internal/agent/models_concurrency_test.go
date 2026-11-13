package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func modelTestEvents(m *Manager) <-chan map[string]any {
	events := make(chan map[string]any, 64)
	// Install before starting the fake pi's reader.
	m.cfg.OnEvent = func(e Event) {
		var data map[string]any
		if json.Unmarshal(e.Data, &data) == nil {
			events <- data
		}
	}
	return events
}

func waitModelEvent(t *testing.T, events <-chan map[string]any, kind string) map[string]any {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case e := <-events:
			if e["type"] == kind {
				return e
			}
		case <-deadline.C:
			t.Fatalf("did not receive %s", kind)
		}
	}
}

func TestWorkerReportWaitsForModelConfiguration(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_MODEL_CHECK_DELAY=1")
	events := modelTestEvents(m)
	id := ID("alice", "lead")
	if _, _, err := m.ensure(id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	configured := make(chan error, 1)
	go func() {
		_, err := m.SetModel(ctx, id, "beta", "shared")
		configured <- err
	}()
	// Start the report while configuration is waiting for its idle check.
	waitModelEvent(t, events, "test_model_check")
	reported := make(chan error, 1)
	go func() { reported <- m.prompt(ctx, id, "[yesman] Worker finished", "followUp") }()
	if err := <-configured; err != nil {
		t.Fatal(err)
	}
	if e := waitModelEvent(t, events, "test_prompt_received"); e["provider"] != "beta" {
		t.Fatalf("report started before the model was applied: %v", e)
	}
	if err := <-reported; err != nil {
		t.Fatal(err)
	}
}

func TestPendingPromptBlocksConfigurationButAllowsSteering(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_HOLD_PROMPTS=1")
	events := modelTestEvents(m)
	id := ID("alice", "lead")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	caller, stopWaiting := context.WithCancel(ctx)
	defer stopWaiting()
	first := make(chan error, 1)
	go func() { first <- m.Prompt(caller, id, "start") }()
	waitModelEvent(t, events, "test_prompt_received")
	stopWaiting()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller: %v", err)
	}
	// pi is still handling the prompt, but has not emitted agent_start or
	// set isStreaming. Caller cancellation must not reopen configuration.
	if _, err := m.SetModel(ctx, id, "beta", "shared"); err == nil {
		t.Fatal("model changed during pending prompt startup")
	}
	if _, err := m.SetEffort(ctx, id, "low"); err == nil {
		t.Fatal("effort changed during pending prompt startup")
	}
	steered := make(chan error, 1)
	go func() { steered <- m.Prompt(ctx, id, "steer") }()
	waitModelEvent(t, events, "test_prompt_received")
	// Both prompts reached pi before either one finished: the gate does not
	// serialize whole turns or hold up steering messages.
	if _, err := m.running(id).call(ctx, map[string]any{"type": "test_release_prompts"}); err != nil {
		t.Fatal(err)
	}
	if err := <-steered; err != nil {
		t.Fatal(err)
	}
}
