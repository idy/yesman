package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// What the history keeps and checks again: pruning never removes what a
// run still answers for, and runs waiting on a receipt are checked again
// after a quit.

func (e *env) runPath(id string) string {
	return filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules", "runs", id+".json")
}

func (e *env) exists(id string) bool {
	_, err := os.Stat(e.runPath(id))
	return err == nil
}

// manualRuns makes n manual runs of a plan, one a second, under timed
// request IDs, and closes each run's board task with status.
func (e *env) manualRuns(plan string, n int, status string) []Run {
	e.t.Helper()
	var out []Run
	for range n {
		e.clock.Add(time.Second)
		r, err := e.svc.RunNow(ws, plan, e.requestID(), "")
		if err != nil {
			e.t.Fatal(err)
		}
		e.svc.Wait()
		if status != "" {
			e.setTask(e.run(r.ID).Board, status)
		}
		out = append(out, r)
	}
	return out
}

var requests int

// requestID is a request ID as the page makes them: the time, then random.
func (e *env) requestID() string {
	requests++
	return fmt.Sprintf("%012x%012x", e.clock.Now().UnixMilli(), requests)
}

func (e *env) editRun(id string, fn func(map[string]any)) {
	e.t.Helper()
	data, err := os.ReadFile(e.runPath(id))
	if err != nil {
		e.t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		e.t.Fatal(err)
	}
	fn(obj)
	data, _ = json.Marshal(obj)
	if err := os.WriteFile(e.runPath(id), data, 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// A missed record is caught up once, also after the catch-up's own record
// left the history, and also when a crash kept the missed record from
// learning of its catch-up before that.
func TestCatchUpOnceAfterItsRecordWasPruned(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(fmt.Sprint("crash=", crash), func(t *testing.T) {
			e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
			e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
			e.clock.Add(5 * time.Hour)
			e.tick()
			missed := e.plan("s1").Runs[0]
			c, err := e.svc.RunNow(ws, "s1", e.requestID(), missed.ID)
			if err != nil {
				t.Fatal(err)
			}
			e.svc.Wait()
			e.setTask(e.run(c.ID).Board, yesmen.TaskDone)
			if crash {
				e.close()
				e.editRun(missed.ID, func(m map[string]any) { delete(m, "caughtUp") })
				e.restart("")
				if got := e.run(missed.ID); got.CaughtUp != "" || !got.CanCatchUp {
					t.Fatalf("the crash was not simulated: %+v", got)
				}
			}
			e.manualRuns("s1", keepRuns+2, yesmen.TaskDone)
			e.tick()
			if e.exists(c.ID) {
				t.Fatalf("the catch-up's record was not pruned: %v", e.runFiles())
			}
			if got := e.run(missed.ID); got.CaughtUp != c.ID || got.CanCatchUp {
				t.Fatalf("the missed record forgot its catch-up: %+v", got)
			}
			before := len(e.prompts())
			for range 2 {
				if r, err := e.svc.RunNow(ws, "s1", e.requestID(), missed.ID); !errors.Is(err, errCaughtUp) {
					t.Fatalf("caught up again after pruning: %+v %v", r, err)
				}
			}
			e.restart("")
			if r, err := e.svc.RunNow(ws, "s1", e.requestID(), missed.ID); !errors.Is(err, errCaughtUp) {
				t.Fatalf("caught up again after a restart: %+v %v", r, err)
			}
			e.svc.Wait()
			if n := len(e.prompts()); n != before {
				t.Fatalf("%d prompts, want %d", n, before)
			}
			if n := len(e.scheduledTasks(c.ID)); n != 1 {
				t.Fatalf("the catch-up has %d board tasks", n)
			}
		})
	}
}

// A click's request repeated after its run's record was pruned is not a
// new run, also after a restart: pruned.json keeps its request ID.
func TestPrunedRequestIsNotANewRun(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T12:00:00Z"))
	e.save(PlanInput{Schedule: daily("09:00")})
	first := e.manualRuns("s1", 1, yesmen.TaskDone)[0]
	req := strings.TrimPrefix(first.ID, "s1-m-")
	e.manualRuns("s1", keepRuns+1, yesmen.TaskDone)
	e.tick()
	if e.exists(first.ID) {
		t.Fatalf("the first run was not pruned: %v", e.runFiles())
	}
	before := len(e.prompts())
	if r, err := e.svc.RunNow(ws, "s1", req, ""); !errors.Is(err, ErrRequestUsed) {
		t.Fatalf("the pruned request ran again: %+v %v", r, err)
	}
	e.restart("")
	if r, err := e.svc.RunNow(ws, "s1", req, ""); !errors.Is(err, ErrRequestUsed) {
		t.Fatalf("the pruned request ran again after a restart: %+v %v", r, err)
	}
	e.svc.Wait()
	if n := len(e.prompts()); n != before {
		t.Fatalf("%d prompts, want %d", n, before)
	}
	// A new click still runs.
	e.manualRuns("s1", 1, "")
	if n := len(e.waitPrompts(before + 1)); n != before+1 {
		t.Fatalf("%d prompts", n)
	}
	data, err := os.ReadFile(filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules", "pruned.json"))
	if err != nil || !strings.Contains(string(data), req) {
		t.Fatalf("pruned.json: %s %v", data, err)
	}
}

// pruned.json keeps the newest request IDs of a plan exactly and a floor
// below them; an ID without a plausible time does not raise the floor.
func TestPrunedRequestsFloor(t *testing.T) {
	f, _ := decodePruned(nil)
	at := func(ms int64, n int) string { return fmt.Sprintf("%012x%012x", ms, n) }
	latest := fmt.Sprintf("%012x", int64(2_000_000_000_000))
	var ids []string
	for i := range maxPrunedRequests + 6 {
		ids = append(ids, at(1_700_000_000_000+int64(i), i))
	}
	f.add("s1", ids[:10], latest)
	f.add("s1", ids[10:], latest)
	p := f.Plans["s1"]
	if len(p.Requests) != maxPrunedRequests || p.Floor != requestTime(ids[5]) {
		t.Fatalf("requests %d, floor %q", len(p.Requests), p.Floor)
	}
	for i, id := range ids {
		if !f.used("s1", id) {
			t.Fatalf("request %d is not known as used", i)
		}
	}
	if f.used("s1", at(1_700_000_001_000, 0)) || f.used("s2", ids[0]) {
		t.Fatal("a new request was taken as used")
	}
	legacy := strings.Repeat("f", 16)
	g, _ := decodePruned(nil)
	g.add("s1", []string{legacy}, latest)
	for i := range maxPrunedRequests {
		g.add("s1", []string{at(1_700_000_000_000+int64(i), i)}, latest)
	}
	if g.Plans["s1"].Floor != "" || g.used("s1", at(1_800_000_000_000, 0)) {
		t.Fatalf("a request ID without a plausible time raised the floor: %+v", g.Plans["s1"])
	}
	data, err := f.encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := decodePruned(data)
	if err != nil || !slices.Equal(back.Plans["s1"].Requests, p.Requests) || back.Plans["s1"].Floor != p.Floor {
		t.Fatalf("round trip: %+v %v", back.Plans["s1"], err)
	}
	for _, bad := range []string{`{"version":2,"plans":{}}`, `{"version":1,"plans":{"x":{}}}`, `{"version":1,"plans":{"s1":{"floor":"zz"}}}`, `{"version":1,"plans":{"s1":{"requests":["no"]}}}`, `[`} {
		if _, err := decodePruned([]byte(bad)); err == nil {
			t.Fatalf("%s was read", bad)
		}
	}
	keep, err := decodePruned([]byte(`{"version":1,"plans":{},"future":{"n":9007199254740993}}`))
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := keep.encode(); !strings.Contains(string(data), `"future":{"n":9007199254740993}`) {
		t.Fatalf("an unknown field was lost: %s", data)
	}
}

// When pruned.json cannot be read, nothing is pruned and no click is taken
// for a new run, while the schedule goes on; the file is left as it is.
func TestUnreadablePrunedFileFailsClosed(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.manualRuns("s1", keepRuns+2, yesmen.TaskDone)
	e.close()
	path := filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules", "pruned.json")
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.restart("")
	files := len(e.runFiles())
	e.tick()
	if n := len(e.runFiles()); n != files {
		t.Fatalf("pruned without pruned.json: %d files, then %d", files, n)
	}
	if v := e.view(); v.Notice == "" || v.Error != "" {
		t.Fatalf("view: notice %q, error %q", v.Notice, v.Error)
	}
	if _, err := e.svc.RunNow(ws, "s1", e.requestID(), ""); err == nil {
		t.Fatal("a click ran without pruned.json")
	}
	before := len(e.prompts())
	e.clock.Set(utc("2026-11-16T09:00:01Z"))
	e.tick()
	if n := len(e.waitPrompts(before + 1)); n != before+1 {
		t.Fatalf("the schedule stopped: %d prompts", n)
	}
	if data, _ := os.ReadFile(path); string(data) != "{" {
		t.Fatalf("pruned.json was changed: %q", data)
	}
}

// Runs whose board tasks are open stay in the history however many there
// are, with their receipts' IDs; new runs wait once maxOpenRuns are open;
// closed ones are pruned down to keepRuns.
func TestOpenRunsStayAndBoundNewOnes(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T12:00:00Z"))
	e.save(PlanInput{Schedule: daily("09:00")})
	closed := e.manualRuns("s1", keepRuns+5, yesmen.TaskDone)
	open := e.manualRuns("s1", maxOpenRuns, "")
	e.tick()
	for _, r := range open {
		if !e.exists(r.ID) {
			t.Fatalf("open run %s was pruned", r.ID)
		}
	}
	for i, r := range closed {
		if e.exists(r.ID) != (i >= len(closed)-keepRuns) {
			t.Fatalf("closed run %d of %d kept: %v; %d are kept", i, len(closed), e.exists(r.ID), keepRuns)
		}
	}
	p := e.plan("s1")
	if len(p.Runs) != maxRunsPerPlan || p.More != maxOpenRuns+keepRuns-maxRunsPerPlan {
		t.Fatalf("view lists %d runs and %d more", len(p.Runs), p.More)
	}
	before := len(e.prompts())
	if _, err := e.svc.RunNow(ws, "s1", e.requestID(), ""); !errors.Is(err, errTooManyOpen) {
		t.Fatalf("run %d was made: %v", maxOpenRuns+1, err)
	}
	if n := len(e.prompts()); n != before {
		t.Fatalf("%d prompts, want %d", n, before)
	}
	// Closing one lets a new run go, and the closed one can go later.
	e.setTask(e.run(open[len(open)-1].ID).Board, yesmen.TaskDropped)
	if _, err := e.svc.RunNow(ws, "s1", e.requestID(), ""); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	e.waitPrompts(before + 1)
}

// An archived plan is cleared away to make room only when no run of it is
// still open; its records go once the file was written.
func TestArchivedPlanWithOpenRunsIsKept(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T12:00:00Z"))
	e.save(PlanInput{Title: "with an open run", Schedule: daily("09:00")})
	e.save(PlanInput{Title: "with a closed run", Schedule: daily("09:00")})
	openRun := e.manualRuns("s1", 1, "")[0]
	closedRun := e.manualRuns("s2", 1, yesmen.TaskDone)[0]
	for _, id := range []string{"s1", "s2"} {
		e.clock.Add(time.Second) // archived in this order
		if err := e.svc.Archive(ws, id, e.plan(id).Revision); err != nil {
			t.Fatal(err)
		}
	}
	for n := 2; n < maxPlans; n++ {
		if activeCount(plansOfView(e.view())) == MaxActivePlans {
			for _, p := range e.view().Plans {
				if !p.Archived() {
					e.clock.Add(time.Second)
					if err := e.svc.Archive(ws, p.ID, p.Revision); err != nil {
						t.Fatal(err)
					}
					break
				}
			}
		}
		e.save(PlanInput{Title: fmt.Sprint("plan ", n), Schedule: daily("09:00")})
	}
	if n := len(e.view().Plans); n != maxPlans {
		t.Fatalf("%d plans", n)
	}
	for _, p := range e.view().Plans {
		if !p.Archived() {
			if err := e.svc.Archive(ws, p.ID, p.Revision); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	e.save(PlanInput{Title: "one more", Schedule: daily("09:00")})
	ids := map[string]bool{}
	for _, p := range e.view().Plans {
		ids[p.ID] = true
	}
	if !ids["s1"] || ids["s2"] || len(ids) != maxPlans {
		t.Fatalf("s1 kept %v, s2 kept %v, %d plans", ids["s1"], ids["s2"], len(ids))
	}
	if !e.exists(openRun.ID) || e.exists(closedRun.ID) {
		t.Fatalf("open run kept %v, closed run kept %v", e.exists(openRun.ID), e.exists(closedRun.ID))
	}
}

// A run left queued in the lead's queue when the app quit is checked once
// the app is open again: the queue went with the lead's process, so it is
// unknown, and nothing is sent again.
func TestQueuedRunIsCheckedAfterRestart(t *testing.T) {
	e := newEnv(t, "busy", utc("2026-11-16T08:59:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Set(utc("2026-11-16T09:00:01Z"))
	e.tick()
	e.waitPrompts(1)
	id := "s1-20261116t0900"
	before := e.run(id)
	if before.State != StateQueued {
		t.Fatalf("run %+v", before)
	}
	e.restart("")
	e.tick()
	after := e.run(id)
	if after.State != StateUnknown || after.Status != StateUnknown || len(after.Attempts) != 1 || after.Attempts[0].ID != before.Attempts[0].ID {
		t.Fatalf("after a restart: %+v", after)
	}
	if !after.CanCheck || after.CanRetry || !after.CanRunAgain {
		t.Fatalf("actions: %+v", after)
	}
	e.tick()
	if n := len(e.prompts()); n != 1 {
		t.Fatalf("%d prompts", n)
	}
	if n := len(e.scheduledTasks(id)); n != 1 {
		t.Fatalf("%d board tasks", n)
	}
}

// A receipt update the scheduler missed while the app runs is made up for:
// the next tick reads the receipts of runs waiting on one.
func TestMissedReceiptUpdateIsReadAgain(t *testing.T) {
	e := newEnv(t, "busy", utc("2026-11-16T08:59:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Set(utc("2026-11-16T09:00:01Z"))
	e.tick()
	e.waitPrompts(1)
	id := "s1-20261116t0900"
	e.tick() // read once since it was claimed: still queued
	if r := e.run(id); r.State != StateQueued {
		t.Fatalf("run %+v", r)
	}
	// The lead stops; its update waits unread, as when the loop is behind.
	e.agents.Stop(agent.ID(ws, yesmen.LeadName))
	e.tick()
	if r := e.run(id); r.State != StateQueued {
		t.Fatalf("read again with no update missed: %+v", r)
	}
	// Updates are then dropped: the queue of updates is full.
	for range cap(e.svc.events) + 1 {
		e.svc.OnAgentEvent(agent.Event{Agent: agent.ID(ws, yesmen.LeadName), Data: []byte(`{"type":"other","origin":null}`)})
	}
	e.tick()
	if r := e.run(id); r.State != StateUnknown {
		t.Fatalf("a dropped update was not made up for: %+v", r)
	}
	if n := len(e.prompts()); n != 1 {
		t.Fatalf("%d prompts", n)
	}
}

// An older copy of a receipt, read before a newer one was applied, never
// takes its place.
func TestOlderReceiptIsIgnored(t *testing.T) {
	r := Run{Kind: RunScheduled, State: StateSending, Attempts: []Attempt{{ID: "a"}}}
	interrupted := agent.Delivery{ID: "a", Revision: 4, State: "interrupted", Ack: "queued"}
	queued := agent.Delivery{ID: "a", Revision: 2, State: "queued", Ack: "queued"}
	if !applyReceipt(&r, "a", &interrupted, "") || r.State != StateUnknown {
		t.Fatalf("%+v", r)
	}
	if applyReceipt(&r, "a", &queued, "") || r.State != StateUnknown || r.Attempts[0].Rev != 4 {
		t.Fatalf("an older receipt was applied: %+v", r)
	}
}
