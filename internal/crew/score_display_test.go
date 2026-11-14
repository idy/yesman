package crew

import (
	"encoding/json"
	"testing"
)

// Exercise the path used by both CrewView and the lead's context, including
// optional figures and the source metadata the page uses to explain effort.
func TestScoreFiguresAndEffortProvenance(t *testing.T) {
	scores, err := Parse(`model,intelligence_index,tok_per_s,coding_mean_pct,cost_per_task_usd,time_min_per_task
Orion (high),52.5,48.1,49.4,0.76,10.5
Orion (max),54.6,48.6,50.3,1.27,23.3
Plain,0,0,,0,0
Mixed,20,100,30,1,2
Mixed (high),40,80,50,2,3
`)
	if err != nil {
		t.Fatal(err)
	}
	snap := Snapshot{Scores: scores}
	models := []Model{
		{Provider: "synthetic", ID: "routed/orion", Name: "Orion · route", Levels: []string{"low", "high", "max"}},
		{Provider: "synthetic", ID: "plain", Name: "Plain", Levels: []string{"low", "high"}},
		{Provider: "synthetic", ID: "mixed", Name: "Mixed", Levels: []string{"low", "high"}},
		{Provider: "synthetic", ID: "unknown", Name: "Unknown", Levels: []string{"off"}},
	}
	config := Config{Options: []Option{
		{Provider: "synthetic", Model: "routed/orion", Effort: "high"},
		{Provider: "synthetic", Model: "routed/orion", Effort: "max"},
		{Provider: "synthetic", Model: "routed/orion", Effort: "low", Score: &Score{Intelligence: 12}},
		{Provider: "synthetic", Model: "plain", Effort: "low"},
		{Provider: "synthetic", Model: "plain", Effort: "high"},
		{Provider: "synthetic", Model: "mixed", Effort: "low"},
		{Provider: "synthetic", Model: "unknown", Effort: "off"},
	}}
	members := Members(config, models, snap)
	for i, want := range []struct {
		effort                              string
		intel, cost, speed, coding, minutes float64
	}{
		{"high", 52.5, 0.76, 48.1, 49.4, 10.5},
		{"max", 54.6, 1.27, 48.6, 50.3, 23.3},
	} {
		m := members[i]
		if m.Source != SourceAA || m.Score == nil {
			t.Fatalf("member %d lost its AA score: %+v", i, m)
		}
		s := m.Score
		if s.Effort != want.effort || s.Intelligence != want.intel || s.CostPerTask == nil || *s.CostPerTask != want.cost || s.Speed == nil || *s.Speed != want.speed || s.Coding == nil || *s.Coding != want.coding || s.MinutesPerTask == nil || *s.MinutesPerTask != want.minutes {
			t.Errorf("member %d has the wrong effort or units: %+v", i, s)
		}
	}
	if m := members[2]; m.Source != SourceUser || m.Score.Intelligence != 12 || m.Score.Effort != "" || m.Score.Speed != nil {
		t.Errorf("an unmeasured effort must keep the user's partial estimate: %+v", m)
	}
	for _, m := range members[3:5] {
		if m.Source != SourceAA || m.Score.Name != "Plain" || m.Score.Effort != "" || m.Score.Intelligence != 0 {
			t.Errorf("a model-level score must keep its unspecified effort: %+v", m)
		}
		data, err := json.Marshal(m.Score)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(data, &wire); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"intelligence", "speed", "costPerTask", "minutesPerTask"} {
			if value, present := wire[key]; !present || value != float64(0) {
				t.Errorf("a real zero %s must survive JSON: %s", key, data)
			}
		}
		if _, present := wire["coding"]; present {
			t.Errorf("an unmeasured coding figure must stay absent: %s", data)
		}
	}
	for _, m := range members[5:] {
		if m.Score != nil || m.Source != "" {
			t.Errorf("no score should be fabricated for an unmatched model/effort: %+v", m)
		}
	}
}
