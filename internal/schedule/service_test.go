package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

func t0ctx() context.Context { return context.Background() }

func daily(at string) Spec { return Spec{Kind: KindDaily, Time: at} }

func str(s string) *string { return &s }

func (e *env) setTask(id, status string) {
	e.t.Helper()
	if _, err := e.store.ChangeBoard(ws, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: id, Status: &status}}}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) receipt(id string) (agent.Delivery, agent.DeliveryLookup) {
	e.t.Helper()
	d, found, err := e.agents.LookupDelivery(t0ctx(), agent.ID(ws, yesmen.LeadName), id, time.Time{})
	if err != nil {
		e.t.Fatal(err)
	}
	return d, found
}

// A due daily plan reaches the lead once, as a follow-up through the receipt
// path, with one board task; more ticks add nothing.
func TestDailyRunReachesTheLeadOnce(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:55:00Z"))
	p := e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.tick()
	if len(e.prompts()) != 0 || len(e.runFiles()) != 0 {
		t.Fatal("ran before it was due")
	}
	if next := e.plan(p.ID).Next; next == nil || !next.Equal(utc("2026-11-16T09:00:00Z")) {
		t.Fatalf("next = %v", next)
	}
	e.clock.Set(utc("2026-11-16T09:00:20Z"))
	e.tick()
	e.tick()
	e.clock.Set(utc("2026-11-16T09:05:00Z"))
	e.tick()
	prompts := e.waitPrompts(1)
	if len(prompts) != 1 || prompts[0].Behavior != "followUp" {
		t.Fatalf("prompts = %+v", prompts)
	}
	r := e.run("s1-20261116t0900")
	tasks := e.scheduledTasks(r.ID)
	if len(tasks) != 1 || r.Board != tasks[0].ID || tasks[0].Status != yesmen.TaskTodo || tasks[0].Schedule.Plan != "s1" {
		t.Fatalf("run %+v, board tasks %+v", r, tasks)
	}
	if r.Status != StateSent || r.State != StateSent || len(r.Attempts) != 1 {
		t.Fatalf("run = %+v", r)
	}
	msg := prompts[0].Message
	for _, want := range []string{"Scheduled task s1 \"Check the dependencies\"", "every day at 09:00 (UTC)", "Board task " + r.Board, "Check for outdated dependencies and report."} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, r.ID) || strings.Contains(msg, r.Attempts[0].ID) {
		t.Errorf("the message carries private IDs:\n%s", msg)
	}
	d, found := e.receipt(r.Attempts[0].ID)
	if found != agent.DeliveryFound || d.Origin == nil || d.Origin.Kind != agent.OriginSchedule || d.Origin.Run != r.ID {
		t.Fatalf("receipt = %+v (%v)", d, found)
	}
	if next := e.plan(p.ID).Next; next == nil || !next.Equal(utc("2026-11-17T09:00:00Z")) {
		t.Fatalf("next after the run = %v", next)
	}
}

// What a run achieved is its board task, never the lead's reply or settle.
func TestBoardStatusIsTheOutcome(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Set(utc("2026-11-16T09:00:01Z"))
	e.tick()
	e.waitPrompts(1)
	time.Sleep(50 * time.Millisecond) // the fake run settles
	e.tick()
	r := e.run("s1-20261116t0900")
	if r.Status != StateSent {
		t.Fatalf("a settled run is %s before its board task changed", r.Status)
	}
	e.setTask(r.Board, yesmen.TaskDoing)
	if r := e.run(r.ID); r.Status != yesmen.TaskDoing {
		t.Fatalf("status = %s, want doing", r.Status)
	}
	done := yesmen.TaskDone
	if _, err := e.store.ChangeBoard(ws, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: r.Board, Status: &done, Note: str("Two packages updated, PR #9")}}}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	r = e.run(r.ID)
	if r.Status != yesmen.TaskDone || r.Outcome == nil || r.Outcome.Status != yesmen.TaskDone || r.Outcome.Note != "Two packages updated, PR #9" {
		t.Fatalf("run = %+v", r)
	}
	if _, err := e.store.ChangeBoard(ws, yesmen.BoardChange{Remove: []string{r.Board}}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if r := e.run(r.ID); r.Status != yesmen.TaskDone || r.Task != nil {
		t.Fatalf("after its task was removed: %+v", r)
	}
}

