package crew

import (
	"reflect"
	"testing"
)

// Keep the supplied GPT-6.2 figures tied to their exact effort through the
// embedded snapshot and routed model IDs, without reading a live pi catalog.
func TestEmbeddedGPT62Efforts(t *testing.T) {
	snap := Embedded()
	for _, c := range []struct {
		family, effort                                     string
		intelligence, speed, coding, cost, minutes, tokens float64
	}{
		{"Astra", "low", 45.7, 58.2, 41.8, 0.66, 1.5, 5216},
		{"Astra", "medium", 57, 58.6, 52.6, 1.62, 3.7, 13165},
		{"Astra", "high", 60.1, 60.7, 53.6, 2.07, 4.9, 17828},
		{"Astra", "xhigh", 61.6, 59.9, 56.5, 2.52, 6.2, 22238},
		{"Astra", "max", 62.1, 63.8, 54.8, 4, 10.7, 41029},
		{"Sol", "low", 40.6, 72, 34.6, 0.18, 1.4, 5980},
		{"Sol", "medium", 52.7, 74.5, 48.7, 0.41, 3.1, 13651},
		{"Sol", "high", 54.9, 79.3, 49.4, 0.56, 4.2, 20160},
		{"Sol", "xhigh", 55.7, 80.2, 49.4, 0.65, 5.1, 24323},
		{"Sol", "max", 57.3, 85.2, 52.2, 1.01, 7.9, 40360},
	} {
		t.Run(c.family+"/"+c.effort, func(t *testing.T) {
			id := "gpt-6.2-astra"
			if c.family == "Sol" {
				id = "gpt-6.2-sol"
			}
			m := Model{Provider: "synthetic", ID: "codex-remote/codex/" + id, Name: "GPT-6.2 " + c.family + " · Codex"}
			s, ok := snap.Lookup(m, c.effort)
			if !ok || s.Name != "GPT-6.2 "+c.family+" ("+c.effort+")" || s.Effort != c.effort || s.Intelligence != c.intelligence {
				t.Fatalf("wrong embedded model/effort: %+v, %v", s, ok)
			}
			m.Levels = []string{c.effort}
			members := Members(Config{Options: []Option{{Provider: m.Provider, Model: m.ID, Effort: c.effort}}}, []Model{m}, snap)
			if len(members) != 1 || members[0].Missing || members[0].Source != SourceAA || members[0].Score == nil || !reflect.DeepEqual(*members[0].Score, s) {
				t.Fatalf("member lost exact embedded score: %+v", members)
			}
			for _, f := range []struct {
				name string
				got  *float64
				want float64
			}{
				{"speed", s.Speed, c.speed}, {"coding", s.Coding, c.coding},
				{"costPerTask", s.CostPerTask, c.cost}, {"minutesPerTask", s.MinutesPerTask, c.minutes},
				{"tokensPerTask", s.TokensPerTask, c.tokens},
			} {
				if f.got == nil || *f.got != f.want {
					t.Errorf("%s = %v, want %v", f.name, f.got, f.want)
				}
			}
		})
	}
	for _, family := range []string{"astra", "sol"} {
		m := Model{ID: "route/gpt-6.2-" + family}
		for _, effort := range []string{"", "off", "minimal"} {
			if s, ok := snap.Lookup(m, effort); ok {
				t.Errorf("unmeasured %s/%q borrowed %+v", family, effort, s)
			}
		}
	}
	for _, id := range []string{"gpt-6.2", "gpt-6.2-sol-codex", "gpt-6.2-sol-fast", "gpt-6.2-astra-preview", "astra-for-law-6.2"} {
		if s, ok := snap.Lookup(Model{ID: "route/" + id}, "max"); ok {
			t.Errorf("unverified alias %q borrowed %+v", id, s)
		}
	}
}
