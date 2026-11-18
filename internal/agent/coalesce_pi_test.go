package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Real Manager + managed pi (capability 4) + production extension + local
// deterministic provider. The fixture holds one turn until its gate opens.

type auditMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	} `json:"content"`
}

func (m auditMessage) text() string {
	var parts []string
	for _, b := range m.Content {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
func (m auditMessage) images() []Image {
	var out []Image
	for _, b := range m.Content {
		if b.Type == "image" {
			out = append(out, Image{Type: "image", MimeType: b.MimeType, Data: b.Data})
		}
	}
	return out
}

// providerUsers returns each provider call's user messages.
func (f *promotionFixture) providerUsers(t *testing.T) ([][]auditMessage, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(f.gate), "audit"))
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]auditMessage
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var users []auditMessage
		if err := json.Unmarshal([]byte(line), &users); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, users)
	}
	return calls, string(raw)
}

// introduced counts the user messages, across all provider calls, whose text is
// exactly want: each user message is counted once, in the call that first saw it.
func introduced(calls [][]auditMessage, want string) []auditMessage {
	var out []auditMessage
	seen := 0
	for _, users := range calls {
		for _, m := range users[min(seen, len(users)):] {
			if m.text() == want {
				out = append(out, m)
			}
		}
		seen = len(users)
	}
	return out
}

