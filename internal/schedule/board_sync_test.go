package schedule

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// What a run's board task says follows what is known of its delivery even
// when the board cannot be written at the time: the change is recorded with
// the run and made once the board can take it, after a restart too, once
// only, and never over a change the lead or the user made meanwhile.

// boardEnv is an env whose board writes can be made to fail, and which
// counts the board changes the scheduler reports.
type boardEnv struct {
	*env
	notified atomic.Int32
	failRun  func(data string) bool // fails a run record write when true
	mu       sync.Mutex
}

func newBoardEnv(t *testing.T, mode string) *boardEnv {
	e := &boardEnv{env: newEnv(t, mode, utc("2026-11-16T08:59:00Z"))}
	e.reopen(Config{
		BoardChanged: func(string) { e.notified.Add(1) },
		writeRun: func(_ string, data []byte) error {
			e.mu.Lock()
			fail := e.failRun
			e.mu.Unlock()
			if fail != nil && fail(string(data)) {
				return errors.New("the app is gone")
			}
			return nil
		},
	})
	return e
}

// obstruct makes the board's next writes fail, as a full disk would; the
// board still reads.
func (e *boardEnv) obstruct() func() {
	e.t.Helper()
	tmp := mustGet(e.t, e.env).BoardFile() + ".tmp"
	if err := os.Mkdir(tmp, 0o700); err != nil {
		e.t.Fatal(err)
	}
	return func() {
		if err := os.Remove(tmp); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *boardEnv) task(run string) yesmen.Task {
	e.t.Helper()
	tasks := e.scheduledTasks(run)
	if len(tasks) != 1 {
		e.t.Fatalf("%d board tasks for %s", len(tasks), run)
	}
	return tasks[0]
}

func (e *boardEnv) pending(id string) bool {
	e.t.Helper()
	data, err := os.ReadFile(e.runPath(id))
	if err != nil {
		e.t.Fatal(err)
	}
	return strings.Contains(string(data), `"boardSync"`)
}

// decidedAt is when the run's pending board change was decided.
func (e *boardEnv) decidedAt(id string) time.Time {
	e.t.Helper()
	data, err := os.ReadFile(e.runPath(id))
	if err != nil {
		e.t.Fatal(err)
	}
	var r Run
	if err := json.Unmarshal(data, &r); err != nil || r.BoardSync == nil {
		e.t.Fatalf("no pending change: %v", err)
	}
	return r.BoardSync.At
}

// failedRun makes the 09:00 run fail (the fake pi refuses it), the board
// writes failing from then on until the returned function is called.
func (e *boardEnv) failedRun() (string, func()) {
	e.t.Helper()
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	id := "s1-20261116t0900"
	var restore func()
	e.reopen(Config{
		BoardChanged: func(string) { e.notified.Add(1) },
		afterBoard: func(r Run) error {
			if r.ID == id && restore == nil {
				restore = e.obstruct()
			}
			return nil
		},
	})
	e.clock.Add(time.Minute)
	e.tick()
	if r := e.run(id); r.State != StateFailed || !e.pending(id) {
		e.t.Fatalf("the run: %+v, pending %v", r, e.pending(id))
	}
	if task := e.task(id); task.Status != yesmen.TaskTodo {
		e.t.Fatalf("the board was written: %+v", task)
	}
	return id, restore
}

// The board write failed: once the board can be written, after a restart,
// the task is blocked, once, and the change is forgotten.
func TestBoardChangeIsMadeAfterTheBoardWriteFailed(t *testing.T) {
	e := newBoardEnv(t, "reject")
	id, restore := e.failedRun()
	e.tick() // still obstructed: tried, kept
	if !e.pending(id) || e.task(id).Status != yesmen.TaskTodo {
		t.Fatal("given up while the board could not be written")
	}
	restore()
	e.restart("reject")
	e.notified.Store(0)
	e.reopen(Config{BoardChanged: func(string) { e.notified.Add(1) }})
	e.tick()
	task := e.task(id)
	if task.Status != yesmen.TaskBlocked || !strings.HasPrefix(task.Note, notDelivered) || e.pending(id) {
		t.Fatalf("after the restart: %+v, pending %v", task, e.pending(id))
	}
	at := task.UpdatedAt
	e.tick()
	e.tick()
	if again := e.task(id); !again.UpdatedAt.Equal(at) || e.notified.Load() != 1 {
		t.Fatalf("made again: %+v, %d notices", again, e.notified.Load())
	}
	if r := e.run(id); r.BoardSync != nil {
		t.Fatal("the view shows the scheduler's bookkeeping")
	}
}

// The board was written but the app went away before the run forgot the
// change: the next start finds it made, and neither writes the board nor
// tells of a change again.
func TestBoardChangeMadeBeforeAQuitIsNotMadeAgain(t *testing.T) {
	e := newBoardEnv(t, "reject")
	e.mu.Lock()
	e.failRun = func(data string) bool {
		return strings.Contains(data, `"state": "failed"`) && !strings.Contains(data, `"boardSync"`)
	}
	e.mu.Unlock()
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Add(time.Minute)
	e.tick()
	id := "s1-20261116t0900"
	task := e.task(id)
	if task.Status != yesmen.TaskBlocked || !e.pending(id) {
		t.Fatalf("before the restart: %+v, pending %v", task, e.pending(id))
	}
	e.restart("reject")
	e.notified.Store(0)
	e.reopen(Config{BoardChanged: func(string) { e.notified.Add(1) }})
	e.tick()
	if again := e.task(id); !again.UpdatedAt.Equal(task.UpdatedAt) || again.Note != task.Note || e.notified.Load() != 0 || e.pending(id) {
		t.Fatalf("after the restart: %+v, %d notices, pending %v", again, e.notified.Load(), e.pending(id))
	}
}

// A task the lead or the user changed, or removed, while the change waited
// is left as they left it, and the change is forgotten.
func TestBoardChangeLeavesATaskChangedMeanwhile(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(e *boardEnv, task yesmen.Task)
		check  func(t *testing.T, e *boardEnv, id string)
	}{
		{"taken up", func(e *boardEnv, task yesmen.Task) {
			status, note := yesmen.TaskDoing, "I sent it myself"
			if _, err := e.store.ChangeBoard(ws, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: task.ID, Status: &status, Note: &note}}}); err != nil {
				e.t.Fatal(err)
			}
		}, func(t *testing.T, e *boardEnv, id string) {
			if task := e.task(id); task.Status != yesmen.TaskDoing || task.Note != "I sent it myself" {
				t.Fatalf("the edit was undone: %+v", task)
			}
		}},
		{"still to do, owner set", func(e *boardEnv, task yesmen.Task) {
			owner := "lead"
			if _, err := e.store.ChangeBoard(ws, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: task.ID, Owner: &owner}}}); err != nil {
				e.t.Fatal(err)
			}
		}, func(t *testing.T, e *boardEnv, id string) {
			if task := e.task(id); task.Status != yesmen.TaskTodo || task.Owner != "lead" {
				t.Fatalf("the edited task was changed: %+v", task)
			}
		}},
		{"removed", func(e *boardEnv, task yesmen.Task) {
			if _, err := e.store.ChangeBoard(ws, yesmen.BoardChange{Remove: []string{task.ID}}); err != nil {
				e.t.Fatal(err)
			}
		}, func(t *testing.T, e *boardEnv, id string) {
			if n := len(e.scheduledTasks(id)); n != 0 {
				t.Fatalf("%d tasks", n)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newBoardEnv(t, "reject")
			id, restore := e.failedRun()
			restore()
			c.change(e, e.task(id))
			e.restart("reject")
			e.tick()
			c.check(t, e, id)
			if e.pending(id) {
				t.Fatal("the change is still kept")
			}
		})
	}
}

