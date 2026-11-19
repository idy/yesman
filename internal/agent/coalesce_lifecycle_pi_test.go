package agent

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// Live families across the boundaries that end a pi process or an agent
// generation: real Manager, managed pi (capability 4), the production
// extension and the local deterministic provider. No family outlives its
// process or generation, no old ID retry reaches the model, and nothing is
// rebuilt from a preview.

// pendingFamily makes A+B one pending native occurrence while the fixture
// holds its first turn, and returns the IDs.
func pendingFamily(t *testing.T, ctx context.Context, f *promotionFixture, n int, image Image) (string, string) {
	t.Helper()
	idA, idB := deliveryTestID(n), deliveryTestID(n+1)
	if _, err := f.m.SubmitCoalescing(ctx, f.info.ID, idA, "family alpha", []Image{image}, f.info.Since); err != nil {
		t.Fatal(err)
	}
	f.delivery(t, idA, "steering")
	if b, err := f.m.SubmitCoalescing(ctx, f.info.ID, idB, "family beta", nil, f.info.Since); err != nil || b.MergedInto != idA {
		t.Fatalf("B did not join A: %+v %v", b, err)
	}
	waitFor(t, "A lists B", func() bool { return slices.Equal(f.receipt(idA).Merged, []string{idB}) })
	return idA, idB
}

// sessionIntroductions counts the distinct user messages, across sessions,
// whose text is exactly text.
func sessionIntroductions(t *testing.T, f *promotionFixture, text string) (int, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(f.gate), "audit"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var users []struct {
			auditMessage
			Timestamp int64 `json:"timestamp"`
		}
		if err := json.Unmarshal([]byte(line), &users); err != nil {
			t.Fatal(err)
		}
		for _, m := range users {
			if m.text() == text {
				seen[m.Timestamp] = true
			}
		}
	}
	return len(seen), string(raw)
}

// neverIntroduced fails when any provider call so far received a family input.
func neverIntroduced(t *testing.T, f *promotionFixture, texts ...string) {
	t.Helper()
	calls, _ := f.providerUsers(t)
	for _, users := range calls {
		for _, m := range users {
			for _, text := range texts {
				if strings.Contains(m.text(), text) {
					t.Fatalf("provider received %q after its generation ended", text)
				}
			}
		}
	}
}

func TestCoalesceFamilyEndsWithArchiveAndSameNameHireWithManagedPi(t *testing.T) {
	f := newPromotionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	image := testPNG(7, 8, 9)
	idA, idB := pendingFamily(t, ctx, f, 91001, image)
	old, err := f.store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := f.store.Archive("alice", func(yesmen.Yesman) error { return f.m.StopYesman("alice") })
	if err != nil {
		t.Fatal(err)
	}
	archivedReceipt := filepath.Join(archive, ".yesman", "sessions", "lead", "delivery.json")
	archivedDrafts, err := readHumanDrafts(archivedReceipt)
	if err != nil || archivedDrafts[idA].Text != "family alpha" || !slices.Equal(archivedDrafts[idA].Images, []Image{image}) || archivedDrafts[idB].Text != "family beta" {
		t.Fatalf("archive did not retain originals: %v", err)
	}
	if _, err = f.store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	// The archived receipts keep the interrupted family links and no payload bytes.
	var archived []byte
	_ = filepath.WalkDir(filepath.Join(filepath.Dir(f.gate), "archive"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == "delivery.json" && strings.Contains(path, filepath.Join("sessions", "lead")) {
			archived, _ = os.ReadFile(path)
		}
		return nil
	})
	var file deliveryDisk
	if err = json.Unmarshal(archived, &file); err != nil {
		t.Fatalf("archived receipts %q: %v", archived, err)
	}
	states := map[string]Delivery{}
	for _, d := range file.Records {
		states[d.ID] = d.Delivery
	}
	if a, b := states[idA], states[idB]; a.State != "interrupted" || b.State != "interrupted" || b.MergedInto != idA || !slices.Equal(a.Merged, []string{idB}) {
		t.Fatalf("archived family A=%+v B=%+v", a, b)
	}
	if strings.Contains(string(archived), image.Data) {
		t.Fatal("an original image was persisted")
	}
	// The old generation's IDs never act on the new one: no send, no recall,
	// no payload, and no replacement pi started for them.
	for _, id := range []string{idA, idB} {
		if _, err := f.m.SubmitCoalescing(ctx, f.info.ID, id, "family beta", nil, f.info.Since); err == nil {
			t.Fatalf("old generation retry of %s accepted", id)
		}
		if _, err := f.m.CancelSubmission(ctx, f.info.ID, id, f.info.Since); err == nil {
			t.Fatal("old generation recalled into the new workspace")
		}
		if _, err := f.m.SubmissionPayload(f.info.ID, id, f.info.Since); err == nil {
			t.Fatal("old generation payload served")
		}
	}
	if f.m.running(f.info.ID) != nil {
		t.Fatal("an old ID started the new generation's pi")
	}
	// The new generation starts clean and delivers its own input once.
	fresh, err := f.m.Get("alice/lead")
	if err != nil || fresh.Since.Equal(f.info.Since) {
		t.Fatalf("new generation %+v %v", fresh, err)
	}
	f.release(t)
	idC := deliveryTestID(91003)
	if c, err := f.m.SubmitCoalescing(ctx, fresh.ID, idC, "new generation", nil, fresh.Since); err != nil || c.MergedInto != "" {
		t.Fatalf("new generation submit %+v %v", c, err)
	}
	// Service post-Archive cleanup can arrive after the new hire initializes.
	f.m.ForgetYesman("alice", old.HiredAt)
	f.m.mu.Lock()
	used, accounted := f.m.recallBytes, f.m.agents[fresh.ID].recallBytes
	f.m.mu.Unlock()
	if used != accounted {
		t.Fatalf("retired originals still charged: global %d, current agent %d", used, accounted)
	}
	if err := f.m.AckRecall(fresh.ID, idA, f.info.Since); err == nil {
		t.Fatal("stale acknowledgement accepted")
	}
	if got, err := readHumanDrafts(archivedReceipt); err != nil || !slices.Equal(got[idA].Images, []Image{image}) {
		t.Fatalf("replacement changed the sealed archive: %v", err)
	}
	waitFor(t, "new generation input taken", func() bool {
		f.m.mu.Lock()
		defer f.m.mu.Unlock()
		d := f.m.agents[fresh.ID].deliveries[idC]
		return d.State == "dispatched" || d.State == "unconfirmed" || d.State == "failed"
	})
	// A new generation is a new pi session: count its message across calls
	// by its own record, not by the old session's growing context.
	waitFor(t, "new generation input reached the provider", func() bool { n, _ := sessionIntroductions(t, f, "new generation"); return n > 0 })
	time.Sleep(200 * time.Millisecond)
	n, raw := sessionIntroductions(t, f, "new generation")
	if n != 1 {
		t.Fatalf("new generation input introduced %d times", n)
	}
	neverIntroduced(t, f, "family alpha", "family beta")
	for _, id := range []string{idA, idB, idC} {
		if strings.Contains(raw, id) {
			t.Fatal("private submission ID reached the provider")
		}
	}
}

