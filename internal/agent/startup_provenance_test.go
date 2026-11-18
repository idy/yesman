package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// Delivers a post-response event before send returns to the waiting caller.
// Assigning provenance on the Manager/caller side is deterministically too late.
type startupStateWire struct {
	p          *proc
	capability int
	observed   bool
}

func (w *startupStateWire) Close() error { return nil }
func (w *startupStateWire) Write(b []byte) (int, error) {
	var cmd struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &cmd); err != nil {
		return 0, err
	}
	response, _ := json.Marshal(map[string]any{"type": "response", "id": cmd.ID, "success": true, "data": map[string]any{"pendingQueue": map[string]any{"capability": w.capability}}})
	onRecord := func(p *proc, kind string, raw json.RawMessage) {
		p.origins.observe(kind, raw)
		w.observed = p.origins.native
	}
	w.p.handle(response, onRecord)
	w.p.handle([]byte(`{"type":"queue_update","steering":[],"followUp":[]}`), onRecord)
	return len(b), nil
}
func TestStartupProvenancePrecedesPostResponseEvents(t *testing.T) {
	for _, capability := range []int{0, 1, 2, 3, 4, 5, 99} {
		t.Run(fmt.Sprint(capability), func(t *testing.T) {
			p := &proc{pending: map[string]waiter{}, done: make(chan struct{})}
			wire := &startupStateWire{p: p, capability: capability}
			p.stdin = wire
			state, err := p.startupState(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := capability >= 2 && capability <= 4
			if state.PendingQueue.Capability != capability || wire.observed != want || p.origins.native != want {
				t.Fatalf("post-response event ran in wrong provenance mode: capability=%d observed=%v current=%v", capability, wire.observed, p.origins.native)
			}
			// Subsequent model/session refreshes cannot change the process mode.
			wire.capability = 0
			if _, err = p.modelState(context.Background()); err != nil {
				t.Fatal(err)
			}
			if p.origins.native != want {
				t.Fatal("provenance mode changed after startup")
			}
		})
	}
}
