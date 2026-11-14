package crew

import (
	"encoding/json"
	"slices"
	"testing"
	"testing/fstest"
)

func TestEmbedded(t *testing.T) {
	s := Embedded()
	if s.Date != "2026-11-14" {
		t.Errorf("date = %q", s.Date)
	}
	if len(s.Scores) != 50 {
		t.Fatalf("%d scores, want 50", len(s.Scores))
	}
	first := s.Scores[0]
	if first.Name != "Claude Fable 5.2 (max)" || first.Base != "Claude Fable 5.2" || first.Effort != "max" || first.Rank != 1 {
		t.Errorf("first = %+v", first)
	}
	if first.Intelligence != 64.6 || first.Speed == nil || *first.Speed != 97.5 || first.Coding == nil || *first.Coding != 58.7 ||
		first.CostPerTask == nil || *first.CostPerTask != 26.54 || first.MinutesPerTask == nil || *first.MinutesPerTask != 46.8 {
		t.Errorf("first's figures = %+v", first)
	}
	for _, sc := range s.Scores {
		if sc.Intelligence <= 0 || sc.Base == "" {
			t.Errorf("score %+v", sc)
		}
	}
	bases := s.Bases()
	for _, want := range []string{"Claude Opus 5.5", "GPT-6.1 Sol", "Kimi K3.1 Pro", "Qwen3.8 Max (0902)"} {
		if !slices.Contains(bases, want) {
			t.Errorf("no base %q in %v", want, bases)
		}
	}
}

func TestNewestSnapshotWins(t *testing.T) {
	header := "rank,model,creator,intelligence_index\n"
	fsys := fstest.MapFS{
		"scores/aa-2026-01-02.csv": {Data: []byte(header + "1,Old,X,1\n")},
		"scores/aa-2026-03-04.csv": {Data: []byte(header + "1,New (high),X,2\n")},
		"scores/notes.txt":         {Data: []byte("not scores")},
	}
	s, err := loadNewest(fsys, "scores")
	if err != nil {
		t.Fatal(err)
	}
	if s.Date != "2026-03-04" || len(s.Scores) != 1 || s.Scores[0].Base != "New" || s.Scores[0].Effort != "high" {
		t.Errorf("snapshot = %+v", s)
	}
}

func TestParse(t *testing.T) {
	scores, err := Parse("model,intelligence_index,tok_per_s\nA (Non-reasoning),10,\nB,20,30.5\n")
	if err != nil {
		t.Fatal(err)
	}
	if scores[0].Base != "A" || scores[0].Effort != "off" || scores[0].Speed != nil {
		t.Errorf("A = %+v", scores[0])
	}
	if scores[1].Base != "B" || scores[1].Effort != "" || *scores[1].Speed != 30.5 {
		t.Errorf("B = %+v", scores[1])
	}
	for _, bad := range []string{"", "model\nA\n", "model,intelligence_index\nA,\n", "model,intelligence_index\nA,x\n"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) took it", bad)
		}
	}
}

func TestSplitEffort(t *testing.T) {
	for name, want := range map[string][2]string{
		"GPT-6.1 Sol (xhigh)":            {"GPT-6.1 Sol", "xhigh"},
		"Claude Opus 5.5 (Max)":          {"Claude Opus 5.5", "max"},
		"Qwen3.8 Max (0902)":             {"Qwen3.8 Max (0902)", ""},
		"Kimi K3.1 Pro":                  {"Kimi K3.1 Pro", ""},
		"DeepSeek V4.1 Flash 1017 (max)": {"DeepSeek V4.1 Flash 1017", "max"},
	} {
		if base, effort := splitEffort(name); base != want[0] || effort != want[1] {
			t.Errorf("splitEffort(%q) = %q, %q", name, base, effort)
		}
	}
}

