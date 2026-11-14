package usage

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// TestRealPiSession reads a session the installed pi wrote, run by
// scripts/check-usage-pi.mjs against a local deterministic server; it is
// skipped otherwise. pi's openai-completions code normalized the server's
// usage: 120 prompt tokens of which 100 cached, 30 completion tokens of
// which 10 reasoning.
func TestRealPiSession(t *testing.T) {
	dir := os.Getenv("YESMAN_USAGE_PI_SESSIONS")
	if dir == "" {
		t.Skip("run by scripts/check-usage-pi.mjs")
	}
	var runs struct {
		First, Second struct{ Start, End int64 }
		MinMs         int64
	}
	if err := json.Unmarshal([]byte(os.Getenv("YESMAN_USAGE_PI_RUNS")), &runs); err != nil {
		t.Fatal(err)
	}
	records, st := scan(t, &Dir{}, dir)
	if st.Files != 1 || st.Unreadable != 0 || len(records) != 2 {
		t.Fatalf("records = %+v, stats = %+v", records, st)
	}
	want := Tokens{Input: 20, Output: 30, CacheRead: 100, CacheWrite: 0, Total: 150}
	for i, c := range []struct {
		model, effort string
		start, end    int64
	}{{"m1", "high", runs.First.Start, runs.First.End}, {"m2", "low", runs.Second.Start, runs.Second.End}} {
		r := records[i]
		if r.Kind != KindResponse || r.Provider != "usage-test" || r.Model != c.model || r.Effort != c.effort || r.Stop != "stop" {
			t.Errorf("response %d = %+v, want %s at %s", i, r, c.model, c.effort)
		}
		if r.Tokens != want {
			t.Errorf("response %d tokens = %+v, want %+v", i, r.Tokens, want)
		}
		// The request started within the prompt's window, and took at least
		// the server's delays, and no longer than the whole prompt.
		at := r.At.UnixMilli()
		if at < c.start || at > c.end {
			t.Errorf("response %d started at %d, outside %d..%d", i, at, c.start, c.end)
		}
		if r.DurationMs < runs.MinMs || r.DurationMs > c.end-c.start || at+r.DurationMs > c.end {
			t.Errorf("response %d took %d ms, want at least %d and within %d..%d", i, r.DurationMs, runs.MinMs, c.start, c.end)
		}
		t.Logf("response %d: %s at %s, %+v, %d ms (%.1f tok/s)", i, r.Model, r.Effort, r.Tokens, r.DurationMs, float64(r.Output)/(float64(r.DurationMs)/1000))
	}
	s := Summarize([]Workspace{{Name: "fixture", Workers: []yesmen.Worker{{Name: "w1", StartedAt: time.UnixMilli(runs.First.Start - 1000)}},
		Agents: map[string][]Record{"w1": records}}}, Query{Workers: true})
	if len(s.Rows) != 2 || s.Total.Workers != 1 || s.Overlap != 1 || s.Total.Tokens != (Tokens{Input: 40, Output: 60, CacheRead: 200, Total: 300}) {
		t.Fatalf("summary = %+v", s)
	}
	for _, r := range s.Rows {
		if r.Speed == nil || *r.Speed != float64(r.Output)/(float64(r.TimedMs)/1000) || r.Timed != 1 {
			t.Errorf("row %s: speed %v, timed %d", r.Model, r.Speed, r.Timed)
		}
	}
}
