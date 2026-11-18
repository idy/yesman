package schedule

import (
	"testing"

	"github.com/idy/yesman/internal/agent"
)

// R5-F5: pi's own queue identifies a scheduled submission before pi answers
// the command, so its receipt may say queued, then dispatched, with no Ack;
// a receipt kept from before has none either. Either is acceptance: the run
// leaves sending, and no later state is taken back.
func TestNativeReceiptStatesWithoutAck(t *testing.T) {
	const id = "01a1eb57f673db7bcdc8f54715307de164d20c417fcacd50"
	run := func(state string) *Run {
		return &Run{State: state, Attempts: []Attempt{{ID: id, State: "sending"}}}
	}
	type step struct {
		receipt  agent.Delivery
		state    string
		definite bool
	}
	for name, steps := range map[string][]step{
		"queued, then taken": {
			{agent.Delivery{ID: id, State: "queued", Revision: 2}, StateQueued, false},
			{agent.Delivery{ID: id, State: "dispatched", Revision: 3}, StateSent, false},
			// A late answer to the command takes nothing back.
			{agent.Delivery{ID: id, State: "dispatched", Ack: "queued", Revision: 4}, StateSent, false},
		},
		"taken before it was read": {
			{agent.Delivery{ID: id, State: "dispatched", Revision: 3}, StateSent, false},
		},
		"queued, then its lead stopped": {
			{agent.Delivery{ID: id, State: "queued", Revision: 2}, StateQueued, false},
			{agent.Delivery{ID: id, State: "interrupted", Revision: 3}, StateUnknown, false},
		},
		"queued, then taken out of the queue": {
			{agent.Delivery{ID: id, State: "queued", Revision: 2}, StateQueued, false},
			{agent.Delivery{ID: id, State: "cancelled", Revision: 3}, StateFailed, true},
		},
		"answered queued, then taken": {
			{agent.Delivery{ID: id, State: "queued", Ack: "queued", Revision: 2}, StateQueued, false},
			{agent.Delivery{ID: id, State: "dispatched", Ack: "queued", Revision: 3}, StateSent, false},
		},
	} {
		r := run(StateSending)
		for i, s := range steps {
			d := s.receipt
			applyReceipt(r, id, &d, "")
			if r.State != s.state || r.Attempts[0].Definite != s.definite {
				t.Errorf("%s, step %d (%s/%q): run %s definite %v, want %s %v", name, i, d.State, d.Ack, r.State, r.Attempts[0].Definite, s.state, s.definite)
			}
		}
	}
	// A run that is sent is not made queued, failed or unknown by a later
	// receipt state.
	r := run(StateSent)
	for _, state := range []string{"queued", "cancelled", "interrupted", "unconfirmed"} {
		applyReceipt(r, id, &agent.Delivery{ID: id, State: state, Revision: 9}, "")
		if r.State != StateSent {
			t.Fatalf("a sent run became %s on %s", r.State, state)
		}
	}
}
