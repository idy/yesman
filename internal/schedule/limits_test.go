package schedule

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/idy/yesman/internal/yesmen"
)

// ---------- How large the files may grow ----------

// worstPlan is a plan at its largest as plans.json keeps it: every
// character of its title and instructions one JSON writes as six bytes, and
// every other field at its longest.
func worstPlan(t *testing.T, id string) Plan {
	t.Helper()
	at := time.Date(2026, 11, 16, 8, 59, 0, 123456789, time.UTC)
	p := Plan{
		ID: id, Title: strings.Repeat("<", MaxTitle), Instructions: strings.Repeat("<", MaxInstructions),
		Schedule: Spec{Kind: KindWeekly, Date: "2026-11-16", Time: "09:00", Days: []int{0, 1, 2, 3, 4, 5, 6}, Every: 10080},
		Timezone: strings.Repeat("A", 64), Enabled: true, Revision: 1<<63 - 1,
		ActiveSince: at, CreatedAt: at, UpdatedAt: at, ArchivedAt: at, CreatedBy: "lead",
	}
	// The title and instructions pass as the user would save them.
	in, _, err := PlanInput{Title: p.Title, Instructions: p.Instructions, Schedule: Spec{Kind: KindDaily, Time: "09:00"}, Timezone: "UTC"}.normalize(at)
	if err != nil || in.Title != p.Title || in.Instructions != p.Instructions {
		t.Fatalf("the worst plan is not one the user can save: %v", err)
	}
	return p
}