func TestSupportedLevels(t *testing.T) {
	levels := func(reasoning bool, m string) []string {
		var lm map[string]json.RawMessage
		if m != "" {
			if err := json.Unmarshal([]byte(m), &lm); err != nil {
				t.Fatal(err)
			}
		}
		return SupportedLevels(reasoning, lm)
	}
	if got := levels(false, ""); !slices.Equal(got, []string{"off"}) {
		t.Errorf("no reasoning: %v", got)
	}
	if got := levels(true, ""); !slices.Equal(got, []string{"off", "minimal", "low", "medium", "high"}) {
		t.Errorf("no map: %v", got)
	}
	got := levels(true, `{"high":"high","low":"low","max":"max","medium":null,"minimal":null,"off":null,"xhigh":null}`)
	if !slices.Equal(got, []string{"low", "high", "max"}) {
		t.Errorf("kimi's map: %v", got)
	}
}

func TestSuggest(t *testing.T) {
	bases := Embedded().Bases()
	for _, c := range []struct {
		id, name, want string
	}{
		{"auto/claude-opus-5-5", "Claude Opus 5.5 · routing group", "Claude Opus 5.5"},
		{"codex-remote/codex/gpt-6.1-sol", "GPT-6.1 Sol · Codex · Codex Magpie", "GPT-6.1 Sol"},
		{"codex-remote/codex/gpt-6-astra", "GPT-6 Astra · Codex", "GPT-6 Astra"},
		{"china-remote/kimi-cn/kimi-k3.1-pro", "Kimi K3.1 Pro · China Magpie", "Kimi K3.1 Pro"},
		{"china-remote/kimi-cn/kimi-k3.1-pro-highspeed", "Kimi K3.1 Pro Highspeed · China Magpie", ""},
		{"deepseek-flash", "DeepSeek Flash", ""},
		{"x", "GPT-6.1 Terra", "GPT-6.1 Terra"}, // by name when the ID says nothing
	} {
		if got := Suggest(Model{Provider: "magpie", ID: c.id, Name: c.name}, bases); got != c.want {
			t.Errorf("Suggest(%s) = %q, want %q", c.id, got, c.want)
		}
	}
}

func TestParseKey(t *testing.T) {
	for key, want := range map[string][3]string{
		"magpie/codex-remote/codex/gpt-6.1-sol:high": {"magpie", "codex-remote/codex/gpt-6.1-sol", "high"},
		"magpie/auto/claude-opus-5-5":                {"magpie", "auto/claude-opus-5-5", ""},
		"bedrock/anthropic.claude-v2:1":              {"bedrock", "anthropic.claude-v2:1", ""},
	} {
		p, m, e, ok := ParseKey(key)
		if !ok || p != want[0] || m != want[1] || e != want[2] {
			t.Errorf("ParseKey(%q) = %q %q %q %v", key, p, m, e, ok)
		}
		if MemberKey(p, m, e) != key {
			t.Errorf("MemberKey does not give %q back", key)
		}
	}
	for _, bad := range []string{"", "magpie", "/x", "magpie/", "magpie/:high"} {
		if _, _, _, ok := ParseKey(bad); ok {
			t.Errorf("ParseKey(%q) took it", bad)
		}
	}
}