// Sent again: the task blocked by the scheduler goes back to do, also when
// the board could not be written when it was sent; one the lead dropped
// meanwhile stays dropped.
func TestBoardChangeBackToDoWhenSentAgain(t *testing.T) {
	for _, dropped := range []bool{false, true} {
		t.Run(map[bool]string{false: "kept", true: "dropped"}[dropped], func(t *testing.T) {
			e := newBoardEnv(t, "reject")
			e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
			e.clock.Add(time.Minute)
			e.tick()
			id := "s1-20261116t0900"
			if task := e.task(id); task.Status != yesmen.TaskBlocked {
				t.Fatalf("not blocked: %+v", task)
			}
			e.restart("")
			restore := e.obstruct()
			if _, err := e.svc.Retry(ws, id); err != nil {
				t.Fatal(err)
			}
			e.svc.Wait()
			if !e.pending(id) || e.task(id).Status != yesmen.TaskBlocked {
				t.Fatalf("the change was made through an obstructed board: %+v", e.task(id))
			}
			restore()
			if dropped {
				status := yesmen.TaskDropped
				if _, err := e.store.ChangeBoard(ws, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: e.task(id).ID, Status: &status}}}); err != nil {
					t.Fatal(err)
				}
			}
			e.tick()
			task := e.task(id)
			want := yesmen.TaskTodo
			if dropped {
				want = yesmen.TaskDropped
			}
			if task.Status != want || !dropped && task.Note != "" || e.pending(id) {
				t.Fatalf("after the tick: %+v, pending %v", task, e.pending(id))
			}
		})
	}
}

// When the board could not even be read as the run failed, the change is
// made only to a task still to do and not changed after it was decided.
func TestBoardChangeDecidedWithoutTheBoard(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "edited"}[edited], func(t *testing.T) {
			e := newBoardEnv(t, "reject")
			e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
			id := "s1-20261116t0900"
			path := mustGet(t, e.env).BoardFile()
			var saved []byte
			e.reopen(Config{afterBoard: func(r Run) error {
				if r.ID == id && saved == nil {
					saved, _ = os.ReadFile(path)
					if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}})
			e.clock.Add(time.Minute)
			e.tick()
			if !e.pending(id) {
				t.Fatal("no change kept")
			}
			if err := os.WriteFile(path, saved, 0o644); err != nil {
				t.Fatal(err)
			}
			if edited {
				// Edited after the change was decided, by the board's clock.
				for decided := e.decidedAt(id); !time.Now().After(decided); {
					time.Sleep(time.Millisecond)
				}
				title := "Edited title"
				if _, err := e.store.ChangeBoard(ws, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: e.task(id).ID, Title: &title}}}); err != nil {
					t.Fatal(err)
				}
			}
			e.restart("reject")
			e.tick()
			task := e.task(id)
			want := yesmen.TaskBlocked
			if edited {
				want = yesmen.TaskTodo
			}
			if task.Status != want || e.pending(id) {
				t.Fatalf("%+v, pending %v", task, e.pending(id))
			}
		})
	}
}