// No character of a title or instructions takes more than six bytes in
// plans.json, so "<" everywhere is the worst case.
func TestNoCharacterTakesMoreThanSixBytes(t *testing.T) {
	for _, s := range []string{"<", ">", "&", "\u2028", "\u2029", `"`, `\`, "\n", "\t", "\u00a0", "\ufeff", "é", "中", "😀", "a"} {
		data, err := json.Marshal(strings.Repeat(s, 10))
		if err != nil {
			t.Fatal(err)
		}
		if per := (len(data) - 2) / 10; per > 6 {
			t.Errorf("%q takes %d bytes", s, per)
		}
	}
	data, _ := json.Marshal("<")
	if string(data) != `"\u003c"` {
		t.Fatalf("< is written %s", data)
	}
}

// One plan at its largest fits its share of the file, and the most plans a
// file may hold at their largest fit the file; so does a run record.
func TestWorstPlanFitsItsShare(t *testing.T) {
	file := func(n int) plansFile {
		f := plansFile{Version: plansVersion, Next: 999999}
		for i := range n {
			f.Plans = append(f.Plans, worstPlan(t, fmt.Sprintf("s%d", 999999-i)))
		}
		return f
	}
	size := func(f plansFile) int {
		data, err := f.encode()
		if err != nil {
			t.Fatal(err)
		}
		return len(data)
	}
	empty, one := size(file(0)), size(file(1))
	if share := one - empty; share > maxPlanBytes || share < 6*(MaxTitle+MaxInstructions) {
		t.Fatalf("a plan at its largest takes %d bytes, its share is %d", share, maxPlanBytes)
	}
	if all := size(file(maxPlans)); all > maxPlansFile {
		t.Fatalf("%d plans at their largest take %d bytes, the file is read up to %d", maxPlans, all, maxPlansFile)
	}

	at := time.Date(2026, 11, 16, 8, 59, 0, 123456789, time.UTC)
	p := worstPlan(t, "s999999")
	long := "s999999-" + strings.Repeat("a", 80)
	r := record{Run: Run{
		ID: long, Plan: p.ID, Revision: p.Revision, Kind: RunCatchUp, Key: "20261116t0900", At: at, First: at, Last: at,
		Count: 1<<63 - 1, CountCapped: true, Due: at, ClaimedAt: at, UpdatedAt: at, State: StateUnknown,
		Reason: "Not sent: " + strings.Repeat("<", 301), Board: "t999999",
		Outcome: &Outcome{Status: "removed", Note: strings.Repeat("<", 301), At: at, Removed: true},
		Title:   p.Title, Instructions: p.Instructions, Schedule: p.Schedule, Timezone: p.Timezone,
		Agent: strings.Repeat("a", 80), AgentSince: at, From: long, CaughtUp: long, Prev: long, Claimer: strings.Repeat("a", 16),
	}}
	for range maxAttempts {
		r.Attempts = append(r.Attempts, Attempt{ID: strings.Repeat("a", 64), At: at, State: "unconfirmed", Rev: 1<<64 - 1, Error: strings.Repeat("<", 301), Definite: true})
	}
	data, err := encodeRun(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxRunFile {
		t.Fatalf("a run record at its largest takes %d bytes, it is read up to %d", len(data), maxRunFile)
	}
}

// Plans the user saved read back after a restart: as many as may be on at
// once, at their longest, written through Save; then as many as may be
// kept, archived ones too, at their largest.
func TestLargestPlansReadAfterRestart(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	text := strings.Repeat("<", MaxInstructions)
	for i := range MaxActivePlans {
		e.save(PlanInput{Title: fmt.Sprint("Plan ", i), Instructions: text, Schedule: daily("09:00")})
	}
	y, _ := e.store.Get(ws)
	info, err := os.Stat(plansPath(y))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= 1<<20 {
		t.Fatalf("the file is %d bytes; the case is one over 1 MiB", info.Size())
	}
	e.restart("")
	v := e.view()
	if v.Error != "" || len(v.Plans) != MaxActivePlans || v.Plans[0].Instructions != text {
		t.Fatalf("after a restart: error %q, %d plans", v.Error, len(v.Plans))
	}

	// The most a file may hold, every plan at its largest, most of them
	// archived, as many saves over time would leave it.
	f := plansFile{Version: plansVersion}
	for i := 1; i <= maxPlans; i++ {
		p := worstPlan(t, fmt.Sprintf("s%d", i))
		p.Revision = 1
		if i >= MaxActivePlans { // one place left on, for one more
			p.ArchivedAt = p.CreatedAt.Add(time.Duration(i) * time.Second)
			p.Enabled = false
		} else {
			p.ArchivedAt = time.Time{}
			p.Enabled = false
		}
		p.Schedule = Spec{Kind: KindWeekly, Time: "09:00", Days: []int{0, 1, 2, 3, 4, 5, 6}}
		p.Timezone = "America/Argentina/ComodRivadavia"
		f.Plans = append(f.Plans, p)
		f.Next = i
	}
	data, err := f.encode()
	if err != nil {
		t.Fatal(err)
	}
	e.close()
	if err := os.WriteFile(plansPath(y), data, 0o644); err != nil {
		t.Fatal(err)
	}
	e.restart("")
	v = e.view()
	if v.Error != "" || len(v.Plans) != maxPlans {
		t.Fatalf("a full file of %d bytes after a restart: error %q, %d plans", len(data), v.Error, len(v.Plans))
	}
	// One more plan forgets the oldest archived one, and still reads back.
	if _, err := e.svc.Save(ws, PlanInput{Title: strings.Repeat("<", MaxTitle), Instructions: text, Schedule: daily("09:00"), Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	e.restart("")
	v = e.view()
	ids := map[string]bool{}
	for _, p := range v.Plans {
		ids[p.ID] = true
	}
	oldest := fmt.Sprintf("s%d", MaxActivePlans)
	if v.Error != "" || len(v.Plans) != maxPlans || !ids[fmt.Sprintf("s%d", maxPlans+1)] || ids[oldest] {
		t.Fatalf("after one more: error %q, %d plans, new one %v, oldest archived kept %v", v.Error, len(v.Plans), ids[fmt.Sprintf("s%d", maxPlans+1)], ids[oldest])
	}
}

// limitEnv is an env whose plans.json is read and written up to limit
// bytes, so the bound is checked with small files.
func limitEnv(t *testing.T, limit int64) *env {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	e.cfg.plansLimit = limit
	e.reopen(e.cfg)
	return e
}

func (e *env) plansBytes() []byte {
	e.t.Helper()
	y, _ := e.store.Get(ws)
	data, err := os.ReadFile(plansPath(y))
	if err != nil {
		e.t.Fatal(err)
	}
	return data
}

// A change that would make plans.json larger than it is read at is refused
// before anything is written: the file, what the scheduler knows of it,
// the plan's revision and the next plan ID stay as they were. A change that
// keeps it within the bound still saves.
func TestChangeRefusedOverTheFileLimit(t *testing.T) {
	const limit = 8 << 10
	e := limitEnv(t, limit)
	big := strings.Repeat("<", 1000) // 6,000 bytes in the file
	first := e.save(PlanInput{Title: "First", Instructions: big, Schedule: daily("09:00")})
	before := e.plansBytes()

	_, err := e.svc.Save(ws, PlanInput{Title: "Second", Instructions: big, Schedule: daily("10:00"), Timezone: "UTC"})
	if !errors.Is(err, errTasksTooLarge) {
		t.Fatalf("a second large plan: %v", err)
	}
	if !bytes.Equal(e.plansBytes(), before) {
		t.Fatal("plans.json changed")
	}
	if v := e.view(); len(v.Plans) != 1 || v.Plans[0].Revision != first.Revision {
		t.Fatalf("view after the refusal: %+v", v.Plans)
	}
	// An edit that would grow it past the bound is refused too, at the
	// same revision, which stays usable.
	_, err = e.svc.Save(ws, PlanInput{ID: first.ID, Revision: first.Revision, Title: "First", Instructions: big + strings.Repeat("&", 600), Schedule: daily("09:00"), Timezone: "UTC"})
	if !errors.Is(err, errTasksTooLarge) {
		t.Fatalf("growing the plan: %v", err)
	}
	if !bytes.Equal(e.plansBytes(), before) {
		t.Fatal("plans.json changed")
	}
	edited, err := e.svc.Save(ws, PlanInput{ID: first.ID, Revision: first.Revision, Title: "First", Instructions: "Short now", Schedule: daily("09:00"), Timezone: "UTC"})
	if err != nil || edited.Revision != first.Revision+1 {
		t.Fatalf("a smaller edit at the same revision: %+v %v", edited, err)
	}
	// The ID the refused plan would have had was not used up.
	if p := e.save(PlanInput{Title: "Second", Instructions: big, Schedule: daily("10:00")}); p.ID != "s2" {
		t.Fatalf("next plan is %s", p.ID)
	}
	e.restart("")
	if v := e.view(); v.Error != "" || len(v.Plans) != 2 {
		t.Fatalf("after a restart: %q %d", v.Error, len(v.Plans))
	}
}

// Fields this version does not know take room too: they are kept, and a
// change that would push the file past the bound with them is refused,
// leaving them and the file as they were.
func TestUnknownFieldsCountTowardTheLimit(t *testing.T) {
	const limit = 8 << 10
	e := limitEnv(t, limit)
	p := e.save(PlanInput{Schedule: daily("09:00")})
	y, _ := e.store.Get(ws)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(e.plansBytes(), &obj); err != nil {
		t.Fatal(err)
	}
	obj["future"] = json.RawMessage(`{"n":9007199254740993,"text":"` + strings.Repeat("x", 6000) + `"}`)
	data, _ := json.MarshalIndent(obj, "", "  ")
	if err := os.WriteFile(plansPath(y), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if v := e.view(); v.Error != "" || len(v.Plans) != 1 {
		t.Fatalf("with an unknown field: %q", v.Error)
	}
	if _, err := e.svc.Save(ws, PlanInput{Title: "More", Instructions: strings.Repeat("<", 400), Schedule: daily("10:00"), Timezone: "UTC"}); !errors.Is(err, errTasksTooLarge) {
		t.Fatalf("a plan past the bound: %v", err)
	}
	if !bytes.Equal(e.plansBytes(), data) {
		t.Fatal("plans.json changed")
	}
	// A change that fits keeps the unknown field, its large number exact.
	if _, err := e.svc.SetEnabled(ws, p.ID, true, p.Revision); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(e.plansBytes()), `"n": 9007199254740993`) {
		t.Fatal("the unknown field was not kept")
	}
}

// A file at the bound reads; one byte over is refused and left as it is,
// and nothing of it runs or is saved over it.
func TestFileOverTheLimitIsLeftUnchanged(t *testing.T) {
	const limit = 8 << 10
	e := limitEnv(t, limit)
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	y, _ := e.store.Get(ws)
	pad := func(size int) []byte {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(e.plansBytes(), &obj); err != nil {
			t.Fatal(err)
		}
		obj["future"] = json.RawMessage(`""`)
		data, _ := json.Marshal(obj)
		n := size - len(data)
		obj["future"] = json.RawMessage(`"` + strings.Repeat("x", n) + `"`)
		data, _ = json.Marshal(obj)
		if len(data) != size {
			t.Fatalf("padded to %d, not %d", len(data), size)
		}
		return data
	}
	at := pad(limit)
	if err := os.WriteFile(plansPath(y), at, 0o644); err != nil {
		t.Fatal(err)
	}
	if v := e.view(); v.Error != "" || len(v.Plans) != 1 {
		t.Fatalf("a file of exactly %d bytes: %q", limit, v.Error)
	}
	over := pad(limit + 1)
	if err := os.WriteFile(plansPath(y), over, 0o644); err != nil {
		t.Fatal(err)
	}
	e.restart("")
	if v := e.view(); !strings.Contains(v.Error, "larger than") {
		t.Fatalf("a file one byte over: %q", v.Error)
	}
	e.clock.Add(2 * time.Minute)
	e.tick()
	if _, err := e.svc.Save(ws, PlanInput{Title: "x", Instructions: "y", Schedule: daily("10:00"), Timezone: "UTC"}); err == nil {
		t.Fatal("saved over a file it cannot read")
	}
	if got, _ := os.ReadFile(plansPath(y)); !bytes.Equal(got, over) || len(e.prompts()) != 0 {
		t.Fatalf("the file changed or a run went out: %d prompts", len(e.prompts()))
	}
}

// A run record that would be larger than it is read at is not written: the
// record on disk stays as it was, and reads.
func TestRunRecordOverTheLimitIsNotWritten(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Add(time.Minute)
	e.tick()
	id := "s1-20261116t0900"
	path := filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules", "runs", id+".json")
	// Padded with a field this version does not know to 100 bytes under the
	// bound, as the scheduler writes it.
	var obj map[string]json.RawMessage
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	obj["future"] = json.RawMessage(`""`)
	data, _ = json.MarshalIndent(obj, "", "  ")
	obj["future"] = json.RawMessage(`"` + strings.Repeat("x", maxRunFile-100-len(data)-1) + `"`)
	data, _ = json.MarshalIndent(obj, "", "  ")
	data = append(data, '\n')
	if len(data) != maxRunFile-100 {
		t.Fatalf("padded to %d", len(data))
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	e.restart("")
	if r := e.run(id); r.State == StateDamaged {
		t.Fatalf("a record within the bound reads as damaged: %s", r.Reason)
	}
	y := mustGet(t, e)
	sp := e.svc.spaceFor(y)
	// A change that keeps it within the bound is written, the field kept.
	if _, err := e.svc.update(sp, id, func(r *Run) bool { r.Reason = "x"; return true }); err != nil {
		t.Fatal(err)
	}
	within, _ := os.ReadFile(path)
	if len(within) > maxRunFile || !strings.Contains(string(within), `"future"`) {
		t.Fatalf("%d bytes", len(within))
	}
	// One that would push it over is refused, and the record stays.
	if _, err := e.svc.update(sp, id, func(r *Run) bool { r.Reason = strings.Repeat("<", 200); return true }); !errors.Is(err, errRunTooLarge) {
		t.Fatalf("growing it past the bound: %v", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, within) || !utf8.Valid(got) {
		t.Fatal("the record changed")
	}
	if r := e.run(id); r.State == StateDamaged || r.Reason != "x" {
		t.Fatalf("after the refusal: %s %q", r.State, r.Reason)
	}
}

func mustGet(t *testing.T, e *env) yesmen.Yesman {
	t.Helper()
	y, err := e.store.Get(ws)
	if err != nil {
		t.Fatal(err)
	}
	return y
}

// ---------- Close and the lock ----------

// locked reports whether another holder of the lock would be refused: the
// lock is taken for real, as another app would take it.
func locked(t *testing.T, path string) bool {
	t.Helper()
	f, err := lockFile(path)
	if errors.Is(err, errLocked) {
		return true
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return false
}

// Close keeps the lock while a delivery may still go out, and lets it go
// once the delivery ended, though Close no longer waits by then; no other
// app can take the run over before. Closing again waits for it.
func TestCloseLetsTheLockGoWhenDeliveriesEnd(t *testing.T) {
	e := newEnv(t, "noack", utc("2026-11-16T08:59:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Add(time.Minute)
	e.svc.Tick(t0ctx())
	e.waitPrompts(1)

	start := time.Now()
	e.svc.Close()
	if waited := time.Since(start); waited < closeWait-100*time.Millisecond {
		t.Fatalf("Close returned after %v with a delivery under way", waited)
	}
	if !locked(t, e.cfg.LockPath) {
		t.Fatal("the lock was let go while a delivery may still go out")
	}
	other := New(Config{Yesmen: e.store, Agents: e.agents, Now: e.clock.Now, LockPath: e.cfg.LockPath})
	defer other.Close()
	if other.Owner() {
		t.Fatal("another scheduler took the tasks over while a delivery may still go out")
	}

	// The delivery ends with its lead; no Close is waiting now.
	e.agents.StopAll()
	deadline := time.Now().Add(10 * time.Second)
	for locked(t, e.cfg.LockPath) {
		if time.Now().After(deadline) {
			t.Fatal("the lock is still held after the delivery ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	start = time.Now()
	e.svc.Close()
	if waited := time.Since(start); waited > closeWait/2 {
		t.Fatalf("closing again took %v", waited)
	}
	if e.svc.Owner() {
		t.Fatal("a closed scheduler owns the tasks")
	}
	// The other app takes over, and does not send the run again.
	if !other.Owner() {
		t.Fatal("the lock was not taken over")
	}
	other.Tick(t0ctx())
	other.Wait()
	if n := len(e.prompts()); n != 1 {
		t.Fatalf("%d prompts", n)
	}
}

// Close called at once from many goroutines, and again, returns each time
// and lets the lock go once; so does a scheduler that never started or
// never took the lock. Nothing starts after it.
func TestCloseConcurrentAndRepeated(t *testing.T) {
	e := newEnv(t, "noack", utc("2026-11-16T08:59:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.svc.Start()
	e.clock.Add(time.Minute)
	e.svc.Wake()
	e.waitPrompts(1)
	done := make(chan struct{})
	for range 8 {
		go func() { e.svc.Close(); done <- struct{}{} }()
	}
	time.Sleep(50 * time.Millisecond)
	e.agents.StopAll()
	for range 8 {
		<-done
	}
	e.svc.Close()
	if locked(t, e.cfg.LockPath) {
		t.Fatal("the lock is still held")
	}
	e.svc.Start() // no loop starts again
	if _, err := e.svc.RunNow(ws, "s1", "00000000000000c1", ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("run after Close: %v", err)
	}

	idle := New(Config{Yesmen: e.store, Agents: e.agents, Now: e.clock.Now, LockPath: e.cfg.LockPath})
	idle.Close()
	idle.Close()
	if idle.Owner() || locked(t, e.cfg.LockPath) {
		t.Fatal("a scheduler closed before it began holds the lock")
	}
}