func TestMembers(t *testing.T) {
	snap := Snapshot{Scores: []Score{
		{Name: "Sol (high)", Base: "Sol", Effort: "high", Intelligence: 50},
		{Name: "Sol (max)", Base: "Sol", Effort: "max", Intelligence: 55},
		{Name: "Sol (minimal)", Base: "Sol", Effort: "minimal", Intelligence: 20},
		{Name: "Kimi", Base: "Kimi", Intelligence: 40},
		{Name: "Other (high)", Base: "Other", Effort: "high", Intelligence: 60},
	}}
	models := []Model{
		{Provider: "p", ID: "sol", Name: "Sol · served here", Levels: []string{"low", "high", "max"}},
		{Provider: "p", ID: "kimi", Name: "Kimi", Levels: []string{"low", "high"}},
		{Provider: "p", ID: "unknown", Levels: []string{"high"}},
	}
	mine := &Score{Intelligence: 33}
	c := Config{Options: []Option{
		{Provider: "p", Model: "sol", Effort: "high"},
		{Provider: "p", Model: "sol", Effort: "low", Score: mine}, // AA has no Sol (low)
		{Provider: "p", Model: "kimi", Effort: "low"},             // AA's one Kimi, at any effort
		{Provider: "p", Model: "unknown", Effort: "high"},
		{Provider: "p", Model: "gone", Effort: "high", Score: mine},
		{Provider: "p", Model: "sol", Effort: "minimal", Score: mine}, // sol takes no minimal
	}}
	got := Members(c, models, snap)
	if len(got) != 6 {
		t.Fatalf("members = %+v", got)
	}
	for i, want := range []struct {
		name, source string
		intel        float64
		missing      bool
	}{
		{"Sol", SourceAA, 50, false},
		{"Sol", SourceUser, 33, false},
		{"Kimi", SourceAA, 40, false},
		{"", "", 0, false},
		{"", SourceUser, 33, true},
		{"Sol", SourceAA, 20, true},
	} {
		m := got[i]
		var intel float64
		if m.Score != nil {
			intel = m.Score.Intelligence
		}
		if ShortName(m.ModelName, "") != want.name || m.Source != want.source || intel != want.intel || m.Missing != want.missing {
			t.Errorf("member %d = %+v, want %+v", i, m, want)
		}
	}
	if got[1].Score == mine {
		t.Error("a member shares the option's score")
	}
	if !Usable(got, "p/sol:high") || Usable(got, "p/gone:high") || Usable(got, "p/sol:max") {
		t.Error("Usable is wrong")
	}
	var order []string
	for _, m := range Smartest(got) {
		order = append(order, m.Key)
	}
	if want := []string{"p/sol:high", "p/kimi:low", "p/sol:low", "p/gone:high", "p/sol:minimal", "p/unknown:high"}; !slices.Equal(order, want) {
		t.Errorf("smartest = %v, want %v", order, want)
	}
	// Without pi's catalog, nothing is missing.
	if got := Members(c, nil, snap); got[4].Missing || got[5].Missing {
		t.Errorf("without a catalog: %+v", got)
	}
	if c.Index("p/kimi:low") != 2 || c.Index("p/kimi:high") != -1 {
		t.Error("Index is wrong")
	}
}

func TestLookup(t *testing.T) {
	snap := Embedded()
	opus := Model{Provider: "magpie", ID: "auto/claude-opus-5-5", Name: "Claude Opus 5.5 · routing group"}
	if s, ok := snap.Lookup(opus, "high"); !ok || s.Name != "Claude Opus 5.5 (high)" {
		t.Errorf("opus at high = %+v, %v", s, ok)
	}
	if _, ok := snap.Lookup(opus, "minimal"); ok {
		t.Error("AA has no Opus at minimal")
	}
	kimi := Model{Provider: "magpie", ID: "china-remote/kimi-cn/kimi-k3.1-pro", Name: "Kimi K3.1 Pro · China Magpie"}
	if s, ok := snap.Lookup(kimi, "max"); !ok || s.Name != "Kimi K3.1 Pro" {
		t.Errorf("kimi = %+v, %v", s, ok)
	}
	if _, ok := snap.Lookup(Model{ID: "deepseek-flash", Name: "DeepSeek Flash"}, "max"); ok {
		t.Error("found a score for an unknown model")
	}
}

func TestCheckScore(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	if err := CheckScore(Score{Intelligence: 40, Speed: f(80), Coding: f(50), CostPerTask: f(0), MinutesPerTask: f(9)}); err != nil {
		t.Error(err)
	}
	for _, bad := range []Score{{}, {Intelligence: 101}, {Intelligence: 40, Speed: f(-1)}, {Intelligence: 40, Coding: f(120)}} {
		if CheckScore(bad) == nil {
			t.Errorf("took %+v", bad)
		}
	}
}