// awaitProvider waits until the provider has been called with every wanted
// user message: pi takes an input at a checkpoint before the next call starts.
func (f *promotionFixture) awaitProvider(t *testing.T, want ...string) ([][]auditMessage, string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		calls, raw := f.providerUsers(t)
		ok := true
		for _, w := range want {
			if len(introduced(calls, w)) == 0 {
				ok = false
			}
		}
		if ok {
			return calls, raw
		}
		if time.Now().After(deadline) {
			t.Fatalf("provider never received %q", want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func (f *promotionFixture) release(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(f.gate, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *promotionFixture) takenIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.taken)
}
func (f *promotionFixture) pendingItems() []pendingItem {
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	return slices.Clone(f.m.agents[f.info.ID].nativeQueue.Items)
}
func (f *promotionFixture) receipt(id string) Delivery {
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	a := f.m.agents[f.info.ID]
	return a.deliveryView(a.deliveries[id])
}
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestCoalesceWithManagedPi(t *testing.T) {
	f := newPromotionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p1, p2, p3, p4 := testPNG(255, 0, 0), testPNG(0, 255, 0), testPNG(0, 0, 255), testPNG(9, 9, 9)
	idA, idB, idC := deliveryTestID(83001), deliveryTestID(83002), deliveryTestID(83003)
	aText := " alpha 原文\n  keep spacing  "
	if _, err := f.m.SubmitCoalescing(ctx, f.info.ID, idA, aText, []Image{p1}, f.info.Since); err != nil {
		t.Fatal(err)
	}
	f.delivery(t, idA, "steering")
	b, err := f.m.SubmitCoalescing(ctx, f.info.ID, idB, "beta 补充", []Image{p2, p3}, f.info.Since)
	if err != nil || b.MergedInto != idA || b.State != "steering" {
		t.Fatalf("B did not join A: %+v %v", b, err)
	}
	// C is image-only and still finds A, the occurrence, not B, the newest receipt.
	c, err := f.m.SubmitCoalescing(ctx, f.info.ID, idC, "", []Image{p4}, f.info.Since)
	if err != nil || c.MergedInto != idA {
		t.Fatalf("C did not join A: %+v %v", c, err)
	}
	waitFor(t, "A lists B and C", func() bool { return slices.Equal(f.receipt(idA).Merged, []string{idB, idC}) })
	if items := f.pendingItems(); len(items) != 1 || items[0].SubmissionID != idA || !slices.Equal(items[0].Segments, []string{idB, idC}) {
		t.Fatalf("native queue %+v", items)
	}
	// Retrying A or B returns its receipt; nothing is sent again and A's own
	// intent is unchanged (a different payload under A's ID is still refused).
	if again, err := f.m.SubmitCoalescing(ctx, f.info.ID, idA, aText, []Image{p1}, f.info.Since); err != nil || !slices.Equal(again.Merged, []string{idB, idC}) {
		t.Fatalf("A retry %+v %v", again, err)
	}
	if again, err := f.m.SubmitCoalescing(ctx, f.info.ID, idB, "beta 补充", []Image{p2, p3}, f.info.Since); err != nil || again.MergedInto != idA {
		t.Fatalf("B retry %+v %v", again, err)
	}
	if _, err := f.m.SubmitCoalescing(ctx, f.info.ID, idA, aText+" and more", []Image{p1}, f.info.Since); err == nil {
		t.Fatal("another payload reused A's ID")
	}
	if items := f.pendingItems(); len(items) != 1 {
		t.Fatalf("a retry created an occurrence: %+v", items)
	}
	wantText, wantImages := aText+"\nbeta 补充", []Image{p1, p2, p3, p4}
	payload, err := f.m.SubmissionPayload(f.info.ID, idC, f.info.Since)
	if err != nil || payload.Text != wantText || !reflect.DeepEqual(payload.Images, wantImages) {
		t.Fatalf("aggregate payload %q %v", payload.Text, err)
	}
	if text, err := f.m.SubmissionText(f.info.ID, idB, f.info.Since); err != nil || text != wantText {
		t.Fatalf("aggregate Copy text %q %v", text, err)
	}
	if root := f.receipt(idA); !root.CanCancel || !root.OriginalPayload || root.ImageCount != 4 || root.Text != wantText {
		t.Fatalf("occurrence receipt %+v", root)
	}
	if child := f.receipt(idB); child.CanCancel || child.NativePending || child.OriginalPayload {
		t.Fatalf("appended input acts on its own: %+v", child)
	}
	f.release(t)
	for _, id := range []string{idA, idB, idC} {
		f.delivery(t, id, "dispatched")
	}
	waitFor(t, "A taken", func() bool { return slices.Contains(f.takenIDs(), idA) })
	if taken := f.takenIDs(); slices.Contains(taken, idB) || slices.Contains(taken, idC) || len(slices.DeleteFunc(slices.Clone(taken), func(s string) bool { return s != idA })) != 1 {
		t.Fatalf("native taken %v", taken)
	}
	calls, raw := f.awaitProvider(t, wantText)
	merged := introduced(calls, wantText)
	if len(merged) != 1 || !reflect.DeepEqual(merged[0].images(), wantImages) {
		var texts [][]string
		for _, users := range calls {
			var row []string
			for _, m := range users {
				row = append(row, m.text())
			}
			texts = append(texts, row)
		}
		t.Fatalf("provider introduced the merged message %d times: %q", len(merged), texts)
	}
	if len(introduced(calls, "beta 补充")) != 0 || len(introduced(calls, "")) != 0 || len(introduced(calls, aText)) != 0 {
		t.Fatal("provider also received an input on its own")
	}
	for _, id := range []string{idA, idB, idC} {
		if strings.Contains(raw, id) {
			t.Fatal("private submission ID reached the provider")
		}
	}
	// A whole Manager restart neither resends nor restores the family.
	f.m.StopAll()
	restarted := New(f.cfg)
	defer restarted.StopAll()
	for _, id := range []string{idA, idB, idC} {
		d, err := restarted.SubmitCoalescing(ctx, f.info.ID, id, "x", nil, f.info.Since)
		if err == nil && d.State != "dispatched" {
			t.Fatalf("restart %s: %+v", id, d)
		}
	}
	if restarted.running(f.info.ID) != nil {
		t.Fatal("old ID retry started pi")
	}
}

func TestCoalescePendingFamilyAcrossRestartWithManagedPi(t *testing.T) {
	f := newPromotionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	idA, idB := deliveryTestID(84001), deliveryTestID(84002)
	if _, err := f.m.SubmitCoalescing(ctx, f.info.ID, idA, "alpha", nil, f.info.Since); err != nil {
		t.Fatal(err)
	}
	f.delivery(t, idA, "steering")
	if b, err := f.m.SubmitCoalescing(ctx, f.info.ID, idB, "beta", nil, f.info.Since); err != nil || b.MergedInto != idA {
		t.Fatalf("%+v %v", b, err)
	}
	f.m.StopAll()
	restarted := New(f.cfg)
	defer restarted.StopAll()
	a, errA := restarted.SubmitCoalescing(ctx, f.info.ID, idA, "alpha", nil, f.info.Since)
	b, errB := restarted.SubmitCoalescing(ctx, f.info.ID, idB, "beta", nil, f.info.Since)
	if errA != nil || errB != nil || a.State != "interrupted" || b.State != "interrupted" || b.MergedInto != idA || !slices.Equal(a.Merged, []string{idB}) || a.CanRestore || a.CanCancel {
		t.Fatalf("restart family A=%+v %v B=%+v %v", a, errA, b, errB)
	}
	if restarted.running(f.info.ID) != nil {
		t.Fatal("interrupted family started pi")
	}
	if _, err := restarted.SubmissionPayload(f.info.ID, idA, f.info.Since); err == nil {
		t.Fatal("restart reconstructed a payload")
	}
}

func TestCoalesceFallbacksWithManagedPi(t *testing.T) {
	t.Run("combined images over the limit", func(t *testing.T) {
		f := newPromotionFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		idA, idB := deliveryTestID(85001), deliveryTestID(85002)
		a3 := []Image{testPNG(1, 0, 0), testPNG(2, 0, 0), testPNG(3, 0, 0)}
		b2 := []Image{testPNG(4, 0, 0), testPNG(5, 0, 0)}
		if _, err := f.m.SubmitCoalescing(ctx, f.info.ID, idA, "three", a3, f.info.Since); err != nil {
			t.Fatal(err)
		}
		f.delivery(t, idA, "steering")
		b, err := f.m.SubmitCoalescing(ctx, f.info.ID, idB, "two", b2, f.info.Since)
		if err != nil || b.MergedInto != "" || len(f.receipt(idA).Merged) != 0 {
			t.Fatalf("over-limit input joined: %+v %v", b, err)
		}
		f.delivery(t, idB, "steering")
		f.release(t)
		f.delivery(t, idA, "dispatched")
		f.delivery(t, idB, "dispatched")
		calls, _ := f.awaitProvider(t, "three", "two")
		if got := introduced(calls, "three"); len(got) != 1 || !reflect.DeepEqual(got[0].images(), a3) {
			t.Fatal("A changed")
		}
		if got := introduced(calls, "two"); len(got) != 1 || !reflect.DeepEqual(got[0].images(), b2) {
			t.Fatal("B lost or duplicated")
		}
	})
	t.Run("plain Submit and other lanes stay separate", func(t *testing.T) {
		f := newPromotionFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		idA, idB, idC := deliveryTestID(86001), deliveryTestID(86002), deliveryTestID(86003)
		if _, err := f.m.SubmitCoalescing(ctx, f.info.ID, idA, "first", nil, f.info.Since); err != nil {
			t.Fatal(err)
		}
		f.delivery(t, idA, "steering")
		if b, err := f.m.Submit(ctx, f.info.ID, idB, "plain", nil, f.info.Since); err != nil || b.MergedInto != "" {
			t.Fatalf("plain Submit joined: %+v %v", b, err)
		}
		f.delivery(t, idB, "steering")
		// A follow-up composer input never joins a steering occurrence.
		if c, err := f.m.submitDelivery(ctx, f.info.ID, idC, "later", nil, "followUp", submission{coalesce: true}, f.info.Since); err != nil || c.MergedInto != "" {
			t.Fatalf("follow-up joined steering: %+v %v", c, err)
		}
		if items := f.pendingItems(); len(items) != 3 {
			t.Fatalf("queue %+v", items)
		}
	})
}

// Only a composer occurrence is joined. A pending plain Submit, topic message
// or scheduled run stays its own message, and a later composer input becomes
// its own occurrence: pi takes and the provider receives each once.
func TestCoalesceTargetsOnlyComposerOccurrencesWithManagedPi(t *testing.T) {
	cases := []struct {
		name, lane, first string
		submit            func(ctx context.Context, f *promotionFixture, id string) (Delivery, error)
	}{
		{"plain Submit", "steering", "plain A", func(ctx context.Context, f *promotionFixture, id string) (Delivery, error) {
			return f.m.Submit(ctx, f.info.ID, id, "plain A", nil, f.info.Since)
		}},
		{"topic message", "steering", "[Topic tp1: Storage]\n\ntopic A", func(ctx context.Context, f *promotionFixture, id string) (Delivery, error) {
			return f.m.SubmitInContext(ctx, f.info.ID, id, "[Topic tp1: Storage]\n\ntopic A", nil, "topic:tp1", f.info.Since)
		}},
		{"scheduled run", "queued", "scheduled A", func(ctx context.Context, f *promotionFixture, id string) (Delivery, error) {
			return f.m.SubmitFollowUp(ctx, f.info.ID, id, "scheduled A", Origin{Source: OriginSchedule, Plan: "s1", Run: "s1-20261118t0900"}, f.info.Since)
		}},
	}
	for n, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newPromotionFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			idA, idB := deliveryTestID(87001+2*n), deliveryTestID(87002+2*n)
			if _, err := c.submit(ctx, f, idA); err != nil {
				t.Fatal(err)
			}
			f.delivery(t, idA, c.lane)
			behavior := "steer"
			if c.lane == "queued" {
				behavior = "followUp" // the same lane, so only the occurrence's kind decides
			}
			b, err := f.m.submitDelivery(ctx, f.info.ID, idB, "composer B", nil, behavior, submission{coalesce: true}, f.info.Since)
			if err != nil || b.MergedInto != "" || len(f.receipt(idA).Merged) != 0 {
				t.Fatalf("composer input joined a %s: %+v %v", c.name, b, err)
			}
			f.delivery(t, idB, c.lane)
			if items := f.pendingItems(); len(items) != 2 || len(items[0].Segments) != 0 {
				t.Fatalf("queue %+v", items)
			}
			f.release(t)
			f.delivery(t, idA, "dispatched")
			f.delivery(t, idB, "dispatched")
			calls, raw := f.awaitProvider(t, c.first, "composer B")
			if len(introduced(calls, c.first)) != 1 || len(introduced(calls, "composer B")) != 1 || len(introduced(calls, c.first+"\ncomposer B")) != 0 {
				t.Fatalf("provider saw %s", raw)
			}
			for _, private := range []string{idA, idB, "topic:tp1", "s1-20261118t0900"} {
				if strings.Contains(raw, private) {
					t.Fatalf("provider saw %q", private)
				}
			}
		})
	}
}

