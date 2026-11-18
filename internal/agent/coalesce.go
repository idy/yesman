package agent

import (
	"encoding/base64"
	"slices"
	"strings"
	"time"
)

// Capability 4 appends a later human input into the still-pending native
// occurrence of an earlier one, so pi takes one message. pi decides at its own
// synchronous enqueue point (append, or enqueue as a new occurrence) within the
// same RPC; Go only proposes the target and never resubmits. Receipts keep each
// input's own immutable ID and fingerprint: the first input owns the occurrence
// and lists the appended IDs in Merged, each appended input names it in
// MergedInto. Complete payloads stay in the bounded volatile recall cache.
const (
	// pi bounds one occurrence to its primary input plus this many appended ones.
	maxMergedInputs = 15
	// A composer submission waits this long for an earlier one's RPC so it can
	// see that occurrence; afterwards it is sent as its own message.
	appendSerialWait = 3 * time.Second
	segmentSeparator = "\n"
)

// joinSegmentText is pi's canonical merge of two inputs' text blocks: one
// newline between two non-empty texts, each kept verbatim, none around an
// empty one (an image-only input).
func joinSegmentText(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + segmentSeparator + b
}

// decodedImageBytes is the decoded size of an already validated base64 image.
func decodedImageBytes(img Image) int {
	n := base64.StdEncoding.DecodedLen(len(img.Data))
	return n - (len(img.Data) - len(strings.TrimRight(img.Data, "=")))
}

// members lists the submission IDs forming this occurrence, primary first.
func (d Delivery) members() []string {
	return append([]string{d.ID}, d.Merged...)
}

// aggregatePayloadLocked returns the complete current logical message: every
// member's original text joined as pi joins them, then all images in order. It
// is unavailable unless every member's original payload is retained.
func (a *agent) aggregatePayloadLocked(root Delivery) (RecallPayload, bool) {
	var text string
	var images []Image
	for _, id := range root.members() {
		payload, ok := a.recallPayloads[id]
		if !ok {
			return RecallPayload{}, false
		}
		text = joinSegmentText(text, payload.Text)
		images = append(images, payload.Images...)
	}
	return RecallPayload{Text: strings.Clone(text), Images: slices.Clone(images)}, true
}

// rootDeliveryLocked resolves an appended input to the occurrence it joined.
// Without a receipt the ID stands for itself and ok is false.
func (a *agent) rootDeliveryLocked(id string) (Delivery, bool) {
	d, ok := a.deliveries[id]
	if !ok {
		return Delivery{ID: id}, false
	}
	if d.MergedInto != "" {
		d, ok = a.deliveries[d.MergedInto]
		if !ok || d.MergedInto != "" {
			return Delivery{}, false
		}
	}
	return d, true
}

// hostOriginWire is the host origin sent with an input for pi's own identity of
// it: who wrote it and, for a message sent within one, the trusted
// conversation context. pi keeps it beside the message, never in it.
type hostOriginWire struct {
	Origin
	Context string `json:"context,omitempty"`
}

// joinableText excludes slash input. pi expands /skill and /template inputs and
// runs extension commands before queueing, so a merged message would mix an
// expansion with ordinary text; such inputs always stay their own messages.
func joinableText(text string) bool { return !strings.HasPrefix(strings.TrimSpace(text), "/") }

// appendTargetLocked proposes the occurrence a composer submission may join.
// Only the newest pending occurrence qualifies, and only when it is itself a
// composer submission (root.coalesce) in the same lane, session
// and agent generation, unclaimed by an idle preparation, with every member's
// complete payload retained and the combined images within the single-message
// limits. The revision is pi's own current one for exactly that segment list,
// so pi rejects (and queues separately) anything that changed meanwhile.
func (m *Manager) appendTargetLocked(a *agent, p *proc, d Delivery, behavior string) (map[string]any, string) {
	q := a.nativeQueue
	if !d.coalesce || !q.supportsAppend() || !q.AppendAllowed || q.Epoch == "" || len(q.Items) == 0 || a.proc != p || a.retiring {
		return nil, ""
	}
	last := q.Items[len(q.Items)-1] // pi lists pending occurrences in enqueue order
	kind := "steer"
	if behavior == "followUp" {
		kind = "followUp"
	}
	if last.Kind != kind || last.Preparing || !deliveryID.MatchString(last.SubmissionID) || last.SubmissionID == d.ID || len(last.Segments) >= maxMergedInputs {
		return nil, ""
	}
	root, ok := a.deliveries[last.SubmissionID]
	session := p.deliverySession()
	// Only another composer input is a target: never a scheduled run, a topic's
	// message, a plain submission, or an occurrence restored without that fact.
	// pi independently requires a user origin and the same trusted context.
	if !ok || !root.coalesce || root.Origin != nil || root.hostContext != d.hostContext ||
		root.MergedInto != "" || root.Session != session || !root.AgentSince.Equal(a.info.Since) || !d.AgentSince.Equal(a.info.Since) ||
		root.queueID != last.QueueID || root.queueEpoch != q.Epoch || root.queueRevision != last.ItemRevision ||
		root.State != "steering" && root.State != "queued" {
		return nil, ""
	}
	payload, ok := a.recallPayloads[d.ID]
	if !ok || !joinableText(payload.Text) {
		return nil, "" // the aggregate could not be restored completely, or a slash input
	}
	images := len(payload.Images)
	total := 0
	for _, img := range payload.Images {
		total += decodedImageBytes(img)
	}
	for _, id := range append([]string{root.ID}, last.Segments...) {
		member, ok := a.deliveries[id]
		retained, held := a.recallPayloads[id]
		if id == d.ID || !ok || !held || !joinableText(retained.Text) || member.Session != session || !member.AgentSince.Equal(a.info.Since) {
			return nil, ""
		}
		images += len(retained.Images)
		for _, img := range retained.Images {
			total += decodedImageBytes(img)
		}
	}
	if images > maxImages || total > maxTotalImageBytes {
		return nil, ""
	}
	return map[string]any{"epoch": q.Epoch, "queueId": last.QueueID, "expectedRevision": last.ItemRevision, "controlGeneration": q.ControlGeneration}, root.ID
}

