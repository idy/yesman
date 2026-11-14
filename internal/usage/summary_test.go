package usage

import (
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func rec(kind, provider, model, effort string, at time.Time, durationMs int64, stop string, input, output, read, write int64) Record {
	return Record{Kind: kind, Provider: provider, Model: model, Effort: effort, At: at, DurationMs: durationMs, Stop: stop,
		Tokens: Tokens{Input: input, Output: output, CacheRead: read, CacheWrite: write, Total: input + output + read + write}}
}

func response(model, effort string, at time.Time, ms, output int64) Record {
	return rec(KindResponse, "p", model, effort, at, ms, "toolUse", 10, output, 100, 1)
}

func rowOf(t *testing.T, s Summary, model, effort string) Row {
	t.Helper()
	for _, r := range s.Rows {
		if r.Provider+"/"+r.Model == model && r.Effort == effort || model == "" && r.Model == "" && r.Effort == effort {
			return r
		}
	}
	t.Fatalf("no row %s at %q in %+v", model, effort, s.Rows)
	return Row{}
}

var all = Query{Workers: true, Leads: true}

// Each response counts under its own model and effort, so a switch mid
// session splits the agent's usage; usage without an effort or a model is
// kept apart rather than given the current one.
func TestSummarizeGroupsByProviderModelAndEffort(t *testing.T) {
	w := Workspace{Name: "y", HiredAt: t0, Workers: []yesmen.Worker{{Name: "w1", StartedAt: t0}},
		Agents: map[string][]Record{"w1": {
			response("m", "high", t0.Add(1*time.Minute), 1000, 10),
			response("m", "xhigh", t0.Add(2*time.Minute), 1000, 20),
			response("m2", "xhigh", t0.Add(3*time.Minute), 1000, 30),
			{Kind: KindResponse, Provider: "q", Model: "m", Effort: "xhigh", At: t0.Add(4 * time.Minute), DurationMs: -1, Stop: "stop", Tokens: Tokens{Output: 5, Total: 5}},
			response("m", "", t0.Add(5*time.Minute), 1000, 40),
			rec(KindEntry, "p", "m", "", t0.Add(6*time.Minute), -1, "", 0, 0, 500, 0),
			rec(KindSummary, "", "", "", t0.Add(7*time.Minute), -1, "", 50, 20, 0, 0),
		}}}
	s := Summarize([]Workspace{w}, all)
	if len(s.Rows) != 6 {
		t.Fatalf("rows = %+v", s.Rows)
	}
	if r := rowOf(t, s, "p/m", "high"); r.Responses != 1 || r.Output != 10 || r.Workers != 1 {
		t.Errorf("m high = %+v", r)
	}
	if r := rowOf(t, s, "p/m", "xhigh"); r.Responses != 1 || r.Output != 20 {
		t.Errorf("m xhigh = %+v", r)
	}
	if r := rowOf(t, s, "q/m", "xhigh"); r.Responses != 1 || r.Output != 5 {
		t.Errorf("another provider's m = %+v", r)
	}
	if r := rowOf(t, s, "p/m", ""); r.Responses != 1 || r.Other != 1 || r.CacheRead != 600 {
		t.Errorf("m without effort = %+v", r)
	}
	if r := rowOf(t, s, "", ""); r.Responses != 0 || r.Other != 1 || r.Total != 70 || r.Workers != 1 {
		t.Errorf("model not recorded = %+v", r)
	}
	if s.Coverage.NoEffort != 1 || s.Coverage.NoModel != 1 || s.Coverage.Records != 7 {
		t.Errorf("coverage = %+v", s.Coverage)
	}
	// One worker in several rows counts once in the total.
	if s.Total.Workers != 1 || s.Overlap != 1 {
		t.Errorf("total workers = %d, overlap = %d", s.Total.Workers, s.Overlap)
	}
}

// Totals add pi's totals, which already hold cache reads and writes, and
// output, which already holds reasoning.
func TestSummarizeAddsTokensOnce(t *testing.T) {
	w := Workspace{Name: "y", Workers: []yesmen.Worker{{Name: "w1", StartedAt: t0}}, Agents: map[string][]Record{"w1": {
		rec(KindResponse, "p", "m", "high", t0, 1000, "stop", 3, 4, 1_000_000, 2),
		rec(KindResponse, "p", "m", "high", t0, 1000, "error", 5, 0, 0, 0),
	}}}
	s := Summarize([]Workspace{w}, all)
	r := rowOf(t, s, "p/m", "high")
	want := Tokens{Input: 8, Output: 4, CacheRead: 1_000_000, CacheWrite: 2, Total: 1_000_014}
	if r.Tokens != want || s.Total.Tokens != want {
		t.Fatalf("tokens = %+v, total %+v, want %+v", r.Tokens, s.Total.Tokens, want)
	}
	if r.Responses != 2 || r.Failed != 1 {
		t.Errorf("responses = %d, failed = %d", r.Responses, r.Failed)
	}
}

// Speed weighs each timed response by its output: the sum of output over
// the sum of time, not the mean of each response's speed. Failed, untimed
// and zero-length responses are left out of it.
func TestSummarizeWeighsSpeed(t *testing.T) {
	w := Workspace{Name: "y", Workers: []yesmen.Worker{{Name: "w1", StartedAt: t0}}, Agents: map[string][]Record{"w1": {
		response("m", "high", t0, 1000, 100), // 100 tok/s
		response("m", "high", t0, 9000, 100), // 11.1 tok/s
		response("m", "high", t0, -1, 5000),  // untimed
		response("m", "high", t0, 0, 5000),   // a zero duration is not a time
		rec(KindResponse, "p", "m", "high", t0, 60_000, "aborted", 0, 3, 0, 0),
		rec(KindResponse, "p", "m", "high", t0, 60_000, "error", 0, 0, 0, 0),
		response("slow", "high", t0, -1, 10), // nothing timed
	}}}
	s := Summarize([]Workspace{w}, all)
	r := rowOf(t, s, "p/m", "high")
	if r.Speed == nil || *r.Speed != 20 {
		t.Fatalf("speed = %v, want 200/10s = 20", r.Speed)
	}
	if r.Timed != 2 || r.Untimed != 2 || r.Failed != 2 || r.TimedOutput != 200 || r.TimedMs != 10_000 {
		t.Errorf("timing = %+v", r)
	}
	if r := rowOf(t, s, "p/slow", "high"); r.Speed != nil || r.Untimed != 1 {
		t.Errorf("untimed row = %+v", r)
	}
	if s.Total.Speed != nil {
		t.Error("the total combines speeds of different models")
	}
}

// Workers are instances: many responses of one worker count once; a name
// hired again is another worker, whose session continues the old one; a
// session left by a removed worker is a worker of its own. Leads count
// apart and only when asked for.
func TestSummarizeCountsWorkerInstances(t *testing.T) {
	again := t0.Add(time.Hour)
	w := Workspace{Name: "y", HiredAt: t0,
		Workers: []yesmen.Worker{{Name: "w1", StartedAt: t0}, {Name: "w2", StartedAt: again}},
		Agents: map[string][]Record{
			"lead": {response("m", "high", t0, 1000, 1)},
			"w1":   {response("m", "high", t0.Add(time.Minute), 1000, 1), response("m", "high", t0.Add(2*time.Minute), 1000, 1), response("m", "high", t0.Add(30*time.Minute), 1000, 1)},
			"w2":   {response("m", "high", t0.Add(time.Minute), 1000, 1), response("m", "high", again.Add(time.Minute), 1000, 1)},
			"gone": {response("m", "high", t0.Add(time.Minute), 1000, 1)},
		}}
	s := Summarize([]Workspace{w}, Query{Workers: true})
	r := rowOf(t, s, "p/m", "high")
	// w1, w2 before its hire, w2 now, and the removed worker.
	if r.Workers != 4 || r.Leads != 0 || r.Responses != 6 {
		t.Fatalf("workers row = %+v", r)
	}
	if s.Total.Workers != 4 || s.Total.Leads != 0 || s.Overlap != 0 {
		t.Errorf("total = %+v", s.Total)
	}
	s = Summarize([]Workspace{w}, Query{Leads: true})
	if r := rowOf(t, s, "p/m", "high"); r.Workers != 0 || r.Leads != 1 || r.Responses != 1 {
		t.Errorf("leads row = %+v", r)
	}
	s = Summarize([]Workspace{w, {Name: "z", HiredAt: t0, Agents: map[string][]Record{"lead": {response("m", "high", t0, 1000, 1)}}}}, all)
	if r := rowOf(t, s, "p/m", "high"); r.Workers != 4 || r.Leads != 2 || s.Total.Leads != 2 || s.Total.Workers != 4 {
		t.Errorf("all agents row = %+v, total = %+v", r, s.Total)
	}
}

func task(id, status, owner string, updated time.Time) yesmen.Task {
	return yesmen.Task{ID: id, Title: id, Status: status, Owner: owner, CreatedAt: t0, UpdatedAt: updated}
}

// Done tasks count for the model their owner used: the one it was using
// when the task was done, when the board recorded that, else the only one
// it used. Open, reopened and dropped tasks do not count.
func TestSummarizeAttributesDoneTasks(t *testing.T) {
	switched := t0.Add(time.Hour)
	again := t0.Add(5 * time.Hour)
	recorded := func(tk yesmen.Task, at time.Time, owner string) yesmen.Task {
		tk.DoneAt, tk.DoneOwner = at, owner
		return tk
	}
	w := Workspace{Name: "y", HiredAt: t0,
		Workers: []yesmen.Worker{{Name: "solo", StartedAt: t0}, {Name: "switch", StartedAt: t0}, {Name: "re", StartedAt: again}, {Name: "idle", StartedAt: t0}},
		Agents: map[string][]Record{
			"lead":   {response("m", "high", t0, 1000, 1)},
			"solo":   {response("a", "high", t0.Add(time.Minute), 1000, 1), response("a", "high", t0.Add(2*time.Minute), 1000, 1)},
			"switch": {response("a", "high", t0.Add(time.Minute), 1000, 1), response("b", "xhigh", switched, 1000, 1)},
			"re":     {response("a", "low", t0.Add(time.Minute), 1000, 1), response("b", "low", again.Add(time.Minute), 1000, 1)},
		},
		Board: yesmen.Board{Tasks: []yesmen.Task{
			task("t1", yesmen.TaskDone, "solo", t0.Add(3*time.Hour)),                                               // a high
			task("t2", yesmen.TaskDone, "switch", t0.Add(3*time.Hour)),                                             // several models, no time
			recorded(task("t3", yesmen.TaskDone, "switch", t0.Add(3*time.Hour)), t0.Add(30*time.Minute), "switch"), // a high, before the switch
			recorded(task("t4", yesmen.TaskDone, "lead", t0.Add(4*time.Hour)), t0.Add(2*time.Hour), "switch"),      // b xhigh: owner when done
			recorded(task("t5", yesmen.TaskDone, "switch", t0.Add(4*time.Hour)), t0.Add(2*time.Hour), ""),          // unowned when done
			task("t6", yesmen.TaskDoing, "solo", t0),                                                               // open
			task("t7", yesmen.TaskDropped, "solo", t0),                                                             // dropped
			task("t8", yesmen.TaskDone, "", t0),                                                                    // unowned
			task("t9", yesmen.TaskDone, "nobody", t0),                                                              // unknown owner
			task("t10", yesmen.TaskDone, "lead", t0),                                                               // a lead's
			task("t11", yesmen.TaskDone, "re", t0.Add(2*time.Hour)),                                                // the earlier "re": a low
			task("t12", yesmen.TaskDone, "re", again.Add(2*time.Hour)),                                             // the current "re": b low only
			task("t13", yesmen.TaskDone, "idle", t0),                                                               // no responses
			recorded(task("t14", yesmen.TaskDone, "solo", t0.Add(3*time.Hour)), t0.Add(30*time.Second), "solo"),    // before its first response
		}},
	}
	s := Summarize([]Workspace{w}, Query{Workers: true})
	got := map[string]int{}
	for _, r := range s.Rows {
		if r.Tasks > 0 {
			got[r.Model+":"+r.Effort] = r.Tasks
		}
	}
	want := map[string]int{"a:high": 2, "b:xhigh": 1, "a:low": 1, "b:low": 1}
	if len(got) != len(want) {
		t.Fatalf("tasks by row = %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("tasks of %s = %d, want %d (all %v)", k, got[k], n, got)
		}
	}
	tc := s.Tasks
	if tc.Attributed != 5 || tc.SeveralModels != 1 || tc.Unowned != 2 || tc.UnknownOwner != 1 || tc.OtherRole != 1 || tc.NoResponses != 2 || tc.Recorded != 4 {
		t.Errorf("task counts = %+v", tc)
	}
	if tc.Done != tc.Attributed+tc.SeveralModels+tc.Unowned+tc.UnknownOwner+tc.OtherRole+tc.NoResponses || tc.Done != 12 {
		t.Errorf("done = %d does not add up: %+v", tc.Done, tc)
	}
	if s.Total.Tasks != tc.Attributed {
		t.Errorf("total tasks = %d, attributed %d", s.Total.Tasks, tc.Attributed)
	}

	// With leads too, the lead's task counts for the lead's model.
	s = Summarize([]Workspace{w}, all)
	if r := rowOf(t, s, "p/m", "high"); r.Tasks != 1 || s.Tasks.OtherRole != 0 {
		t.Errorf("lead task row = %+v, counts %+v", r, s.Tasks)
	}
}

// A period counts only what happened in it. Tasks done before completion
// times were recorded cannot be placed in one, and are counted apart.
func TestSummarizeLimitsToPeriod(t *testing.T) {
	since := t0.Add(24 * time.Hour)
	w := Workspace{Name: "y", HiredAt: t0, Workers: []yesmen.Worker{{Name: "w1", StartedAt: t0}},
		Agents: map[string][]Record{"w1": {response("m", "high", t0, 1000, 10), response("m", "high", since.Add(time.Hour), 1000, 20)}},
		Board: yesmen.Board{Tasks: []yesmen.Task{
			task("t1", yesmen.TaskDone, "w1", since.Add(2*time.Hour)),
			{ID: "t2", Status: yesmen.TaskDone, Owner: "w1", UpdatedAt: since.Add(time.Hour), DoneAt: since.Add(time.Hour), DoneOwner: "w1"},
			{ID: "t3", Status: yesmen.TaskDone, Owner: "w1", UpdatedAt: since.Add(time.Hour), DoneAt: t0.Add(time.Hour), DoneOwner: "w1"},
		}}}
	s := Summarize([]Workspace{w}, Query{Workers: true, Since: since})
	r := rowOf(t, s, "p/m", "high")
	if r.Responses != 1 || r.Output != 20 || r.Tasks != 1 || r.Workers != 1 {
		t.Fatalf("row = %+v", r)
	}
	if s.Tasks.TimeUnknown != 1 || s.Tasks.Done != 1 {
		t.Errorf("tasks = %+v", s.Tasks)
	}
	if !s.Coverage.From.Equal(since.Add(time.Hour)) || !s.Coverage.To.Equal(since.Add(time.Hour)) {
		t.Errorf("coverage = %v..%v", s.Coverage.From, s.Coverage.To)
	}
	// Without a period every done task counts.
	if s := Summarize([]Workspace{w}, Query{Workers: true}); s.Tasks.Done != 3 || s.Tasks.TimeUnknown != 0 {
		t.Errorf("all-time tasks = %+v", s.Tasks)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	s := Summarize(nil, all)
	if s.Rows == nil || len(s.Rows) != 0 || s.Coverage.Errors == nil || s.Total.Speed != nil {
		t.Fatalf("empty summary = %+v", s)
	}
}