func TestCoalesceCancelAndPromoteFamilyWithManagedPi(t *testing.T) {
	t.Run("cancel returns the whole message and keeps others", func(t *testing.T) {
		f := newPromotionFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		idA, idB, idX := deliveryTestID(87001), deliveryTestID(87002), deliveryTestID(87003)
		pA, pB := testPNG(10, 0, 0), testPNG(20, 0, 0)
		if _, err := f.m.SubmitCoalescing(ctx, f.info.ID, idA, "alpha", []Image{pA}, f.info.Since); err != nil {
			t.Fatal(err)
		}
		f.delivery(t, idA, "steering")
		if b, err := f.m.SubmitCoalescing(ctx, f.info.ID, idB, "beta", []Image{pB}, f.info.Since); err != nil || b.MergedInto != idA {
			t.Fatalf("%+v %v", b, err)
		}
		if _, err := f.m.Submit(ctx, f.info.ID, idX, "survivor", nil, f.info.Since); err != nil {
			t.Fatal(err)
		}
		f.delivery(t, idX, "steering")
		waitFor(t, "merge recorded", func() bool { return slices.Equal(f.receipt(idA).Merged, []string{idB}) })
		result, err := f.m.CancelSubmission(ctx, f.info.ID, idB, f.info.Since)
		if err != nil || result.Payload.Text != "alpha\nbeta" || !reflect.DeepEqual(result.Payload.Images, []Image{pA, pB}) || result.Delivery.ID != idA {
			t.Fatalf("cancel %+v %v", result, err)
		}
		f.delivery(t, idA, "cancelled")
		f.delivery(t, idB, "cancelled")
		if again, err := f.m.CancelSubmission(ctx, f.info.ID, idA, f.info.Since); err != nil || again.Payload.Text != "alpha\nbeta" {
			t.Fatalf("lost-reply retry %+v %v", again, err)
		}
		if items := f.pendingItems(); len(items) != 1 || items[0].SubmissionID != idX {
			t.Fatalf("other queue changed: %+v", items)
		}
		if err = f.m.AckRecall(f.info.ID, idB, f.info.Since); err != nil {
			t.Fatal(err)
		}
		if _, err = f.m.SubmissionPayload(f.info.ID, idA, f.info.Since); err == nil {
			t.Fatal("restored payload kept")
		}
		f.release(t)
		f.delivery(t, idX, "dispatched")
		calls, _ := f.awaitProvider(t, "survivor")
		if len(introduced(calls, "survivor")) != 1 || len(introduced(calls, "alpha\nbeta")) != 0 || len(introduced(calls, "alpha")) != 0 {
			t.Fatal("cancelled family reached the provider")
		}
	})
	t.Run("promoting an appended follow-up moves its occurrence", func(t *testing.T) {
		f := newPromotionFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		idA, idB := deliveryTestID(88001), deliveryTestID(88002)
		if _, err := f.m.submitDelivery(ctx, f.info.ID, idA, "follow A", nil, "followUp", submission{coalesce: true}, f.info.Since); err != nil {
			t.Fatal(err)
		}
		f.delivery(t, idA, "queued")
		if b, err := f.m.submitDelivery(ctx, f.info.ID, idB, "follow B", nil, "followUp", submission{coalesce: true}, f.info.Since); err != nil || b.MergedInto != idA || b.State != "queued" {
			t.Fatalf("%+v %v", b, err)
		}
		waitFor(t, "merge recorded", func() bool { return slices.Equal(f.receipt(idA).Merged, []string{idB}) })
		p, err := f.m.PromoteSubmission(ctx, f.info.ID, idB, f.info.Since)
		if err != nil || p.Outcome != "promoted" || p.Delivery.ID != idA {
			t.Fatalf("promote %+v %v", p, err)
		}
		f.delivery(t, idB, "steering")
		f.release(t)
		f.delivery(t, idA, "dispatched")
		f.delivery(t, idB, "dispatched")
		calls, _ := f.awaitProvider(t, "follow A\nfollow B")
		if len(introduced(calls, "follow A\nfollow B")) != 1 || len(introduced(calls, "follow B")) != 0 {
			t.Fatal("promoted family not delivered once")
		}
	})
}

