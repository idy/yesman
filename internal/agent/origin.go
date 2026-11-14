package agent

import (
	"slices"
	"sync"
)

// Origin says who wrote a prompt yesman sent to pi. pi's user messages carry
// no source: the user's submissions, the lead's messages to its workers and
// yesman's own notices (worker reports and questions, stopped runs, open
// tasks) all reach pi as ordinary prompts, distinguishable only by their text.
//
// yesman's extension asks for the origin of each input while its prompt
// command is still in flight (pi runs input handlers before it answers), and
// records it on the user message pi finalizes, under the message's "yesman"
// field. The window then tells people from yesman by that structure, never by
// the text. The prompt's text, images, delivery mode and third-party input
// handlers are untouched, and the field is not part of the model's context.
type Origin struct {
	Source string `json:"source"`         // OriginUser, OriginLead or OriginYesman
	Kind   string `json:"kind,omitempty"` // lead: "task" or "message"; yesman: "report", "question", "stopped", "failed" or "tasks"
	// Worker names the worker a yesman notice is about.
	Worker string `json:"worker,omitempty"`
	// Submission is the receipt ID of a user's submission from the window.
	Submission string `json:"submission,omitempty"`
}

const (
	OriginUser   = "user"   // a person, from the window
	OriginLead   = "lead"   // a worker's lead: its task, or a message to it
	OriginYesman = "yesman" // yesman itself, telling a lead about its workers
)

// originClaim is a prompt in flight to one process: registered just before
// its command is written and released when pi answers it or exits. Claims
// live only in memory; pi's session keeps the recorded origins.
type originClaim struct {
	proc        *proc
	fingerprint string
	origin      Origin
}

type originClaims struct {
	mu     sync.Mutex // guards claims only; never held with the manager's mu
	claims []*originClaim
}

// claim registers a prompt about to be sent to p, returning its release.
// Unknown origins are registered too, so that they make a lookup ambiguous.
func (m *Manager) claim(p *proc, text string, images []Image, o Origin) (release func()) {
	c := &originClaim{proc: p, fingerprint: deliveryFingerprint(text, images), origin: o}
	m.origins.mu.Lock()
	m.origins.claims = append(m.origins.claims, c)
	m.origins.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.origins.mu.Lock()
			m.origins.claims = slices.DeleteFunc(m.origins.claims, func(x *originClaim) bool { return x == c })
			m.origins.mu.Unlock()
		})
	}
}

// PromptOrigin returns the origin of the prompt now in flight to the process
// holding token whose text and images have this fingerprint, the same as a
// receipt's. It reports false unless the prompts in flight with that
// fingerprint agree on who wrote them; a submission ID is kept only when it is
// the single one, so identical texts are never bound to the wrong receipt.
func (m *Manager) PromptOrigin(token, fingerprint string) (Origin, bool) {
	if token == "" || fingerprint == "" {
		return Origin{}, false
	}
	m.mu.Lock()
	var p *proc
	if id, ok := m.tokens[token]; ok {
		if a := m.agents[id]; a != nil && a.token == token {
			p = a.proc
		}
	}
	m.mu.Unlock()
	if p == nil {
		return Origin{}, false
	}
	m.origins.mu.Lock()
	defer m.origins.mu.Unlock()
	var found *Origin
	for _, c := range m.origins.claims {
		if c.proc != p || c.fingerprint != fingerprint {
			continue
		}
		o := c.origin
		if o.Source == "" {
			return Origin{}, false
		}
		if found == nil {
			found = &o
			continue
		}
		if found.Source != o.Source || found.Kind != o.Kind || found.Worker != o.Worker {
			return Origin{}, false
		}
		if found.Submission != o.Submission {
			found.Submission = ""
		}
	}
	if found == nil {
		return Origin{}, false
	}
	return *found, true
}
