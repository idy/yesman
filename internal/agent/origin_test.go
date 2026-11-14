package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func waitPrompts(t *testing.T, m *Manager, id string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		m.origins.mu.Lock()
		got := len(m.origins.claims)
		m.origins.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d prompts in flight, want %d", id, got, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Who wrote a prompt is known exactly while it is in flight, from the
// process's own credential, and never by guessing between matching inputs.
func TestPromptOriginWhileInFlight(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_HOLD_PROMPTS=1")
	if err := store.AddWorker("alice", yesmen.Worker{Name: "w1", Task: "t", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	lead, worker := ID("alice", yesmen.LeadName), ID("alice", "w1")
	ctx := context.Background()
	errs := make(chan error, 16)
	send := func(f func() error) { go func() { errs <- f() }() }

	send(func() error { return m.Prompt(ctx, lead, "hello") })
	waitPrompts(t, m, lead, 1)
	m.mu.Lock()
	a := m.agents[lead]
	token := a.token
	m.mu.Unlock()
	if o, ok := m.PromptOrigin(token, deliveryFingerprint("hello", nil)); !ok || o != (Origin{Source: OriginUser}) {
		t.Fatalf("origin of the user's prompt = %+v, %v", o, ok)
	}
	if _, ok := m.PromptOrigin(token, deliveryFingerprint("hello!", nil)); ok {
		t.Fatal("a different text matched")
	}
	if _, ok := m.PromptOrigin("forged", deliveryFingerprint("hello", nil)); ok {
		t.Fatal("an unknown credential matched")
	}

	// A yesman notice with the same text as a person's message in flight
	// is ambiguous: neither is said to be yesman's.
	m.mu.Lock()
	w := m.agents[lead] // only its name and yesman are read: the notice goes to the lead
	m.mu.Unlock()
	send(func() error { return m.tellLeadWithin(ctx, w, "report", "hello") })
	waitPrompts(t, m, lead, 2)
	if o, ok := m.PromptOrigin(token, deliveryFingerprint("hello", nil)); ok {
		t.Fatalf("an ambiguous prompt has origin %+v", o)
	}

	// A report alone is yesman's, about its worker.
	report := `[yesman] Worker "lead" finished a run. Its final message:` + "\n\ndone"
	send(func() error { return m.tellLeadWithin(ctx, w, "report", report) })
	waitPrompts(t, m, lead, 3)
	if o, ok := m.PromptOrigin(token, deliveryFingerprint(report, nil)); !ok || o != (Origin{Source: OriginYesman, Kind: "report", Worker: "lead"}) {
		t.Fatalf("origin of a report = %+v, %v", o, ok)
	}

	// Two submissions of the same text are the user's, bound to neither.
	ids := []string{deliveryTestID(1), deliveryTestID(2)}
	for _, id := range ids {
		send(func() error { _, err := m.Submit(ctx, lead, id, "again", nil); return err })
	}
	waitPrompts(t, m, lead, 5)
	if o, ok := m.PromptOrigin(token, deliveryFingerprint("again", nil)); !ok || o != (Origin{Source: OriginUser}) {
		t.Fatalf("origin of two identical submissions = %+v, %v", o, ok)
	}

	// The lead's message to a worker, in the worker's process only.
	send(func() error { return m.PromptFromLead(ctx, worker, "[from your lead] go") })
	waitPrompts(t, m, worker, 6)
	m.mu.Lock()
	workerToken := m.agents[worker].token
	m.mu.Unlock()
	if o, ok := m.PromptOrigin(workerToken, deliveryFingerprint("[from your lead] go", nil)); !ok || o != (Origin{Source: OriginLead, Kind: "message"}) {
		t.Fatalf("origin of the lead's message = %+v, %v", o, ok)
	}
	if _, ok := m.PromptOrigin(token, deliveryFingerprint("[from your lead] go", nil)); ok {
		t.Fatal("another process's prompt matched")
	}

	for _, id := range []string{lead, worker} {
		if _, err := m.running(id).call(ctx, map[string]any{"type": "test_release_prompts"}); err != nil {
			t.Fatal(err)
		}
	}
	for range 6 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	// Answered prompts are no longer anyone's: a later input with the same
	// text cannot inherit their origin.
	m.origins.mu.Lock()
	left := len(m.origins.claims)
	m.origins.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d claims outlived their prompts", left)
	}
	if _, ok := m.PromptOrigin(token, deliveryFingerprint(report, nil)); ok {
		t.Fatal("an answered prompt still has an origin")
	}

	// A single submission is bound to its receipt.
	id := deliveryTestID(3)
	send(func() error {
		_, err := m.Submit(ctx, lead, id, "once", []Image{{Type: "image", MimeType: "image/png", Data: "iVBORw0KGgo="}})
		return err
	})
	waitPrompts(t, m, lead, 1)
	if o, ok := m.PromptOrigin(token, deliveryFingerprint("once", []Image{{Type: "image", MimeType: "image/png", Data: "iVBORw0KGgo="}})); !ok || o != (Origin{Source: OriginUser, Submission: id}) {
		t.Fatalf("origin of a submission = %+v, %v", o, ok)
	}
	if _, err := m.running(lead).call(ctx, map[string]any{"type": "test_release_prompts"}); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

// A process that exits releases its claims, so its replacement starts clean.
func TestPromptOriginReleasedOnExit(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_HOLD_PROMPTS=1")
	lead := ID("alice", yesmen.LeadName)
	errc := make(chan error, 1)
	go func() { errc <- m.Prompt(context.Background(), lead, "hello") }()
	waitPrompts(t, m, lead, 1)
	m.Stop(lead)
	<-errc
	m.origins.mu.Lock()
	left := len(m.origins.claims)
	m.origins.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d claims outlived the process", left)
	}
}

// TestOriginFingerprintFixture writes the Go fingerprints of a few inputs,
// for scripts/check-origin-pi.mjs to check the extension computes the same.
func TestOriginFingerprintFixture(t *testing.T) {
	path := os.Getenv("YESMAN_TEST_ORIGIN_FIXTURE")
	if path == "" {
		t.Skip("written for scripts/check-origin-pi.mjs")
	}
	type vector struct {
		Text        string  `json:"text"`
		Images      []Image `json:"images,omitempty"`
		Fingerprint string  `json:"fingerprint"`
	}
	image := Image{Type: "image", MimeType: "image/png", Data: "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg=="}
	var vectors []vector
	for _, v := range []vector{
		{Text: "你好 yesman — “quotes”"},
		{Text: "", Images: []Image{image}},
		{Text: "two", Images: []Image{image, image}},
		{Text: `[yesman] Worker "w1" finished a run. Its final message:` + "\n\n**done**"},
	} {
		v.Fingerprint = deliveryFingerprint(v.Text, v.Images)
		vectors = append(vectors, v)
	}
	data, err := json.Marshal(vectors)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