// Idle preparation claims A first: A is Preparing (never Taken) while it waits,
// B is sent as its own message, and each reaches the provider once.
func TestCoalescePreparingWinsWithManagedPi(t *testing.T) {
	var hold sync.Mutex
	var holding bool
	entered, unblock := make(chan struct{}, 4), make(chan struct{})
	f := newPromotionFixtureWithContext(t, nil, func() {
		hold.Lock()
		h := holding
		hold.Unlock()
		if h {
			entered <- struct{}{}
			<-unblock
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	idA, idB := deliveryTestID(89001), deliveryTestID(89002)
	if _, err := f.m.submitDelivery(ctx, f.info.ID, idA, "prepared A", nil, "followUp", submission{coalesce: true}, f.info.Since); err != nil {
		t.Fatal(err)
	}
	f.delivery(t, idA, "queued")
	if err := f.m.Abort(ctx, f.info.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "promotion allowed after Stop", func() bool { return f.receipt(idA).CanPromote })
	hold.Lock()
	holding = true
	hold.Unlock()
	f.release(t)
	promoted := make(chan error, 1)
	go func() { _, err := f.m.PromoteSubmission(ctx, f.info.ID, idA, f.info.Since); promoted <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("preparation never asked for context")
	}
	waitFor(t, "A preparing", func() bool { return f.receipt(idA).Preparing })
	if a := f.receipt(idA); a.State == "dispatched" || !a.CanCancel {
		t.Fatalf("Preparing reported as taken: %+v", a)
	}
	b, err := f.m.SubmitCoalescing(ctx, f.info.ID, idB, "later B", nil, f.info.Since)
	if err != nil || b.MergedInto != "" {
		t.Fatalf("B joined a claimed occurrence: %+v %v", b, err)
	}
	hold.Lock()
	holding = false
	hold.Unlock()
	close(unblock)
	if err = <-promoted; err != nil {
		t.Fatal(err)
	}
	f.delivery(t, idA, "dispatched")
	f.delivery(t, idB, "dispatched")
	calls, _ := f.awaitProvider(t, "prepared A", "later B")
	if len(introduced(calls, "prepared A")) != 1 || len(introduced(calls, "later B")) != 1 || len(introduced(calls, "prepared A\nlater B")) != 0 {
		t.Fatal("claim-first inputs not delivered once each")
	}
}