func TestCoalesceFamilyEndsWithNaturalExitWithManagedPi(t *testing.T) {
	f := newPromotionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	image := testPNG(3, 2, 1)
	idA, idB := pendingFamily(t, ctx, f, 92001, image)
	p := f.m.running(f.info.ID)
	if p == nil {
		t.Fatal("no running pi")
	}
	f.m.mu.Lock()
	a := f.m.agents[f.info.ID]
	stale := a.nativeQueue
	f.m.mu.Unlock()
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "family interrupted by the exit", func() bool {
		return f.receipt(idA).State == "interrupted" && f.receipt(idB).State == "interrupted"
	})
	waitFor(t, "old process retired", func() bool { return f.m.running(f.info.ID) != p })
	// The exited process's watcher can no longer revive the family: a late
	// snapshot or append answer from it is ignored.
	stale.Revision += 10
	f.m.syncPending(a, p, stale)
	f.m.deliveryResponse(a, p, idB, "steer", response{Success: true, Data: json.RawMessage(`{"disposition":"queued","append":{"outcome":"appended","queueId":"` + stale.Items[0].QueueID + `"}}`)})
	for _, id := range []string{idA, idB} {
		d := f.receipt(id)
		if d.State != "interrupted" || d.NativePending || d.CanCancel || d.CanRestore || d.CanRecover != (id == idA) || d.OriginalPayload != (id == idA) || d.Preparing {
			t.Fatalf("%s after the old watcher: %+v", id, d)
		}
	}
	if b := f.receipt(idB); b.MergedInto != idA {
		t.Fatalf("link lost %+v", b)
	}
	// Retrying either old ID returns its receipt without dispatch. Recovery
	// reads the quarantined originals and never advertises native recall.
	for _, id := range []string{idA, idB} {
		text, images := "family beta", []Image(nil)
		if id == idA {
			text, images = "family alpha", []Image{image}
		}
		d, err := f.m.SubmitCoalescing(ctx, f.info.ID, id, text, images, f.info.Since)
		if err != nil || d.State != "interrupted" {
			t.Fatalf("retry %s: %+v %v", id, d, err)
		}
		payload, err := f.m.SubmissionPayload(f.info.ID, id, f.info.Since)
		if err != nil || payload.Text != "family alpha\nfamily beta" || !slices.Equal(payload.Images, []Image{image}) {
			t.Fatalf("quarantined originals: %+v %v", payload, err)
		}
		if _, err := f.m.CancelSubmission(ctx, f.info.ID, id, f.info.Since); err == nil {
			t.Fatal("interrupted family recalled")
		}
	}
	if f.m.running(f.info.ID) != nil {
		t.Fatal("an old ID retry restarted pi")
	}
	// The next input starts a new process and is its own message, delivered once.
	f.release(t)
	idC := deliveryTestID(92003)
	if c, err := f.m.SubmitCoalescing(ctx, f.info.ID, idC, "after the exit", nil, f.info.Since); err != nil || c.MergedInto != "" {
		t.Fatalf("after exit %+v %v", c, err)
	}
	waitFor(t, "input after the exit reached the provider", func() bool { n, _ := sessionIntroductions(t, f, "after the exit"); return n > 0 })
	f.delivery(t, idC, "dispatched")
	time.Sleep(200 * time.Millisecond)
	if n, _ := sessionIntroductions(t, f, "after the exit"); n != 1 {
		t.Fatalf("input after the exit introduced %d times", n)
	}
	neverIntroduced(t, f, "family alpha", "family beta")
	for _, item := range f.pendingItems() {
		if item.SubmissionID == idA || item.SubmissionID == idB || slices.Contains(item.Segments, idB) {
			t.Fatalf("old family queued again: %+v", item)
		}
	}
}
