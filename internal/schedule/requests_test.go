package schedule

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// A click is one run for good: whatever the clock did, a request taken is
// refused again after its record left the history, also after a restart.

// clickAt is a request ID as the page makes it, at time at.
func clickAt(at time.Time, n int) string { return fmt.Sprintf("%012x%012x", at.UnixMilli(), n) }

func (e *env) pruned() prunedFile {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules", "pruned.json"))
	if err != nil {
		e.t.Fatal(err)
	}
	f, err := decodePruned(data)
	if err != nil {
		e.t.Fatal(err)
	}
	return f
}

// pruneAfter makes enough newer closed runs that every older record and
// the exact list's oldest entries are pruned.
func (e *env) pruneAfter() {
	e.t.Helper()
	e.manualRuns("s1", keepRuns+maxPrunedRequests+2, yesmen.TaskDone)
	e.tick()
}

// A click taken, more runs after it, then the clock set back by days:
// its record is pruned, and then more than maxPrunedRequests clicks made
// since are pruned after it. The floor cannot cover it (it is ahead of the
// clock), so pruned.json keeps it exactly and it is refused, also after a
// restart. New clicks still run. Once the clock is past it again, it folds
// into the floor and the list is bounded again.
func TestClickTakenBeforeTheClockWentBackIsNotSentAgain(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T12:00:00Z"))
	e.save(PlanInput{Schedule: daily("09:00")})
	first := e.manualRuns("s1", 1, yesmen.TaskDone)[0]
	req := strings.TrimPrefix(first.ID, "s1-m-")
	e.manualRuns("s1", keepRuns, yesmen.TaskDone) // newer than it, kept
	e.clock.Add(-72 * time.Hour)
	e.pruneAfter() // it is pruned, after every click made since
	e.pruneAfter() // and as many again after it
	if e.exists(first.ID) {
		t.Fatalf("the first run was not pruned: %d records", len(e.runFiles()))
	}
	if p := e.pruned().Plans["s1"]; !slices.Contains(p.Requests, req) || p.Floor >= requestTime(req) || p.Requests[0] != req {
		t.Fatalf("pruned.json does not keep the click exactly: floor %q, %d requests", p.Floor, len(p.Requests))
	}
	e.restart("")
	before := len(e.prompts())
	for range 2 {
		if r, err := e.svc.RunNow(ws, "s1", req, ""); !errors.Is(err, ErrRequestUsed) {
			t.Fatalf("the click taken before the clock went back ran again: %+v %v", r, err)
		}
	}
	e.svc.Wait()
	if n := len(e.prompts()); n != before {
		t.Fatalf("%d prompts, want %d", n, before)
	}
	// A new click runs, at the clock's time.
	e.manualRuns("s1", 1, yesmen.TaskDone)
	e.waitPrompts(before + 1)

	// The clock reaches the click again: the floor covers it, and the list
	// is back to its bound.
	e.clock.Add(96 * time.Hour)
	e.pruneAfter()
	p := e.pruned().Plans["s1"]
	if len(p.Requests) > maxPrunedRequests || p.Floor < requestTime(req) {
		t.Fatalf("not folded into the floor: floor %q, %d requests", p.Floor, len(p.Requests))
	}
	e.restart("")
	if _, err := e.svc.RunNow(ws, "s1", req, ""); !errors.Is(err, ErrRequestUsed) {
		t.Fatalf("after the floor covered it: %v", err)
	}
}

// A new click whose ID carries a time more than a day ahead of the clock
// is refused before it is claimed: no record, no board task, nothing sent,
// nothing to keep. Clicks within the day, and clicks arriving out of their
// order, run once each; repeated, each returns its run.
func TestClickAheadOfTheClockIsRefusedBeforeItIsClaimed(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T12:00:00Z"))
	e.save(PlanInput{Schedule: daily("09:00")})
	now := e.clock.Now()
	for _, req := range []string{clickAt(now.Add(72*time.Hour), 1), clickAt(now.Add(requestAhead+time.Second), 2), "eeeeeeeeeeeeeeee"} {
		for range 2 {
			if r, err := e.svc.RunNow(ws, "s1", req, ""); !errors.Is(err, ErrRequestAhead) {
				t.Fatalf("%s: %+v %v", req, r, err)
			}
		}
		if e.exists("s1-m-" + req) {
			t.Fatalf("%s was recorded", req)
		}
	}
	e.svc.Wait()
	if n := len(e.prompts()); n != 0 || len(e.runFiles()) != 0 || len(e.board().Tasks) != 0 {
		t.Fatalf("%d prompts, records %v, %d board tasks", n, e.runFiles(), len(e.board().Tasks))
	}
	later, sooner, ahead := clickAt(now.Add(2*time.Second), 3), clickAt(now.Add(time.Second), 4), clickAt(now.Add(23*time.Hour), 5)
	var ids []string
	for _, req := range []string{later, sooner, ahead} {
		r, err := e.svc.RunNow(ws, "s1", req, "")
		if err != nil {
			t.Fatalf("%s: %v", req, err)
		}
		e.svc.Wait()
		ids = append(ids, r.ID)
	}
	for i, req := range []string{later, sooner, ahead} {
		if r, err := e.svc.RunNow(ws, "s1", req, ""); err != nil || r.ID != ids[i] {
			t.Fatalf("%s again: %+v %v", req, r, err)
		}
	}
	e.svc.Wait()
	if n := len(e.prompts()); n != 3 {
		t.Fatalf("%d prompts, want 3", n)
	}
}

// A click taken by a version whose request IDs did not start with their
// time is still answered: by its record while it is kept, and exactly by
// pruned.json once the record goes, however many newer ones follow, also
// after a restart.
func TestTakenClickWithoutATimeIsAnsweredAfterItsRecordGoes(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T12:00:00Z"))
	e.save(PlanInput{Schedule: daily("09:00")})
	made := e.manualRuns("s1", 1, yesmen.TaskDone)[0]
	e.close()
	// As that version left it: the same run under a random request ID.
	legacy := "eeeeeeeeeeeeeeee"
	id := "s1-m-" + legacy
	if err := os.Rename(e.runPath(made.ID), e.runPath(id)); err != nil {
		t.Fatal(err)
	}
	e.editRun(id, func(r map[string]any) { r["id"] = id })
	e.restart("")
	before := len(e.prompts())
	if r, err := e.svc.RunNow(ws, "s1", legacy, ""); err != nil || r.ID != id {
		t.Fatalf("the taken click: %+v %v", r, err)
	}
	e.pruneAfter()
	if e.exists(id) {
		t.Fatal("its record was not pruned")
	}
	if p := e.pruned().Plans["s1"]; p.Requests[0] != legacy || len(p.Requests) != maxPrunedRequests {
		t.Fatalf("pruned.json: %d requests, has it %v", len(p.Requests), slices.Contains(p.Requests, legacy))
	}
	e.restart("")
	before = len(e.prompts())
	if r, err := e.svc.RunNow(ws, "s1", legacy, ""); !errors.Is(err, ErrRequestUsed) {
		t.Fatalf("the taken click ran again: %+v %v", r, err)
	}
	e.svc.Wait()
	if n := len(e.prompts()); n != before {
		t.Fatalf("%d prompts, want %d", n, before)
	}
}