// Occurrences the app was closed for become one missed record, not a flood;
// catching up runs once, whatever the clicks.
func TestMissedWhileClosedIsCoalescedAndCaughtUpOnce(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.close()
	e.clock.Set(utc("2026-11-19T12:00:00Z"))
	e.restart("")
	e.tick()
	e.tick()
	if n := len(e.prompts()); n != 0 {
		t.Fatalf("%d runs replayed after the app was closed", n)
	}
	p := e.plan("s1")
	if len(p.Runs) != 1 {
		t.Fatalf("runs = %+v", p.Runs)
	}
	m := p.Runs[0]
	if m.Kind != RunMissed || m.Count != 4 || !m.First.Equal(utc("2026-11-16T09:00:00Z")) || !m.Last.Equal(utc("2026-11-19T09:00:00Z")) || !m.CanCatchUp {
		t.Fatalf("missed = %+v", m)
	}
	if p.Next == nil || !p.Next.Equal(utc("2026-11-20T09:00:00Z")) {
		t.Fatalf("next = %v", p.Next)
	}
	var wg sync.WaitGroup
	results := make([]Run, 3)
	errs := make([]error, 3)
	for i := range 3 {
		wg.Go(func() { results[i], errs[i] = e.svc.RunNow(ws, "s1", "00000000000000a1", m.ID) })
	}
	wg.Wait()
	for i := range 3 {
		if errs[i] != nil || results[i].ID != m.ID+"-c" {
			t.Fatalf("catch-up %d: %+v %v", i, results[i], errs[i])
		}
	}
	e.svc.Wait()
	if again, err := e.svc.RunNow(ws, "s1", "00000000000000a2", m.ID); err != nil || again.ID != m.ID+"-c" {
		t.Fatalf("catching up again: %+v %v", again, err)
	}
	prompts := e.waitPrompts(1)
	time.Sleep(50 * time.Millisecond)
	if n := len(e.prompts()); n != 1 || !strings.Contains(prompts[0].Message, "missed 4 times") {
		t.Fatalf("%d prompts: %+v", n, prompts)
	}
	if got := e.run(m.ID); got.CaughtUp != m.ID+"-c" || got.CanCatchUp {
		t.Fatalf("missed after catch-up: %+v", got)
	}
}

// When the newest occurrence is still on time after a sleep, it runs and
// the older ones are missed.
func TestSleepRunsTheNewestOnTimeOnly(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Set(utc("2026-11-19T09:04:00Z")) // woke 4 minutes after the fourth
	e.tick()
	e.waitPrompts(1)
	p := e.plan("s1")
	var kinds []string
	for _, r := range p.Runs {
		kinds = append(kinds, fmt.Sprintf("%s:%s:%d", r.ID, r.Kind, r.Count))
	}
	if len(p.Runs) != 2 || e.run("s1-x-20261116t0900").Count != 3 || e.run("s1-20261119t0900").Kind != RunScheduled {
		t.Fatalf("runs = %v", kinds)
	}
}

// Setting the clock back never runs an occurrence again; a jump forward
// misses what it skipped instead of running it.
func TestClockJumps(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Set(utc("2026-11-16T09:00:05Z"))
	e.tick()
	e.waitPrompts(1)
	for _, at := range []string{"2026-11-16T08:30:00Z", "2026-11-16T08:59:59Z", "2026-11-16T09:00:30Z", "2026-11-16T09:09:00Z"} {
		e.clock.Set(utc(at))
		e.tick()
	}
	if n := len(e.prompts()); n != 1 {
		t.Fatalf("set back: %d runs", n)
	}
	if next := e.plan("s1").Next; next == nil || !next.Equal(utc("2026-11-17T09:00:00Z")) {
		t.Fatalf("next = %v", next)
	}
	e.clock.Set(utc("2026-11-18T15:00:00Z"))
	e.tick()
	if n := len(e.prompts()); n != 1 {
		t.Fatalf("jumped forward: %d runs", n)
	}
	if m := e.run("s1-x-20261117t0900"); m.Count != 2 || m.Kind != RunMissed {
		t.Fatalf("missed = %+v", m)
	}
}