// appendedRootLocked finds the occurrence pi reported appending d into.
func (m *Manager) appendedRootLocked(a *agent, p *proc, d Delivery, queueID string) (Delivery, bool) {
	valid := func(root Delivery) bool {
		return root.ID != d.ID && root.MergedInto == "" && root.Session == d.Session && root.AgentSince.Equal(d.AgentSince)
	}
	if queueID != "" {
		for _, root := range a.deliveries {
			if root.queueID == queueID && root.queueEpoch == a.nativeQueue.Epoch && valid(root) {
				return root, true
			}
		}
	}
	root, ok := a.deliveries[d.appendRoot]
	return root, ok && valid(root) && !pendingDelivery(root.State)
}

// mergeChildLocked records child as appended to root. A pending root lends
// the child its lane; an already taken or cancelled root its final state.
func (m *Manager) mergeChildLocked(a *agent, root, child Delivery) []Event {
	var events []Event
	if !slices.Contains(root.Merged, child.ID) && len(root.Merged) < maxMergedInputs {
		m.setMergedLocked(a, &root, append(slices.Clone(root.Merged), child.ID))
		events = append(events, m.deliveryLocked(a, root))
		root = a.deliveries[root.ID]
	}
	state := root.State
	if state == "sending" || state == "accepted" {
		state = "steering"
	}
	if child.MergedInto != root.ID || child.State != state {
		child.MergedInto, child.State, child.queueID, child.appendRoot = root.ID, state, "", ""
		if state == "unconfirmed" || state == "interrupted" {
			m.forgetRecallLocked(a, child.ID)
		}
		events = append(events, m.deliveryLocked(a, child))
	}
	return events
}

// setMergedLocked records an occurrence's appended inputs and what its
// receipt shows of the whole message.
func (m *Manager) setMergedLocked(a *agent, root *Delivery, merged []string) {
	var added []string
	for _, id := range merged {
		if !slices.Contains(root.Merged, id) {
			added = append(added, id)
		}
	}
	root.Merged = merged
	if !m.refreshAggregatePreviewLocked(a, root) {
		// Without every original the kept text is only the start of the
		// whole message: never present it, or copy it, as complete.
		root.Preview = true
		for _, id := range added {
			root.ImageCount += a.deliveries[id].ImageCount
		}
	}
}

// refreshAggregatePreviewLocked shows what pi will take: the bounded preview
// and image count of the whole logical message. The fingerprint stays the
// primary input's own, so its ID's retry is still recognized.
func (m *Manager) refreshAggregatePreviewLocked(a *agent, root *Delivery) bool {
	payload, ok := a.aggregatePayloadLocked(*root)
	if !ok || len(payload.Images) > maxImages {
		return false
	}
	root.Text, root.Preview = deliveryClip(payload.Text, maxDeliveryPreview)
	root.ImageCount = len(payload.Images)
	return true
}

// acquireAppendGate serializes composer submissions to one agent, so a later
// input sees the earlier one's occurrence and revision. The wait is bounded:
// afterwards the input is sent as its own message, which is always safe.
func (m *Manager) acquireAppendGate(a *agent, p *proc) func() {
	m.mu.Lock()
	if a.appendGate == nil {
		a.appendGate = make(chan struct{}, 1)
	}
	gate := a.appendGate
	m.mu.Unlock()
	timer := time.NewTimer(appendSerialWait)
	defer timer.Stop()
	select {
	case gate <- struct{}{}:
		return func() { <-gate }
	case <-timer.C:
	case <-p.done:
	case <-m.shutdown:
	}
	return func() {}
}
