package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// testPNG is a distinct valid 2x2 PNG, so image order is observable.
func testPNG(r, g, b uint8) Image {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for x := 0; x < 2; x++ {
		for y := 0; y < 2; y++ {
			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		panic(err)
	}
	return Image{Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(out.Bytes())}
}

type coalesceUnit struct {
	m      *Manager
	a      *agent
	p      *proc
	events []Event
}

// newCoalesceUnit builds a capability 4 agent with one pending composer
// occurrence A (steering, queueId E:1, revision 0) and a composer input B that
// is bound to the same session and waiting for pi's answer.
func newCoalesceUnit(t *testing.T) (*coalesceUnit, string, string) {
	t.Helper()
	u := &coalesceUnit{}
	since := time.Now()
	u.p = &proc{pending: map[string]waiter{}, done: make(chan struct{}), session: "proc1"}
	u.p.piSession.Store("pi1")
	u.a = &agent{info: Info{ID: "alice/lead", Since: since}, proc: u.p, deliveries: map[string]Delivery{}}
	u.a.deliveryWriter = newDeliveryWriter(filepath.Join(t.TempDir(), "delivery.json"), nil)
	u.m = &Manager{agents: map[string]*agent{"alice/lead": u.a}, worktreeCleanup: map[string]bool{}, cfg: Config{OnEvent: func(e Event) { u.events = append(u.events, e) }}}
	u.a.nativeQueue = pendingQueue{Capability: 4, Epoch: "E", Revision: 1, AppendAllowed: true,
		Items: []pendingItem{{QueueID: "E:1", SubmissionID: "", Kind: "steer"}}}
	idA, idB := deliveryTestID(80001), deliveryTestID(80002)
	u.a.nativeQueue.Items[0].SubmissionID = idA
	session := u.p.deliverySession()
	u.a.deliveries[idA] = Delivery{ID: idA, State: "steering", Text: "alpha", ImageCount: 1, Order: 1, AgentSince: since, Session: session,
		queueID: "E:1", queueEpoch: "E", coalesce: true, fingerprint: deliveryFingerprint("alpha", []Image{testPNG(1, 0, 0)})}
	u.a.deliveries[idB] = Delivery{ID: idB, State: "sending", Text: "beta", ImageCount: 2, Order: 2, AgentSince: since, Session: session,
		coalesce: true, appendRoot: idA, fingerprint: "b"}
	u.m.retainRecallLocked(u.a, idA, "alpha", []Image{testPNG(1, 0, 0)})
	u.m.retainRecallLocked(u.a, idB, "beta", []Image{testPNG(2, 0, 0), testPNG(3, 0, 0)})
	t.Cleanup(func() { u.a.deliveryWriter.close(context.Background()) })
	return u, idA, idB
}
func (u *coalesceUnit) snapshot(revision uint64, items []pendingItem, changes ...pendingItem) {
	u.m.syncPending(u.a, u.p, pendingQueue{Capability: 4, Epoch: "E", Revision: revision, AppendAllowed: true, Items: items, Changes: changes})
}
func (u *coalesceUnit) reply(id, data string) {
	u.m.deliveryResponse(u.a, u.p, id, "steer", response{Success: true, Data: json.RawMessage(data)})
}
func (u *coalesceUnit) get(id string) Delivery { return u.a.deliveries[id] }

func TestCoalesceEventThenReplyThenTaken(t *testing.T) {
	u, idA, idB := newCoalesceUnit(t)
	merged := pendingItem{QueueID: "E:1", SubmissionID: idA, Kind: "steer", ItemRevision: 1, Segments: []string{idB}}
	u.snapshot(2, []pendingItem{merged}, pendingItem{QueueID: "E:1", SubmissionID: idA, Kind: "steer", ItemRevision: 1, State: "appended", Segments: []string{idB}})
	a, b := u.get(idA), u.get(idB)
	if !slices.Equal(a.Merged, []string{idB}) || a.queueRevision != 1 || a.ImageCount != 3 || a.Text != "alpha\nbeta" || b.MergedInto != idA || b.State != "steering" || b.queueID != "" {
		t.Fatalf("appended event: A=%+v B=%+v", a, b)
	}
	if a.fingerprint != deliveryFingerprint("alpha", []Image{testPNG(1, 0, 0)}) {
		t.Fatal("append replaced the primary input's own intent")
	}
	// The reply arrives after its event and an out-of-order older snapshot.
	u.reply(idB, `{"disposition":"queued","append":{"outcome":"appended","queueId":"E:1","itemRevision":1}}`)
	u.snapshot(1, []pendingItem{{QueueID: "E:1", SubmissionID: idA, Kind: "steer"}})
	if got := u.get(idB); got.State != "steering" || got.MergedInto != idA || !slices.Equal(u.get(idA).Merged, []string{idB}) {
		t.Fatalf("late reply or stale snapshot changed the merge: %+v", got)
	}
	view := u.a.deliveryView(u.get(idB))
	if view.CanCancel || view.CanPromote || view.NativePending || view.OriginalPayload {
		t.Fatalf("appended input acts on its own: %+v", view)
	}
	if root := u.a.deliveryView(u.get(idA)); !root.CanCancel || !root.NativePending {
		t.Fatalf("occurrence lost its actions: %+v", root)
	}
	payload, err := u.m.SubmissionPayload(u.a.info.ID, idB, u.a.info.Since)
	want := []Image{testPNG(1, 0, 0), testPNG(2, 0, 0), testPNG(3, 0, 0)}
	if err != nil || payload.Text != "alpha\nbeta" || !reflect.DeepEqual(payload.Images, want) {
		t.Fatalf("aggregate payload %+v %v", payload, err)
	}
	payload.Images[0].Data = "mutated"
	if again, _ := u.m.SubmissionPayload(u.a.info.ID, idA, u.a.info.Since); !reflect.DeepEqual(again.Images, want) {
		t.Fatal("caller mutated the retained payload")
	}
	// commitDrain reports taken before it releases the claim: taken wins.
	u.snapshot(3, nil, pendingItem{QueueID: "E:1", SubmissionID: idA, Kind: "steer", ItemRevision: 1, State: "taken", Segments: []string{idB}, Preparing: true})
	if u.get(idA).State != "dispatched" || u.get(idB).State != "dispatched" || len(u.a.recallPayloads) != 0 || u.a.deliveryView(u.get(idA)).Preparing {
		t.Fatalf("taken: A=%+v B=%+v payloads=%d", u.get(idA), u.get(idB), len(u.a.recallPayloads))
	}
}

func TestCoalesceReplyBeforeOrWithoutEvent(t *testing.T) {
	u, idA, idB := newCoalesceUnit(t)
	u.reply(idB, `{"disposition":"queued","append":{"outcome":"appended","queueId":"E:1","itemRevision":1}}`)
	if b := u.get(idB); b.MergedInto != idA || b.State != "steering" || !slices.Equal(u.get(idA).Merged, []string{idB}) {
		t.Fatalf("reply alone did not record the merge: %+v", b)
	}
	u.snapshot(2, []pendingItem{{QueueID: "E:1", SubmissionID: idA, Kind: "steer", ItemRevision: 1, Segments: []string{idB}}})
	if !slices.Equal(u.get(idA).Merged, []string{idB}) {
		t.Fatal("event after reply duplicated the member")
	}
}

func TestCoalesceTakenBeforeAppendedEventOrReply(t *testing.T) {
	u, idA, idB := newCoalesceUnit(t)
	// The appended event was missed; pi's taken change still names B.
	u.snapshot(3, nil, pendingItem{QueueID: "E:1", SubmissionID: idA, Kind: "steer", ItemRevision: 1, State: "taken", Segments: []string{idB}})
	if u.get(idA).State != "dispatched" || u.get(idB).State != "dispatched" || u.get(idB).MergedInto != idA {
		t.Fatalf("taken membership lost: A=%+v B=%+v", u.get(idA), u.get(idB))
	}
	u.reply(idB, `{"disposition":"queued","append":{"outcome":"appended","queueId":"E:1","itemRevision":1}}`)
	u.snapshot(2, []pendingItem{{QueueID: "E:1", SubmissionID: idA, Kind: "steer", ItemRevision: 1, Segments: []string{idB}, Preparing: true}})
	if u.get(idB).State != "dispatched" || u.get(idA).State != "dispatched" {
		t.Fatal("late reply or stale preparing snapshot downgraded a taken input")
	}
}

func TestCoalesceFallbackOutcomes(t *testing.T) {
	u, idA, idB := newCoalesceUnit(t)
	// pi queued B as its own occurrence (it reported that queue change first).
	u.snapshot(2, []pendingItem{{QueueID: "E:1", SubmissionID: idA, Kind: "steer"}, {QueueID: "E:2", SubmissionID: idB, Kind: "steer"}})
	u.reply(idB, `{"disposition":"queued","append":{"outcome":"stale_item","queueId":"E:2","itemRevision":0,"enqueued":true}}`)
	if b := u.get(idB); b.MergedInto != "" || b.queueID != "E:2" || b.State != "steering" || len(u.get(idA).Merged) != 0 {
		t.Fatalf("independent fallback: %+v", b)
	}
	u2, _, idB2 := newCoalesceUnit(t)
	u2.reply(idB2, `{"disposition":"queued","append":{"outcome":"duplicate"}}`)
	if b := u2.get(idB2); b.State != "unconfirmed" || b.MergedInto != "" {
		t.Fatalf("duplicate: %+v", b)
	}
}

func TestCoalesceEpochChangeAndCancelFollowOccurrence(t *testing.T) {
	u, idA, idB := newCoalesceUnit(t)
	item := pendingItem{QueueID: "E:1", SubmissionID: idA, Kind: "steer", ItemRevision: 1, Segments: []string{idB}}
	u.snapshot(2, []pendingItem{item})
	u.snapshot(3, nil, pendingItem{QueueID: "E:1", SubmissionID: idA, Kind: "steer", ItemRevision: 1, State: "cancelled", Segments: []string{idB}})
	if u.get(idA).State != "cancelled" || u.get(idB).State != "cancelled" || !u.a.deliveryView(u.get(idA)).CanRestore || u.a.deliveryView(u.get(idB)).CanRestore {
		t.Fatalf("cancel: A=%+v B=%+v", u.get(idA), u.get(idB))
	}
	if err := u.m.AckRecall(u.a.info.ID, idB, u.a.info.Since); err != nil || len(u.a.recallPayloads) != 0 {
		t.Fatalf("restore ack kept member payloads: %v %d", err, len(u.a.recallPayloads))
	}

	v, vA, vB := newCoalesceUnit(t)
	v.snapshot(2, []pendingItem{{QueueID: "E:1", SubmissionID: vA, Kind: "steer", ItemRevision: 1, Segments: []string{vB}}})
	v.m.syncPending(v.a, v.p, pendingQueue{Capability: 4, Epoch: "F", Revision: 1})
	if v.get(vA).State != "interrupted" || v.get(vB).State != "interrupted" || len(v.a.recallPayloads) != 0 {
		t.Fatalf("new session kept a merged input pending: A=%+v B=%+v", v.get(vA), v.get(vB))
	}
}

func TestCoalesceMissingMemberPayloadBlocksPartialRecall(t *testing.T) {
	u, idA, idB := newCoalesceUnit(t)
	u.snapshot(2, []pendingItem{{QueueID: "E:1", SubmissionID: idA, Kind: "steer", ItemRevision: 1, Segments: []string{idB}}})
	u.m.forgetRecallLocked(u.a, idB)
	if u.a.deliveryView(u.get(idA)).CanCancel {
		t.Fatal("recall offered without every member's payload")
	}
	if _, err := u.m.SubmissionPayload(u.a.info.ID, idA, u.a.info.Since); err == nil {
		t.Fatal("partial aggregate returned")
	}
}

func TestCoalesceTargetEligibility(t *testing.T) {
	big := strings.Repeat("A", base64.StdEncoding.EncodedLen(maxImageBytes))
	cases := []struct {
		name string
		edit func(u *coalesceUnit, idA, idB string)
		ok   bool
	}{
		{"eligible", func(*coalesceUnit, string, string) {}, true},
		{"not composer", func(u *coalesceUnit, _, idB string) { d := u.get(idB); d.coalesce = false; u.a.deliveries[idB] = d }, false},
		// Only a composer occurrence is a target: a plain Submit, a scheduled
		// run, a topic's message or one restored from disk (coalesce is never
		// persisted) is not, even when pi would accept its origin.
		{"plain occurrence", func(u *coalesceUnit, idA, _ string) { d := u.get(idA); d.coalesce = false; u.a.deliveries[idA] = d }, false},
		{"scheduled occurrence", func(u *coalesceUnit, idA, _ string) {
			d := u.get(idA)
			d.Origin = &Origin{Source: OriginSchedule, Plan: "s1", Run: "s1-a"}
			u.a.deliveries[idA] = d
		}, false},
		{"topic occurrence", func(u *coalesceUnit, idA, _ string) {
			d := u.get(idA)
			d.hostContext = "topic:tp1"
			u.a.deliveries[idA] = d
		}, false},
		{"input in a topic", func(u *coalesceUnit, _, idB string) {
			d := u.get(idB)
			d.hostContext = "topic:tp1"
			u.a.deliveries[idB] = d
		}, false},
		{"older runtime", func(u *coalesceUnit, _, _ string) { u.a.nativeQueue.Capability = 3 }, false},
		{"stopping", func(u *coalesceUnit, _, _ string) { u.a.nativeQueue.AppendAllowed = false }, false},
		{"preparing", func(u *coalesceUnit, _, _ string) { u.a.nativeQueue.Items[0].Preparing = true }, false},
		{"other lane", func(u *coalesceUnit, _, _ string) { u.a.nativeQueue.Items[0].Kind = "followUp" }, false},
		{"not newest", func(u *coalesceUnit, _, _ string) {
			u.a.nativeQueue.Items = append(u.a.nativeQueue.Items, pendingItem{QueueID: "E:9", Kind: "steer"})
		}, false},
		{"stale revision", func(u *coalesceUnit, _, _ string) { u.a.nativeQueue.Items[0].ItemRevision = 4 }, false},
		{"other session", func(u *coalesceUnit, idA, _ string) { d := u.get(idA); d.Session = "old:pi0"; u.a.deliveries[idA] = d }, false},
		{"other generation", func(u *coalesceUnit, idA, _ string) {
			d := u.get(idA)
			d.AgentSince = time.Time{}
			u.a.deliveries[idA] = d
		}, false},
		{"missing payload", func(u *coalesceUnit, idA, _ string) { u.m.forgetRecallLocked(u.a, idA) }, false},
		{"slash input", func(u *coalesceUnit, _, idB string) {
			u.m.forgetRecallLocked(u.a, idB)
			u.m.retainRecallLocked(u.a, idB, "  /template arg", nil)
		}, false},
		{"slash occurrence", func(u *coalesceUnit, idA, _ string) {
			u.m.forgetRecallLocked(u.a, idA)
			u.m.retainRecallLocked(u.a, idA, "/skill:x", []Image{testPNG(1, 0, 0)})
		}, false},
		{"five images", func(u *coalesceUnit, idA, _ string) {
			u.m.forgetRecallLocked(u.a, idA)
			u.m.retainRecallLocked(u.a, idA, "alpha", []Image{testPNG(1, 0, 0), testPNG(1, 1, 0), testPNG(1, 2, 0)})
		}, false},
		{"over total bytes", func(u *coalesceUnit, idA, idB string) {
			u.m.forgetRecallLocked(u.a, idA)
			u.m.forgetRecallLocked(u.a, idB)
			u.m.retainRecallLocked(u.a, idA, "alpha", []Image{{Type: "image", MimeType: "image/png", Data: big}, {Type: "image", MimeType: "image/png", Data: big}})
			u.m.retainRecallLocked(u.a, idB, "beta", []Image{testPNG(2, 0, 0)})
		}, false},
		{"segment limit", func(u *coalesceUnit, _, _ string) {
			for n := 0; n < maxMergedInputs; n++ {
				u.a.nativeQueue.Items[0].Segments = append(u.a.nativeQueue.Items[0].Segments, deliveryTestID(81000+n))
			}
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, idA, idB := newCoalesceUnit(t)
			c.edit(u, idA, idB)
			target, root := u.m.appendTargetLocked(u.a, u.p, u.get(idB), "steer")
			if (target != nil) != c.ok {
				t.Fatalf("target %v root %q", target, root)
			}
			if c.ok && (root != idA || target["queueId"] != "E:1" || target["expectedRevision"] != uint64(0) || target["epoch"] != "E") {
				t.Fatalf("wrong target %v", target)
			}
		})
	}
}

func TestCoalesceLinksPersistAndValidate(t *testing.T) {
	u, idA, idB := newCoalesceUnit(t)
	u.snapshot(2, []pendingItem{{QueueID: "E:1", SubmissionID: idA, Kind: "steer", ItemRevision: 1, Segments: []string{idB}}})
	path := filepath.Join(t.TempDir(), "delivery.json")
	state := deliveryDisk{Version: 2}
	for _, d := range u.a.deliveries {
		state.Records = append(state.Records, storedDelivery{d, strings.Repeat("a", 64), d.created, time.Now().Add(time.Hour).UnixMilli()})
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	// An appended input persists as a state every earlier version can read.
	if !strings.Contains(string(data), `"state":"steering","text":"beta"`) {
		t.Fatalf("appended input stored an unknown state: %s", data)
	}
	records, _, err := readDeliveries(path, time.Now())
	if err != nil || records[idB].MergedInto != idA || !slices.Equal(records[idA].Merged, []string{idB}) || records[idB].State != "interrupted" {
		t.Fatalf("restart: %+v %v", records, err)
	}
	for _, bad := range []Delivery{
		{ID: idB, MergedInto: idB}, {ID: idB, MergedInto: "x"}, {ID: idA, Merged: []string{idA}},
		{ID: idA, Merged: []string{idB, idB}}, {ID: idB, MergedInto: idA, Merged: []string{idA}},
	} {
		if validMergeLinks(bad) {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestCoalesceFamilyRetiresTogether(t *testing.T) {
	now := time.Now()
	idA, idB, idC := deliveryTestID(82001), deliveryTestID(82002), deliveryTestID(82003)
	expired := now.Add(-time.Second).UnixMilli()
	records := map[string]Delivery{
		// B's own record is old and expired; A's is still retained.
		idA: {ID: idA, State: "cancelled", Order: 10, Merged: []string{idB}, created: now.UnixMilli(), expires: now.Add(time.Hour).UnixMilli()},
		idB: {ID: idB, State: "cancelled", Order: 1, MergedInto: idA, created: now.UnixMilli(), expires: expired},
		idC: {ID: idC, State: "dispatched", Order: 2, created: now.UnixMilli(), expires: expired},
	}
	var floor int64
	removed := pruneDeliveryRecords(records, &floor, now)
	if !slices.Equal(removed, []string{idC}) || records[idB].MergedInto != idA {
		t.Fatalf("a member was retired without its occurrence: removed %v", removed)
	}
	a := records[idA]
	a.expires = expired
	records[idA] = a
	removed = pruneDeliveryRecords(records, &floor, now)
	slices.Sort(removed)
	want := []string{idA, idB}
	slices.Sort(want)
	if !slices.Equal(removed, want) || len(records) != 0 {
		t.Fatalf("family not retired together: %v", removed)
	}
	for _, id := range want {
		if err := newDeliveryIDAllowed(id, floor, now); err == nil {
			t.Fatalf("retired member %s could be submitted again", id)
		}
	}
	// A pending member keeps an otherwise terminal primary (and itself) retained.
	records = map[string]Delivery{
		idA: {ID: idA, State: "unconfirmed", Order: 1, Merged: []string{idB}, expires: expired},
		idB: {ID: idB, State: "steering", Order: 2, MergedInto: idA},
	}
	if removed = pruneDeliveryRecords(records, &floor, now); len(removed) != 0 {
		t.Fatalf("pending family member retired: %v", removed)
	}
}

// Without every original (after a restart, or once released) a family's kept
// text is only the start of the whole message: it is never offered as the
// complete message to copy, and counts every input's images.
func TestCoalesceFamilyPreviewWithoutOriginals(t *testing.T) {
	u, idA, idB := newCoalesceUnit(t)
	u.m.forgetRecallLocked(u.a, idA)
	u.m.forgetRecallLocked(u.a, idB)
	u.m.mergeChildLocked(u.a, u.get(idA), u.get(idB))
	a := u.get(idA)
	if !a.Preview || a.Text != "alpha" || a.ImageCount != 3 || !slices.Equal(a.Merged, []string{idB}) {
		t.Fatalf("family receipt without originals %+v", a)
	}
	if view := u.a.deliveryView(a); view.OriginalText || view.OriginalPayload || view.CanCancel {
		t.Fatalf("incomplete family offered as whole: %+v", view)
	}
	if _, err := u.m.SubmissionPayload(u.a.info.ID, idA, u.a.info.Since); err == nil {
		t.Fatal("partial family payload returned")
	}
	// With every original the receipt shows the whole message, and the page
	// learns which inputs a payload joins.
	v, idA2, idB2 := newCoalesceUnit(t)
	v.m.mergeChildLocked(v.a, v.get(idA2), v.get(idB2))
	if a := v.get(idA2); a.Preview || a.Text != "alpha\nbeta" || a.ImageCount != 3 {
		t.Fatalf("family receipt %+v", a)
	}
	payload, err := v.m.SubmissionPayload(v.a.info.ID, idB2, v.a.info.Since)
	if err != nil || !slices.Equal(payload.Members, []string{idA2, idB2}) || payload.Text != "alpha\nbeta" || len(payload.Images) != 3 {
		t.Fatalf("family payload %+v %v", payload, err)
	}
}

// The host origin pi records carries a trusted context only when the app sent
// the input in one; every other field is the Origin's own.
func TestHostOriginWire(t *testing.T) {
	id := deliveryTestID(80100)
	plain, _ := json.Marshal(hostOriginWire{Origin: Origin{Source: OriginUser, Submission: id}})
	if string(plain) != `{"source":"user","submission":"`+id+`"}` {
		t.Fatalf("plain %s", plain)
	}
	topic, _ := json.Marshal(hostOriginWire{Origin: Origin{Source: OriginUser, Submission: id}, Context: "topic:tp3"})
	if string(topic) != `{"source":"user","submission":"`+id+`","context":"topic:tp3"}` {
		t.Fatalf("topic %s", topic)
	}
	scheduled, _ := json.Marshal(hostOriginWire{Origin: Origin{Source: OriginSchedule, Plan: "s1", Run: "s1-a", Submission: id}})
	if string(scheduled) != `{"source":"schedule","submission":"`+id+`","plan":"s1","run":"s1-a"}` {
		t.Fatalf("scheduled %s", scheduled)
	}
	for _, bad := range []string{"", "Topic:1", "-x", strings.Repeat("a", 65), "topic tp1"} {
		if hostContextRE.MatchString(bad) {
			t.Fatalf("context %q accepted", bad)
		}
	}
}
