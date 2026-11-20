package crew

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Label IDs are a wire and settings contract, independent of the UI language.
type Label struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Guidance string `json:"guidance"`
}

const DefaultLabel = "default"

var Labels = [...]Label{
	{"operations", "Operations & Research", "Release, deploy, run CI, verify execution, search for sources, or deliver research findings."},
	{"backend", "Backend Development", "Build or change an API, server, database, or other backend logic."},
	{"frontend", "Frontend & Design", "Create an interface, design, or artwork, including UI, UX, layout, typography, and drawing."},
	{DefaultLabel, "Default", "Choose a model for work that has no matching label or whose pool has no available models."},
}

const MaxPoolMembers = 16
const MaxGuidance = 1000 // Unicode code points
const maxPoolKeyBytes = 512

// Pool stores only user choices. Empty instructions use the label's built-in guidance.
type Pool struct {
	Instructions string   `json:"instructions,omitempty"`
	Members      []string `json:"members,omitempty"`
}

type Pools map[string]Pool

func (p Pool) Revision() string {
	b, _ := json.Marshal(p)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func validKey(key string) bool {
	return key != "" && len(key) <= maxPoolKeyBytes && utf8.ValidString(key) && strings.IndexFunc(key, unicode.IsControl) < 0
}

func (p Pool) Validate() error {
	if len(p.Instructions) > 4*MaxGuidance || !utf8.ValidString(p.Instructions) || utf8.RuneCountInString(p.Instructions) > MaxGuidance || strings.ContainsFunc(p.Instructions, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }) {
		return fmt.Errorf("Use at most 1,000 characters of plain text for dispatch instructions.")
	}
	if len(p.Members) > MaxPoolMembers {
		return fmt.Errorf("Choose up to 16 model and effort combinations per pool.")
	}
	for _, key := range p.Members {
		if !validKey(key) {
			return fmt.Errorf("Invalid model and effort key.")
		}
	}
	// Include escaping, not just the number of characters. This bounds both
	// the saved settings contribution and the data repeated in lead prompts.
	if raw, _ := json.Marshal(p); len(raw) > 16<<10 {
		return fmt.Errorf("These instructions and model keys exceed the pool’s 16 KiB budget.")
	}
	return nil
}

// Routing is one snapshot. Members use the same effective scores as Settings.
// On dispatch they are resolved against the catalog of the new worker itself.
type Routing struct {
	Pools   Pools
	Members []Member
	// CatalogUnknown identifies an advisory snapshot for the lead. Saved
	// candidates and score provenance are available before Pi verifies them.
	CatalogUnknown bool
}

// Candidates intersects the saved keys with usable, configured tuples. A
// damaged or future pool does not add candidates to the fixed labels.
func (r Routing) Candidates(label string) []Member {
	p := r.Pools[label]
	if !slices.ContainsFunc(Labels[:], func(l Label) bool { return l.ID == label }) || p.Validate() != nil {
		return nil
	}
	var out []Member
	seen := make(map[string]bool)
	for _, key := range p.Members {
		if m, ok := Find(r.Members, key); ok && !seen[key] {
			out = append(out, m)
			seen[key] = true
		}
	}
	return out
}

// Select honors the lead's preference within the first usable pool. If
// settings changed since its turn, the first remaining member in saved order
// is a deterministic substitute. No available member means inherit the lead.
func (r Routing) Select(label, preferred string) (Member, error) {
	if r.CatalogUnknown {
		return Member{}, fmt.Errorf("dispatch requires the worker’s live catalog")
	}
	if label == "" && len(r.Pools) == 0 && preferred != "" {
		// Pre-label extensions explicitly selected a flat Crew member. Keep
		// that contract only until the user configures routing.
		if m, ok := Find(r.Members, preferred); ok {
			return m, nil
		}
		return Member{}, fmt.Errorf("no crew member %q", preferred)
	}
	members := r.Candidates(label)
	if len(members) == 0 && label != DefaultLabel {
		members = r.Candidates(DefaultLabel)
	}
	if m, ok := Find(members, preferred); ok {
		return m, nil
	}
	if len(members) > 0 {
		return members[0], nil
	}
	return Member{}, nil
}
