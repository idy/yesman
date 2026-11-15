package agent

import (
	"encoding/json"
	"strings"
)

type hostIdentity struct {
	SubmissionID string `json:"submissionId"`
	Origin       Origin `json:"origin"`
	Epoch        string `json:"epoch"`
	QueueID      string `json:"queueId"`
}
type hostInputRef struct {
	Native   bool          `json:"native"`
	EntryID  string        `json:"entryId"`
	Identity *hostIdentity `json:"identity"`
}

func nativeOrigin(i *hostIdentity) *Origin {
	if i == nil {
		return nil
	}
	o := i.Origin
	if deliveryID.MatchString(i.SubmissionID) {
		o.Submission = i.SubmissionID
	}
	b, _ := json.Marshal(o)
	v, ok := validOrigin(b)
	if !ok {
		return nil
	}
	return &v
}

// Host event envelope comes from the managed queue occurrence, never message
// contents. It is projected into the existing UI-only yesman field after pi.
func projectNativeEvent(raw json.RawMessage) json.RawMessage {
	var e struct {
		Message  json.RawMessage `json:"message"`
		Identity *hostIdentity   `json:"inputIdentity"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return raw
	}
	var m struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(e.Message, &m)
	var origin *Origin
	if m.Role == "user" {
		origin = nativeOrigin(e.Identity)
	}
	return withOrigin(raw, e.Message, origin)
}
func projectNativeMessages(raw json.RawMessage, refs []hostInputRef) json.RawMessage {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return raw
	}
	for n, m := range list {
		var head struct {
			Role string `json:"role"`
		}
		_ = json.Unmarshal(m, &head)
		var origin *Origin
		if len(refs) == len(list) && head.Role == "user" && refs[n].Native {
			origin = nativeOrigin(refs[n].Identity)
		}
		list[n] = withField(m, origin)
	}
	out, err := json.Marshal(list)
	if err != nil {
		return raw
	}
	return out
}
func (m *Manager) observeNativeInput(a *agent, p *proc, kind string, raw json.RawMessage) {
	if kind != "message_start" && kind != "input_recorded" {
		return
	}
	var e struct {
		HostSession string        `json:"hostSession"`
		Identity    *hostIdentity `json:"inputIdentity"`
		Message     struct {
			Role string `json:"role"`
		} `json:"message"`
		EntryID string `json:"entryId"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Identity == nil || kind == "message_start" && e.Message.Role != "user" {
		return
	}
	m.reconcileRecordedInputs(a, p, e.HostSession, []hostInputRef{{Native: true, EntryID: e.EntryID, Identity: e.Identity}})
}
func (m *Manager) reconcileRecordedInputs(a *agent, p *proc, session string, refs []hostInputRef) {
	m.mu.Lock()
	if m.agents[a.info.ID] != a || a.proc != p || a.retiring || a.removing || a.deliverySealed || !a.nativeQueue.supportsInputIdentity() || session == "" || session != p.piSession.Load().(string) {
		m.mu.Unlock()
		return
	}
	var events []Event
	for _, ref := range refs {
		if !ref.Native || ref.Identity == nil || !deliveryID.MatchString(ref.Identity.SubmissionID) {
			continue
		}
		d, ok := a.deliveries[ref.Identity.SubmissionID]
		if !ok || !strings.HasSuffix(d.Session, ":"+session) || !d.AgentSince.Equal(a.info.Since) || d.State == "cancelled" || d.State == "dispatched" {
			continue
		}
		// A recorded input on the canonical session branch proves intake even when
		// compaction omitted its message from the current model context. It is not
		// a replay permission and must never override a cancellation tombstone.
		d.State = "dispatched"
		d.queueID = ""
		events = append(events, m.deliveryLocked(a, d))
	}
	m.mu.Unlock()
	for _, event := range events {
		m.cfg.OnEvent(event)
	}
}
