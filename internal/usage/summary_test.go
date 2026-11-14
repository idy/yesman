package usage

import (
	"slices"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// rec is a record as scan reads it from an entry pi wrote when the response
// ended: at its start plus its duration, if recorded.
func rec(kind, provider, model, effort string, at time.Time, durationMs int64, stop string, input, output, read, write int64) Record {
	return Record{Kind: kind, Provider: provider, Model: model, Effort: effort, At: at, DurationMs: durationMs, Stop: stop,
		Ended:  ended(at, at.Add(time.Duration(max(durationMs, 0))*time.Millisecond), durationMs),
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
// it used, when only one worker can have held the owner's name. Open,
// reopened and dropped tasks do not count.
func TestSummarizeAttributesDoneTasks(t *testing.T) {
	switched := t0.Add(time.Hour)
	again := t0.Add(5 * time.Hour)
	recorded := func(tk yesmen.Task, at time.Time, owner string) yesmen.Task {
		tk.DoneAt, tk.DoneOwner = at, owner
		return tk
	}
	created := func(tk yesmen.Task, at time.Time) yesmen.Task {
		tk.CreatedAt = at
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
			task("t11", yesmen.TaskDone, "re", again.Add(2*time.Hour)),                                             // either "re": unknown
			created(task("t12", yesmen.TaskDone, "re", again.Add(2*time.Hour)), again.Add(time.Hour)),              // created after the new "re": b low
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
	want := map[string]int{"a:high": 2, "b:xhigh": 1, "b:low": 1}
	if len(got) != len(want) {
		t.Fatalf("tasks by row = %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("tasks of %s = %d, want %d (all %v)", k, got[k], n, got)
		}
	}
	tc := s.Tasks
	if tc.Attributed != 4 || tc.SeveralModels != 1 || tc.Unowned != 2 || tc.UnknownOwner != 1 || tc.OtherRole != 1 || tc.NoResponses != 2 || tc.ReusedName != 1 || tc.Recorded != 4 {
		t.Errorf("task counts = %+v", tc)
	}
	if tc.Done != unattributedSum(tc)+tc.Attributed || tc.Done != 12 {
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

func unattributedSum(tc TaskCounts) int {
	return tc.SeveralModels + tc.NoResponses + tc.Unowned + tc.UnknownOwner + tc.OtherRole + tc.ReusedName + tc.UnknownTiming
}

func tasksByRow(s Summary) map[string]int {
	got := map[string]int{}
	for _, r := range s.Rows {
		if r.Tasks > 0 {
			got[r.Model+":"+r.Effort] = r.Tasks
		}
	}
	return got
}

// A task counts for the owner's latest response that had ended when it was
// done: one that started before but ended after does not take it, however
// its entry arrives, and the result is the same read fresh, in any order
// and in any period.
func TestSummarizeCreditsResponsesEndedByCompletion(t *testing.T) {
	done := t0.Add(time.Hour)
	mk := func(model, effort string, start, written time.Time, ms int64) Record {
		r := response(model, effort, start, ms, 1)
		r.Ended = ended(start, written, ms)
		return r
	}
	cases := []struct {
		name    string
		records []Record
		want    string // "model:effort", or a reason
	}{
		{"ended after completion", []Record{
			mk("old", "high", done.Add(-time.Hour), done.Add(-59*time.Minute), 1000),
			mk("new", "low", done.Add(-time.Second), done.Add(time.Second), 2000),
		}, "old:high"},
		{"same model, another effort", []Record{
			mk("m", "high", done.Add(-time.Hour), done.Add(-59*time.Minute), 1000),
			mk("m", "low", done.Add(-time.Second), done.Add(time.Second), 2000),
		}, "m:high"},
		{"written late, no duration", []Record{
			mk("old", "high", done.Add(-time.Hour), done.Add(-59*time.Minute), -1),
			mk("new", "low", done.Add(-time.Minute), done.Add(time.Second), -1),
		}, "old:high"},
		{"ended exactly at completion", []Record{
			mk("old", "high", done.Add(-time.Hour), done.Add(-59*time.Minute), 1000),
			mk("new", "low", done.Add(-time.Second), done, 1000),
		}, "new:low"},
		{"a long duration ends later than written", []Record{
			mk("old", "high", done.Add(-time.Hour), done.Add(-59*time.Minute), 1000),
			mk("new", "low", done.Add(-time.Minute), done.Add(-30*time.Second), 120_000),
		}, "old:high"},
		{"end not recorded, started after the last that ended", []Record{
			mk("old", "high", done.Add(-time.Hour), done.Add(-59*time.Minute), 1000),
			mk("new", "low", done.Add(-time.Minute), time.Time{}, -1),
		}, "unknownTiming"},
		{"end not recorded, started before the last that ended", []Record{
			mk("first", "low", done.Add(-2*time.Hour), time.Time{}, -1),
			mk("old", "high", done.Add(-time.Hour), done.Add(-59*time.Minute), 1000),
		}, "old:high"},
		{"end not recorded, started after completion", []Record{
			mk("old", "high", done.Add(-time.Hour), done.Add(-59*time.Minute), 1000),
			mk("new", "low", done.Add(time.Minute), time.Time{}, -1),
		}, "old:high"},
		{"only a response whose end is not recorded", []Record{
			mk("new", "low", done.Add(-time.Minute), time.Time{}, -1),
		}, "unknownTiming"},
		{"none ended by completion", []Record{
			mk("new", "low", done.Add(-time.Second), done.Add(time.Second), 2000),
		}, "noResponses"},
		{"a written time before the start is not believed", []Record{
			mk("old", "high", done.Add(-time.Hour), done.Add(-59*time.Minute), 1000),
			mk("new", "low", done.Add(-time.Second), done.Add(-time.Hour), -1),
		}, "unknownTiming"},
		{"an overlong duration is not believed", []Record{
			mk("old", "high", done.Add(-time.Hour), done.Add(-59*time.Minute), 1000),
			mk("new", "low", done.Add(-time.Second), time.Time{}, (25 * time.Hour).Milliseconds()),
		}, "unknownTiming"},
	}
	for _, c := range cases {
		for _, order := range []string{"forward", "reversed"} {
			records := slices.Clone(c.records)
			if order == "reversed" {
				slices.Reverse(records)
			}
			tk := task("t1", yesmen.TaskDone, "w", done.Add(time.Hour))
			tk.DoneAt, tk.DoneOwner = done, "w"
			w := Workspace{Name: "y", Workers: []yesmen.Worker{{Name: "w", StartedAt: t0.Add(-time.Hour)}},
				Agents: map[string][]Record{"w": records}, Board: yesmen.Board{Tasks: []yesmen.Task{tk}}}
			for _, since := range []time.Time{{}, done.Add(-time.Minute)} {
				s := Summarize([]Workspace{w}, Query{Workers: true, Since: since})
				got := ""
				for k := range tasksByRow(s) {
					got = k
				}
				switch {
				case s.Tasks.UnknownTiming == 1:
					got = "unknownTiming"
				case s.Tasks.NoResponses == 1:
					got = "noResponses"
				}
				if got != c.want || s.Tasks.Done != 1 || unattributedSum(s.Tasks)+s.Tasks.Attributed != 1 {
					t.Errorf("%s (%s, since %v): got %q, want %q; tasks %+v", c.name, order, since, got, c.want, s.Tasks)
				}
			}
		}
	}
}

func TestEnded(t *testing.T) {
	start := t0
	for _, c := range []struct {
		written time.Time
		ms      int64
		want    time.Time
	}{
		{start.Add(2 * time.Second), 1000, start.Add(2 * time.Second)},
		{start.Add(time.Second), 3000, start.Add(3 * time.Second)},
		{time.Time{}, 1000, start.Add(time.Second)},
		{start.Add(time.Second), -1, start.Add(time.Second)},
		{start.Add(-time.Second), -1, time.Time{}},
		{start.Add(-time.Second), 500, start.Add(500 * time.Millisecond)},
		{time.Time{}, -1, time.Time{}},
		{time.Time{}, -5, time.Time{}},
		{time.Time{}, 1 << 53, time.Time{}},
		{time.Time{}, (24 * time.Hour).Milliseconds(), start.Add(24 * time.Hour)},
	} {
		if got := ended(start, c.written, c.ms); !got.Equal(c.want) {
			t.Errorf("ended(start, %v, %d) = %v, want %v", c.written, c.ms, got, c.want)
		}
	}
}

// A task done before completion was recorded counts only when one worker
// alone can have held its owner's name since the task was created; edits
// after it was done never move it. Removing and starting the name again any
// number of times, and the period asked for, keep that.
func TestSummarizeLegacyTasksIgnoreEdits(t *testing.T) {
	s1, s2, s3 := t0.Add(time.Hour), t0.Add(2*time.Hour), t0.Add(3*time.Hour)
	starts := []yesmen.WorkerStart{{Name: "w", StartedAt: s1}, {Name: "w", StartedAt: s2}, {Name: "w", StartedAt: s3}}
	records := map[string][]Record{
		"w":    {response("a", "high", s1.Add(time.Minute), 1000, 1), response("b", "low", s2.Add(time.Minute), 1000, 1), response("c", "low", s3.Add(time.Minute), 1000, 1)},
		"solo": {response("s", "xhigh", t0.Add(time.Minute), 1000, 1)},
	}
	legacy := func(id, owner string, created time.Time) yesmen.Task {
		return yesmen.Task{ID: id, Status: yesmen.TaskDone, Owner: owner, CreatedAt: created, UpdatedAt: created.Add(time.Minute)}
	}
	tasks := []yesmen.Task{
		legacy("t1", "w", s1.Add(time.Minute)),    // the first, second or third w
		legacy("t2", "w", s2.Add(time.Minute)),    // the second or third
		legacy("t3", "w", s3.Add(time.Minute)),    // only the third: c low
		legacy("t4", "solo", t0.Add(time.Minute)), // a name used once: s xhigh
	}
	view := func(tasks []yesmen.Task, current []yesmen.Worker, since time.Time) Summary {
		w := Workspace{Name: "y", Workers: current, Starts: starts, Agents: records, Board: yesmen.Board{Tasks: tasks}}
		return Summarize([]Workspace{w}, Query{Workers: true, Since: since})
	}
	check := func(label string, s Summary) {
		t.Helper()
		got := tasksByRow(s)
		if len(got) != 2 || got["c:low"] != 1 || got["s:xhigh"] != 1 || s.Tasks.ReusedName != 2 || s.Tasks.Done != 4 {
			t.Errorf("%s: tasks %v, counts %+v", label, got, s.Tasks)
		}
	}
	solo := []yesmen.Worker{{Name: "solo", StartedAt: t0}}
	check("as done", view(tasks, solo, time.Time{}))
	// A note edit later, at any time, changes only UpdatedAt.
	for _, at := range []time.Time{s1, s2.Add(time.Hour), s3.Add(time.Hour), s3.Add(48 * time.Hour)} {
		edited := slices.Clone(tasks)
		for i := range edited {
			edited[i].UpdatedAt, edited[i].Note = at, "edited"
		}
		check("edited at "+at.Format(time.Kitchen), view(edited, solo, time.Time{}))
	}
	// The current w, listed or removed, does not change who can have done it.
	check("w listed", view(tasks, append(slices.Clone(solo), yesmen.Worker{Name: "w", StartedAt: s3}), time.Time{}))
	// A period cannot place a task done at a time not recorded.
	if s := view(tasks, solo, s1); s.Tasks.TimeUnknown != 4 || s.Tasks.Done != 0 || len(tasksByRow(s)) != 0 {
		t.Errorf("in a period: %+v", s.Tasks)
	}
	// Board order does not matter; a task recorded done before the first
	// known start shows an earlier w existed, which may also have done t3's
	// sibling created then.
	early := yesmen.Task{ID: "t5", Status: yesmen.TaskDone, Owner: "w", DoneOwner: "w", CreatedAt: t0, UpdatedAt: t0, DoneAt: t0.Add(time.Minute)}
	before := legacy("t6", "w", t0) // the earlier w or any later one
	for _, order := range [][]yesmen.Task{append(slices.Clone(tasks), early, before), append([]yesmen.Task{before, early}, tasks...)} {
		s := view(order, solo, time.Time{})
		if s.Tasks.ReusedName != 3 || s.Tasks.NoResponses != 1 || tasksByRow(s)["c:low"] != 1 {
			t.Errorf("with an earlier w: %+v %v", s.Tasks, tasksByRow(s))
		}
	}
	// Reassigning a legacy task while done changes whose it is: the owner is
	// all a legacy task records.
	moved := slices.Clone(tasks)
	moved[3].Owner = "w"
	if s := view(moved, solo, time.Time{}); s.Tasks.ReusedName != 3 || tasksByRow(s)["s:xhigh"] != 0 {
		t.Errorf("reassigned: %+v %v", s.Tasks, tasksByRow(s))
	}
}

func TestSummarizeEmpty(t *testing.T) {
	s := Summarize(nil, all)
	if s.Rows == nil || len(s.Rows) != 0 || s.Coverage.Errors == nil || s.Total.Speed != nil {
		t.Fatalf("empty summary = %+v", s)
	}
}

// Recorded starts keep each start of a name apart, after its workers are
// removed and the name started again any number of times; removing the
// current worker does not change who did what.
func TestSummarizeKeepsStartsAfterRemoval(t *testing.T) {
	s1, s2, s3 := t0.Add(time.Hour), t0.Add(2*time.Hour), t0.Add(3*time.Hour)
	starts := []yesmen.WorkerStart{{Name: "w", StartedAt: s1}, {Name: "w", StartedAt: s2}, {Name: "w", StartedAt: s3}}
	records := []Record{
		response("old", "high", t0.Add(time.Minute), 1000, 1), // before any recorded start
		response("a", "high", s1.Add(time.Minute), 1000, 1),
		response("b", "high", s2.Add(time.Minute), 1000, 1),
		response("b", "high", s2.Add(2*time.Minute), 1000, 1),
		response("c", "high", s3.Add(time.Minute), 1000, 1),
	}
	doneAt := func(id string, at time.Time) yesmen.Task {
		tk := task(id, yesmen.TaskDone, "w", at)
		tk.DoneAt, tk.DoneOwner = at, "w"
		return tk
	}
	legacy := task("t4", yesmen.TaskDone, "w", s3.Add(time.Hour)) // created before all: any of them
	late := task("t5", yesmen.TaskDone, "w", s3.Add(time.Hour))
	late.CreatedAt = s3.Add(10 * time.Minute) // created after the third start: only it
	board := yesmen.Board{Tasks: []yesmen.Task{
		doneAt("t1", s1.Add(30*time.Minute)), // the first start: a
		doneAt("t2", s2.Add(30*time.Minute)), // the second: b
		doneAt("t3", s3.Add(30*time.Minute)), // the third: c
		legacy, late,
	}}
	for _, current := range [][]yesmen.Worker{{{Name: "w", StartedAt: s3}}, nil} {
		w := Workspace{Name: "y", Workers: current, Starts: starts, Board: board, Agents: map[string][]Record{"w": records}}
		s := Summarize([]Workspace{w}, Query{Workers: true})
		if s.Total.Workers != 4 || s.Overlap != 0 || s.Tasks.ReusedName != 1 || s.Tasks.Attributed != 4 {
			t.Fatalf("with %d listed: total = %+v, overlap %d, tasks %+v", len(current), s.Total, s.Overlap, s.Tasks)
		}
		for model, want := range map[string]Row{"a": {Workers: 1, Tasks: 1, Responses: 1}, "b": {Workers: 1, Tasks: 1, Responses: 2}, "c": {Workers: 1, Tasks: 2, Responses: 1}, "old": {Workers: 1, Responses: 1}} {
			r := rowOf(t, s, "p/"+model, "high")
			if r.Workers != want.Workers || r.Tasks != want.Tasks || r.Responses != want.Responses {
				t.Errorf("with %d listed: %s = %+v", len(current), model, r)
			}
		}
	}
}

// A task done in a period counts its owner among the model's workers there,
// though the owner last used the model before the period.
func TestSummarizeCountsOwnersOfTasksDoneInPeriod(t *testing.T) {
	since := t0.Add(24 * time.Hour)
	w := Workspace{Name: "y", Workers: []yesmen.Worker{{Name: "w1", StartedAt: t0}},
		Agents: map[string][]Record{"w1": {response("m", "high", t0.Add(time.Hour), 1000, 10)}},
		Board:  yesmen.Board{Tasks: []yesmen.Task{{ID: "t1", Status: yesmen.TaskDone, Owner: "w1", UpdatedAt: since.Add(time.Hour), DoneAt: since.Add(time.Hour), DoneOwner: "w1"}}}}
	s := Summarize([]Workspace{w}, Query{Workers: true, Since: since})
	r := rowOf(t, s, "p/m", "high")
	if r.Tasks != 1 || r.Workers != 1 || r.Responses != 0 || r.Total != 0 || s.Total.Workers != 1 {
		t.Fatalf("row = %+v, total %+v", r, s.Total)
	}
}

// Dropping the oldest worker starts, to keep them bounded, never makes a
// task count, nor counts one for another model: a task counts only where
// the worker holding its owner's name then is still known. Usage and
// speed do not change, and workers can only merge.
func TestSummarizeDroppedStartsNeverCredit(t *testing.T) {
	m := func(d time.Duration) time.Time { return t0.Add(d) }
	starts := []yesmen.WorkerStart{
		{Name: "solo", StartedAt: m(10 * time.Minute)},
		{Name: "cur", StartedAt: m(30 * time.Minute)},
		{Name: "w", StartedAt: m(time.Hour)}, // no response
		{Name: "v", StartedAt: m(time.Hour + time.Second)},
		{Name: "w", StartedAt: m(2 * time.Hour)},             // no response
		{Name: "v", StartedAt: m(2*time.Hour + time.Second)}, // no response
		{Name: "w", StartedAt: m(5 * time.Hour)},
		{Name: "v", StartedAt: m(5*time.Hour + time.Second)},
		{Name: "fresh", StartedAt: m(6 * time.Hour)},
	}
	current := []yesmen.Worker{{Name: "cur", StartedAt: m(30 * time.Minute)}}
	agents := map[string][]Record{
		"lead":  {response("l", "high", m(time.Minute), 1000, 10)},
		"solo":  {response("s", "xhigh", m(11*time.Minute), 1000, 10)},
		"cur":   {response("m", "x", m(31*time.Minute), 1000, 10), response("m", "x", m(7*time.Hour), 1000, 10)},
		"w":     {response("c", "low", m(5*time.Hour+time.Minute), 1000, 10)},
		"v":     {response("a", "high", m(time.Hour+time.Minute), 1000, 10), response("c", "low", m(5*time.Hour+time.Minute), 1000, 10)},
		"fresh": {response("f", "med", m(6*time.Hour+time.Minute), 1000, 10)},
	}
	legacy := func(id, owner string, created time.Time) yesmen.Task {
		return yesmen.Task{ID: id, Status: yesmen.TaskDone, Owner: owner, CreatedAt: created, UpdatedAt: created}
	}
	recorded := func(id, owner string, done time.Time) yesmen.Task {
		return yesmen.Task{ID: id, Status: yesmen.TaskDone, Owner: owner, DoneOwner: owner, CreatedAt: done.Add(-time.Minute), UpdatedAt: done, DoneAt: done}
	}
	board := yesmen.Board{Tasks: []yesmen.Task{
		legacy("t1", "w", m(time.Hour+time.Minute)),           // any w: unknown
		recorded("t2", "v", m(2*time.Hour+10*time.Minute)),    // the second v, which never answered
		recorded("t3", "fresh", m(6*time.Hour+5*time.Minute)), // f med
		recorded("t4", "cur", m(7*time.Hour+5*time.Minute)),   // m x
		legacy("t5", "cur", m(40*time.Minute)),                // m x
		recorded("t6", "lead", m(2*time.Minute)),              // l high
		legacy("t7", "solo", m(12*time.Minute)),               // s xhigh
	}}
	space := func(name string, k int) Workspace {
		w := Workspace{Name: name, HiredAt: t0, Workers: current, Starts: starts[k:], Agents: agents, Board: board}
		if k > 0 {
			w.StartsCut = starts[k-1].StartedAt
		}
		return w
	}
	base := Summarize([]Workspace{space("y", 0)}, all)
	if got := tasksByRow(base); len(got) != 4 || got["f:med"] != 1 || got["m:x"] != 2 || got["l:high"] != 1 || got["s:xhigh"] != 1 || got["low:"] != 0 ||
		base.Tasks.ReusedName != 1 || base.Tasks.NoResponses != 1 || len(base.Coverage.StartsCut) != 0 {
		t.Fatalf("complete: %v %+v", got, base.Tasks)
	}
	baseRows := map[string]Row{}
	for _, r := range base.Rows {
		baseRows[r.Model+":"+r.Effort] = r
	}
	for k := 1; k <= len(starts); k++ {
		s := Summarize([]Workspace{space("y", k)}, all)
		label := "dropped through " + starts[k-1].Name
		if s.Tasks.Done != base.Tasks.Done || s.Tasks.Attributed > base.Tasks.Attributed || s.Tasks.Attributed+unattributedSum(s.Tasks) != s.Tasks.Done {
			t.Errorf("%s: %+v, complete %+v", label, s.Tasks, base.Tasks)
		}
		if s.Total.Tokens != base.Total.Tokens || s.Total.Responses != base.Total.Responses || s.Total.Workers > base.Total.Workers || len(s.Rows) != len(base.Rows) {
			t.Errorf("%s: total %+v, complete %+v", label, s.Total, base.Total)
		}
		for _, r := range s.Rows {
			b := baseRows[r.Model+":"+r.Effort]
			if r.Tasks > b.Tasks || r.Workers > b.Workers || r.Tokens != b.Tokens || r.TimedMs != b.TimedMs || r.TimedOutput != b.TimedOutput || *r.Speed != *b.Speed {
				t.Errorf("%s: row %+v, complete %+v", label, r, b)
			}
		}
		if len(s.Coverage.StartsCut) != 1 || !s.Coverage.StartsCut[0].Through.Equal(starts[k-1].StartedAt) {
			t.Errorf("%s: coverage %+v", label, s.Coverage.StartsCut)
		}
	}

	// With w's and v's first two starts dropped, as when the oldest are:
	// the w of t1 and the v of t2 are no longer known, nor is solo; cur is
	// still here, fresh and the lead are not affected.
	s := Summarize([]Workspace{space("y", 6)}, all)
	if got := tasksByRow(s); len(got) != 3 || got["f:med"] != 1 || got["m:x"] != 2 || got["l:high"] != 1 || s.Tasks.ReusedName != 3 || s.Tasks.NoResponses != 0 {
		t.Errorf("dropped: %v %+v", got, s.Tasks)
	}
	// Another yesman with the same names keeps all its starts.
	both := Summarize([]Workspace{space("y", 6), space("z", 0)}, all)
	if both.Tasks.Attributed != s.Tasks.Attributed+base.Tasks.Attributed || both.Tasks.ReusedName != s.Tasks.ReusedName+base.Tasks.ReusedName ||
		len(both.Coverage.StartsCut) != 1 || both.Coverage.StartsCut[0].Yesman != "y" {
		t.Errorf("with another yesman: %+v %+v", both.Tasks, both.Coverage.StartsCut)
	}
	// Not knowing any start leaves only what the current workers tell.
	none := space("y", len(starts))
	none.StartsCut = m(48 * time.Hour)
	if got := tasksByRow(Summarize([]Workspace{none}, all)); len(got) != 2 || got["m:x"] != 2 || got["l:high"] != 1 {
		t.Errorf("no starts known: %v", got)
	}
}