// Across fall back, 01:30 occurs twice but runs once; across spring
// forward, 02:30 runs at 03:30.
func TestDSTRunsOncePerLocalTime(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-01T07:00:00Z"))
	e.save(PlanInput{Schedule: daily("01:30"), Timezone: "America/Los_Angeles", Enabled: true})
	for _, at := range []string{"2026-11-01T08:30:10Z", "2026-11-01T09:00:00Z", "2026-11-01T09:30:10Z", "2026-11-01T09:35:00Z"} {
		e.clock.Set(utc(at))
		e.tick()
	}
	e.waitPrompts(1)
	time.Sleep(50 * time.Millisecond)
	if n := len(e.prompts()); n != 1 || len(e.plan("s1").Runs) != 1 {
		t.Fatalf("fold ran %d times: %+v", n, e.plan("s1").Runs)
	}

	g := newEnv(t, "", utc("2026-03-08T08:00:00Z"))
	g.save(PlanInput{Schedule: daily("02:30"), Timezone: "America/Los_Angeles", Enabled: true})
	g.clock.Set(utc("2026-03-08T10:00:00Z")) // 03:00 PDT, half an hour after the gap
	g.tick()
	if len(g.prompts()) != 0 {
		t.Fatal("ran inside the gap")
	}
	g.clock.Set(utc("2026-03-08T10:30:05Z")) // 03:30 PDT
	g.tick()
	g.waitPrompts(1)
	if r := g.run("s1-20260308t0230"); !r.At.Equal(utc("2026-03-08T10:30:00Z")) {
		t.Fatalf("gap run = %+v", r)
	}
}

// Two schedulers ticking at once, as two processes or overlapping ticks
// could, claim an occurrence once: one record, one board task, one prompt.
func TestConcurrentTicksClaimOnce(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	// Without a lock (one the system would not grant, say) both think they
	// own the tasks: the exclusive claim still makes one run.
	e.svc.Close()
	e.svc = New(Config{Yesmen: e.store, Agents: e.agents, Now: e.clock.Now})
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	other := New(Config{Yesmen: yesmen.New(filepath.Join(e.root, "yesmen"), filepath.Join(e.root, "archive")), Agents: e.agents, Now: e.clock.Now})
	defer other.Close()
	e.clock.Set(utc("2026-11-16T09:00:01Z"))
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if i%2 == 0 {
				e.svc.Tick(t0ctx())
			} else {
				other.Tick(t0ctx())
			}
		})
	}
	wg.Wait()
	e.svc.Wait()
	other.Wait()
	// Later ticks of either never carry the other's run a second time.
	e.svc.Tick(t0ctx())
	other.Tick(t0ctx())
	e.svc.Wait()
	other.Wait()
	e.waitPrompts(1)
	time.Sleep(100 * time.Millisecond)
	if n := len(e.prompts()); n != 1 {
		t.Fatalf("%d prompts", n)
	}
	if files := e.runFiles(); len(files) != 1 {
		t.Fatalf("run files = %v", files)
	}
	if tasks := e.scheduledTasks("s1-20261116t0900"); len(tasks) != 1 {
		t.Fatalf("board tasks = %+v", tasks)
	}
}

// Only the app holding the lock schedules or changes plans; the other shows
// them read-only and takes over once the lock is free.
func TestOwnerLock(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	lock := e.cfg.LockPath
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	second := New(Config{Yesmen: yesmen.New(filepath.Join(e.root, "yesmen"), filepath.Join(e.root, "archive")), Agents: e.agents, Now: e.clock.Now, LockPath: lock})
	defer second.Close()
	if second.Owner() {
		t.Fatal("two apps own the scheduled tasks")
	}
	if _, err := second.Save(ws, PlanInput{Title: "x", Instructions: "y", Schedule: daily("10:00"), Timezone: "UTC"}); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("save from the other app: %v", err)
	}
	if _, err := second.RunNow(ws, "s1", "00000000000000b1", ""); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("run from the other app: %v", err)
	}
	v, err := second.View(ws)
	if err != nil || v.Owner || len(v.Plans) != 1 {
		t.Fatalf("other view: %+v %v", v, err)
	}
	e.clock.Set(utc("2026-11-16T09:00:01Z"))
	second.Tick(t0ctx())
	second.Wait()
	if len(e.runFiles()) != 0 {
		t.Fatal("the app without the lock claimed a run")
	}
	e.svc.Close()
	if !second.Owner() {
		t.Fatal("the lock was not taken over")
	}
	second.Tick(t0ctx())
	second.Wait()
	e.waitPrompts(1)
}

// Turning a plan off before its claim stops the run; after the claim, the
// run goes on and nothing is cancelled.
func TestPauseAndClaimAreOrdered(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:58:00Z"))
	p := e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	p, err := e.svc.SetEnabled(ws, p.ID, false, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Set(utc("2026-11-16T09:00:30Z"))
	e.tick()
	if len(e.runFiles()) != 0 {
		t.Fatal("a paused plan ran")
	}
	e.clock.Set(utc("2026-11-16T09:01:00Z"))
	p, err = e.svc.SetEnabled(ws, p.ID, true, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	e.tick()
	if len(e.runFiles()) != 0 {
		t.Fatal("turning it on ran the occurrence it was off for")
	}
	if next := e.plan(p.ID).Next; next == nil || !next.Equal(utc("2026-11-17T09:00:00Z")) {
		t.Fatalf("next = %v", next)
	}
	// Claimed, then paused while on its way: it still arrives.
	e.clock.Set(utc("2026-11-17T09:00:02Z"))
	e.svc.Tick(t0ctx())
	if _, err := e.svc.SetEnabled(ws, p.ID, false, p.Revision); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	e.waitPrompts(1)
	if r := e.run("s1-20261117t0900"); r.Status != StateSent {
		t.Fatalf("claimed run after pause: %+v", r)
	}
	if e.plan(p.ID).State != "off" {
		t.Fatal("the plan is not off")
	}
}

// One click is one run, however often it repeats; a new click waits until
// the last run was sent.
func TestManualRunIsOnePerClick(t *testing.T) {
	e := newEnv(t, "noack", utc("2026-11-16T12:00:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: false})
	var wg sync.WaitGroup
	ids := make([]string, 4)
	for i := range 4 {
		wg.Go(func() {
			r, err := e.svc.RunNow(ws, "s1", "0123456789abcdef", "")
			if err != nil {
				t.Error(err)
			}
			ids[i] = r.ID
		})
	}
	wg.Wait()
	for _, id := range ids {
		if id != "s1-m-0123456789abcdef" {
			t.Fatalf("ids = %v", ids)
		}
	}
	e.waitPrompts(1)
	if _, err := e.svc.RunNow(ws, "s1", "fedcba9876543210", ""); err == nil || !strings.Contains(err.Error(), "still being sent") {
		t.Fatalf("a second run while the first is on its way: %v", err)
	}
	if n := len(e.scheduledTasks("s1-m-0123456789abcdef")); n != 1 {
		t.Fatalf("board tasks = %d", n)
	}
}

// A crash after the board task was added, before its record said so, adds
// no second task on recovery and sends once.
func TestCrashAfterBoardWrite(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	var failed atomic.Bool
	e.reopen(Config{writeRun: func(path string, data []byte) error {
		if strings.Contains(string(data), `"state": "boarded"`) && !failed.Swap(true) {
			return errors.New("crash")
		}
		return writeFile(path, data)
	}})
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Set(utc("2026-11-16T09:00:01Z"))
	e.tick()
	id := "s1-20261116t0900"
	if r := e.run(id); r.State != StateClaimed || len(e.scheduledTasks(id)) != 1 {
		t.Fatalf("after the crash: %+v, tasks %+v", r, e.scheduledTasks(id))
	}
	e.clock.Set(utc("2026-11-16T09:03:00Z"))
	e.restart("")
	e.tick()
	e.waitPrompts(1)
	time.Sleep(50 * time.Millisecond)
	if n, tasks := len(e.prompts()), e.scheduledTasks(id); n != 1 || len(tasks) != 1 || e.run(id).Board != tasks[0].ID {
		t.Fatalf("%d prompts, tasks %+v, run %+v", n, tasks, e.run(id))
	}
}

// A crash after the submission ID was recorded, before the submission:
// within the window the same ID is sent once; later it is not sent, and an
// explicit retry sends it under a new ID with the same board task.
func TestCrashBeforeSubmit(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint("late=", late), func(t *testing.T) {
			e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
			e.reopen(Config{afterAttempt: func(Run) error { return errors.New("crash") }})
			e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
			e.clock.Set(utc("2026-11-16T09:00:01Z"))
			e.tick()
			id := "s1-20261116t0900"
			r := e.run(id)
			if r.State != StateSending || len(r.Attempts) != 1 {
				t.Fatalf("after the crash: %+v", r)
			}
			first := r.Attempts[0].ID
			if late {
				e.clock.Set(utc("2026-11-16T09:30:00Z"))
			} else {
				e.clock.Set(utc("2026-11-16T09:04:00Z"))
			}
			e.restart("")
			e.tick()
			if !late {
				e.waitPrompts(1)
				if _, found := e.receipt(first); found != agent.DeliveryFound {
					t.Fatal("the recorded ID was not the one sent")
				}
				return
			}
			r = e.run(id)
			if r.State != StateFailed || !r.CanRetry || len(e.prompts()) != 0 || !r.Attempts[0].Definite {
				t.Fatalf("late recovery: %+v, %d prompts", r, len(e.prompts()))
			}
			if _, err := e.svc.Retry(ws, id); err != nil {
				t.Fatal(err)
			}
			e.svc.Wait()
			e.waitPrompts(1)
			r = e.run(id)
			if len(r.Attempts) != 2 || r.Attempts[1].ID == first || r.State != StateSent || len(e.scheduledTasks(id)) != 1 {
				t.Fatalf("after retry: %+v", r)
			}
		})
	}
}

// pi received the prompt but its answer was lost and the app went away
// before recording anything: the next start never sends it again.
func TestLostAcknowledgmentIsNeverResent(t *testing.T) {
	e := newEnv(t, "noack", utc("2026-11-16T08:59:00Z"))
	var crashed atomic.Bool
	e.reopen(Config{writeRun: func(path string, data []byte) error {
		if crashed.Load() {
			return errors.New("the app is gone")
		}
		return writeFile(path, data)
	}})
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Set(utc("2026-11-16T09:00:01Z"))
	e.svc.Tick(t0ctx())
	e.waitPrompts(1)
	crashed.Store(true)
	e.agents.StopAll()
	e.svc.Wait()
	id := "s1-20261116t0900"
	if r := e.run(id); r.State != StateSending {
		t.Fatalf("before restart: %+v", r)
	}
	e.clock.Set(utc("2026-11-16T09:02:00Z"))
	e.restart("")
	e.tick()
	r := e.run(id)
	if r.State != StateUnknown || r.CanRetry || !r.CanRunAgain {
		t.Fatalf("after restart: %+v", r)
	}
	if _, err := e.svc.Check(ws, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Retry(ws, id); err == nil {
		t.Fatal("an unknown delivery was retried under a new ID")
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(e.prompts()); n != 1 {
		t.Fatalf("%d prompts, want 1", n)
	}
}

// An ID whose receipt expired or was evicted is never sent: it might have
// been delivered before.
func TestUnretainedReceiptFailsClosed(t *testing.T) {
	for _, how := range []string{"expired", "floor"} {
		t.Run(how, func(t *testing.T) {
			e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
			e.reopen(Config{afterAttempt: func(Run) error { return errors.New("crash") }})
			e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
			e.clock.Set(utc("2026-11-16T09:00:01Z"))
			e.tick()
			id := "s1-20261116t0900"
			path := filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules", "runs", id+".json")
			data, _ := os.ReadFile(path)
			var r Run
			_ = json.Unmarshal(data, &r)
			stamp := time.Now().Add(-25 * time.Hour)
			if how == "floor" {
				stamp = time.Now().Add(-time.Minute)
				// The lead's receipts were compacted past it.
				floor, _ := json.Marshal(map[string]any{"version": 2, "floor": time.Now().UnixMilli(), "records": []any{}})
				dir := filepath.Join(e.root, "yesmen", ws, ".yesman", "sessions", "lead")
				_ = os.MkdirAll(dir, 0o755)
				if err := os.WriteFile(filepath.Join(dir, "delivery.json"), floor, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			r.Attempts[0].ID = fmt.Sprintf("%012x%036x", stamp.UnixMilli(), 7)
			data, _ = json.Marshal(r)
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			e.restart("")
			e.tick()
			if got := e.run(id); got.State != StateUnknown || len(e.prompts()) != 0 {
				t.Fatalf("%+v, %d prompts", got, len(e.prompts()))
			}
		})
	}
}

// A busy lead gets the run as a follow-up in pi's queue, never steered or
// aborted; when its process ends with it still queued, the run is unknown.
func TestBusyLeadQueuesAFollowUp(t *testing.T) {
	e := newEnv(t, "busy", utc("2026-11-16T08:59:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Set(utc("2026-11-16T09:00:01Z"))
	e.tick()
	prompts := e.waitPrompts(1)
	id := "s1-20261116t0900"
	if r := e.run(id); r.Status != StateQueued || prompts[0].Behavior != "followUp" {
		t.Fatalf("run %+v, prompt %+v", r, prompts[0])
	}
	e.restart("")
	r, err := e.svc.Check(ws, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != StateUnknown {
		t.Fatalf("a queued message lost with its process: %+v", r)
	}
	if n := len(e.prompts()); n != 1 {
		t.Fatalf("%d prompts", n)
	}
}

// pi refusing a prompt is definite: the run can be sent again, keeping its
// board task.
func TestRejectedIsRetryable(t *testing.T) {
	e := newEnv(t, "reject", utc("2026-11-16T08:59:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Set(utc("2026-11-16T09:00:01Z"))
	e.tick()
	id := "s1-20261116t0900"
	r := e.run(id)
	if r.State != StateFailed || !r.CanRetry || !r.Attempts[0].Definite {
		t.Fatalf("rejected: %+v", r)
	}
	e.restart("")
	if _, err := e.svc.Retry(ws, id); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if r := e.run(id); r.State != StateSent || len(r.Attempts) != 2 || len(e.scheduledTasks(id)) != 1 {
		t.Fatalf("retried: %+v", r)
	}
	if n := len(e.prompts()); n != 2 {
		t.Fatalf("%d prompts", n)
	}
}

// While the last run's board task is open, the next occurrence is skipped,
// not piled on the lead.
func TestOverlapSkips(t *testing.T) {
	start := utc("2026-11-16T09:00:00Z")
	e := newEnv(t, "", start)
	e.save(PlanInput{Schedule: Spec{Kind: KindInterval, Every: 15}, Enabled: true})
	e.clock.Set(start.Add(15*time.Minute + time.Second))
	e.tick()
	e.waitPrompts(1)
	first := e.plan("s1").Runs[0]
	e.clock.Set(start.Add(30*time.Minute + time.Second))
	e.tick()
	skipped := e.run("s1-20261116t0930z")
	if skipped.Kind != RunSkipped || skipped.Prev != first.ID || !strings.Contains(skipped.Reason, first.Board) {
		t.Fatalf("skipped = %+v", skipped)
	}
	e.setTask(first.Board, yesmen.TaskDone)
	e.clock.Set(start.Add(45*time.Minute + time.Second))
	e.tick()
	e.waitPrompts(2)
}

// Dismissing a yesman stops its scheduling; nothing is written into the
// archive afterwards or into a new yesman with the same name, even by a run
// that was on its way.
func TestArchiveAndSameNameHire(t *testing.T) {
	e := newEnv(t, "noack", utc("2026-11-16T08:59:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Set(utc("2026-11-16T09:00:01Z"))
	e.svc.Tick(t0ctx())
	e.waitPrompts(1)
	old, _ := e.store.Get(ws)
	dest, err := e.store.Archive(ws, func(y yesmen.Yesman) error {
		e.svc.RetireYesman(y.Name, y.HiredAt)
		return e.agents.StopYesman(y.Name)
	})
	if err != nil {
		t.Fatal(err)
	}
	e.agents.ForgetYesman(ws, old.HiredAt)
	e.svc.Wait()
	archived := filepath.Join(dest, ".yesman", "schedules", "runs", "s1-20261116t0900.json")
	before, err := os.ReadFile(archived)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := e.store.Hire(ws); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(utc("2026-11-17T09:00:01Z"))
	e.tick()
	after, _ := os.ReadFile(archived)
	if string(before) != string(after) {
		t.Fatal("the archived run record changed after the archive")
	}
	var r Run
	_ = json.Unmarshal(after, &r)
	if r.State != StateSending {
		t.Fatalf("archived run = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the new yesman got the old one's schedules: %v", err)
	}
	if v := e.view(); len(v.Plans) != 0 {
		t.Fatalf("new yesman plans = %+v", v.Plans)
	}
}

// Fields this version does not know survive an edit; a file it cannot read
// is never written over, and links are not written through.
func TestFilesAreKeptSafe(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
	dir := filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules")
	_ = os.MkdirAll(dir, 0o755)
	plans := filepath.Join(dir, "plans.json")
	future := `{"version":1,"next":1,"owner":"kept","plans":[{"id":"s1","title":"T","instructions":"I","schedule":{"kind":"daily","time":"09:00"},"timezone":"UTC","enabled":false,"revision":1,"color":"blue"}]}`
	if err := os.WriteFile(plans, []byte(future), 0o644); err != nil {
		t.Fatal(err)
	}
	p := e.plan("s1")
	if _, err := e.svc.Save(ws, PlanInput{ID: "s1", Revision: p.Revision, Title: "T2", Instructions: "I", Schedule: daily("10:00"), Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(plans)
	if !strings.Contains(string(data), `"owner": "kept"`) || !strings.Contains(string(data), `"color": "blue"`) || !strings.Contains(string(data), `"title": "T2"`) {
		t.Fatalf("unknown fields lost:\n%s", data)
	}
	if _, err := e.svc.Save(ws, PlanInput{ID: "s1", Revision: p.Revision, Title: "stale", Instructions: "I", Schedule: daily("10:00"), Timezone: "UTC"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale edit: %v", err)
	}
	for _, bad := range []string{`{"version":1,"plans":[`, `{"version":9,"plans":[]}`} {
		if err := os.WriteFile(plans, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if v := e.view(); v.Error == "" {
			t.Fatalf("%s: no error shown", bad)
		}
		if _, err := e.svc.Save(ws, PlanInput{Title: "new", Instructions: "x", Schedule: daily("10:00"), Timezone: "UTC"}); err == nil {
			t.Fatalf("%s: saved over a file it cannot read", bad)
		}
		e.tick()
		if got, _ := os.ReadFile(plans); string(got) != bad {
			t.Fatalf("%s: the file changed to %s", bad, got)
		}
	}
	// A linked schedules folder is not written through.
	other := filepath.Join(e.root, "elsewhere")
	_ = os.MkdirAll(other, 0o755)
	_ = os.RemoveAll(dir)
	if err := os.Symlink(other, dir); err != nil {
		t.Skip("no symbolic links here")
	}
	if _, err := e.svc.Save(ws, PlanInput{Title: "new", Instructions: "x", Schedule: daily("10:00"), Timezone: "UTC"}); err == nil {
		t.Fatal("saved through a link")
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Fatalf("wrote through the link: %v", entries)
	}
}

func TestValidation(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
	_, err := e.svc.Save(ws, PlanInput{Title: "Once", Instructions: "x", Schedule: Spec{Kind: KindOnce, Date: "2026-11-15", Time: "09:00"}, Timezone: "UTC", Enabled: true})
	if fe, ok := err.(*FieldError); !ok || fe.Field != "time" {
		t.Fatalf("a past time saved on: %v", err)
	}
	p := e.save(PlanInput{Title: "Once", Schedule: Spec{Kind: KindOnce, Date: "2026-11-15", Time: "09:00"}, Enabled: false})
	if _, err := e.svc.SetEnabled(ws, p.ID, true, p.Revision); err == nil {
		t.Fatal("turned on with its time past")
	}
	for _, in := range []PlanInput{
		{Title: " ", Instructions: "x", Schedule: daily("09:00"), Timezone: "UTC"},
		{Title: "t", Instructions: " ", Schedule: daily("09:00"), Timezone: "UTC"},
		{Title: strings.Repeat("长", MaxTitle+1), Instructions: "x", Schedule: daily("09:00"), Timezone: "UTC"},
		{Title: "t", Instructions: strings.Repeat("x", MaxInstructions+1), Schedule: daily("09:00"), Timezone: "UTC"},
		{Title: "t", Instructions: "x", Schedule: daily("09:00"), Timezone: "Local"},
	} {
		if _, err := e.svc.Save(ws, in); err == nil {
			t.Errorf("saved %+v", in)
		}
	}
	d, err := e.svc.Draft(ws, PlanInput{Title: "Lead's draft", Instructions: "x", Schedule: daily("09:00"), Timezone: "UTC", Enabled: true})
	if err != nil || d.Enabled || d.CreatedBy != "lead" {
		t.Fatalf("draft = %+v %v", d, err)
	}
}
