package schedule

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// gate holds the first write of a run record that matches, until released.
type gate struct {
	match    string
	once     sync.Once
	entered  chan struct{}
	released chan struct{}
	armed    bool
	mu       sync.Mutex
}

func newGate(match string) *gate {
	return &gate{match: match, entered: make(chan struct{}), released: make(chan struct{})}
}

func (g *gate) arm() { g.mu.Lock(); g.armed = true; g.mu.Unlock() }

func (g *gate) write(_ string, data []byte) error {
	g.mu.Lock()
	armed := g.armed
	g.mu.Unlock()
	if armed && strings.Contains(string(data), g.match) {
		g.once.Do(func() { close(g.entered); <-g.released })
	}
	return nil
}

// Close keeps the lock while a check of a run's receipt may still write
// its record, as it does for a delivery: another app cannot take the tasks
// over and then have its newer record written over. Close itself returns
// after its short wait, as often and from as many goroutines as called; no
// operation starts after it, and other workspaces were never held up. Once
// the check ended the lock goes, and the app that takes over keeps what it
// writes.
func TestCloseKeepsTheLockWhileACheckWrites(t *testing.T) {
	e := newEnv(t, "busy", utc("2026-11-16T08:59:00Z"))
	g := newGate(`"state": "unknown"`)
	e.reopen(Config{writeRun: g.write})
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Add(time.Minute)
	e.tick()
	id := "s1-20261116t0900"
	e.waitPrompts(1)
	// Its lead goes with the queue it waited in: a check makes it unknown.
	e.agents.Stop("alice/lead")
	g.arm()
	checked := make(chan error, 1)
	go func() { _, err := e.svc.Check(ws, id); checked <- err }()
	<-g.entered

	// Another workspace is not held up by the check under way.
	if _, err := e.store.Hire("bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Save("bob", PlanInput{Title: "Bob's", Instructions: "x", Schedule: daily("10:00"), Timezone: "UTC"}); err != nil {
		t.Fatalf("bob was held up: %v", err)
	}

	start := time.Now()
	closed := make(chan time.Duration, 4)
	for range 4 {
		go func() { s := time.Now(); e.svc.Close(); closed <- time.Since(s) }()
	}
	for range 4 {
		if d := <-closed; d > closeWait+time.Second {
			t.Fatalf("Close waited %v", d)
		}
	}
	if waited := time.Since(start); waited < closeWait-100*time.Millisecond {
		t.Fatalf("Close returned after %v with a check under way", waited)
	}
	if !locked(t, e.cfg.LockPath) {
		t.Fatal("the lock was let go while a check may still write")
	}
	other := New(Config{Yesmen: yesmen.New(filepath.Join(e.root, "yesmen"), filepath.Join(e.root, "archive")), Agents: e.agents, Now: e.clock.Now, LockPath: e.cfg.LockPath})
	defer other.Close()
	if other.Owner() {
		t.Fatal("another scheduler took the tasks over while a check may still write")
	}
	// Nothing new starts on the closed scheduler.
	if _, err := e.svc.Check(ws, id); !errors.Is(err, ErrClosed) {
		t.Fatalf("check after Close: %v", err)
	}
	if _, err := e.svc.RunNow(ws, "s1", e.requestID(), ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("run after Close: %v", err)
	}
	if _, err := e.svc.Save(ws, PlanInput{Title: "T", Instructions: "x", Schedule: daily("10:00"), Timezone: "UTC"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("save after Close: %v", err)
	}
	if next := e.svc.Tick(t0ctx()); !next.IsZero() {
		t.Fatalf("a tick after Close: %v", next)
	}

	// The check ends, writing its record; then the lock goes.
	close(g.released)
	if err := <-checked; err != nil {
		t.Fatalf("the check: %v", err)
	}
	if r := readRun(t, e, id); r.State != StateUnknown {
		t.Fatalf("the check's record: %+v", r)
	}
	start = time.Now()
	e.svc.Close()
	if waited := time.Since(start); waited > closeWait/2 || locked(t, e.cfg.LockPath) {
		t.Fatalf("the lock was not let go once the check ended (closing again took %v)", waited)
	}

	// The other app takes over and writes; the old one writes nothing more.
	if !other.Owner() {
		t.Fatal("the lock was not taken over")
	}
	note, status := "new owner outcome", yesmen.TaskDoing
	if _, err := e.store.ChangeBoard(ws, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: e.scheduledTasks(id)[0].ID, Status: &status, Note: &note}}}); err != nil {
		t.Fatal(err)
	}
	other.Tick(t0ctx())
	other.Wait()
	after, _ := os.ReadFile(e.runPath(id))
	if !bytes.Contains(after, []byte(note)) {
		t.Fatalf("the new owner's record: %s", after)
	}
	if _, err := e.svc.Check(ws, id); !errors.Is(err, ErrClosed) {
		t.Fatalf("check on the old scheduler: %v", err)
	}
	if again, _ := os.ReadFile(e.runPath(id)); !bytes.Equal(again, after) {
		t.Fatal("the old scheduler wrote over the new owner's record")
	}
}

func readRun(t *testing.T, e *env, id string) Run {
	t.Helper()
	data, err := readIn(mustRuns(t, e), id+".json", maxRunFile)
	if err != nil || data == nil {
		t.Fatalf("%s: %v", id, err)
	}
	return readRecordIn(mustRuns(t, e), id).Run
}

func mustRuns(t *testing.T, e *env) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules", "runs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// A background check held up reading a file (a slow disk, a file that does
// not answer) holds up Close only for its short wait, and the lock stays
// until the check ended: another app does not take the tasks over
// meanwhile. The check claims nothing once Close was called, so the run
// due is left to whoever runs the tasks next, which claims and sends it.
func TestCloseDoesNotWaitLongForAHeldBackgroundCheck(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Add(time.Minute)
	e.restart("")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	beforeOpenRead = func(name string) {
		if name == "yesman.json" {
			once.Do(func() { close(entered); <-release })
		}
	}
	defer func() { beforeOpenRead = nil }()
	e.svc.Start()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the background check never read the workspace")
	}

	start := time.Now()
	closed := make(chan time.Duration, 4)
	for range 4 {
		go func() { s := time.Now(); e.svc.Close(); closed <- time.Since(s) }()
	}
	for range 4 {
		select {
		case d := <-closed:
			if d > closeWait+time.Second {
				t.Errorf("Close waited %v", d)
			}
		case <-time.After(closeWait + 2*time.Second):
			close(release)
			t.Fatal("Close waited for the background check held up")
		}
	}
	if waited := time.Since(start); waited < closeWait-100*time.Millisecond {
		t.Fatalf("Close returned after %v with the background check under way", waited)
	}
	if !locked(t, e.cfg.LockPath) {
		t.Fatal("the lock was let go while the background check may still write")
	}
	other := New(Config{Yesmen: yesmen.New(filepath.Join(e.root, "yesmen"), filepath.Join(e.root, "archive")), Agents: e.agents, Now: e.clock.Now, LockPath: e.cfg.LockPath})
	defer other.Close()
	if other.Owner() {
		t.Fatal("another scheduler took the tasks over while the background check may still write")
	}

	// The check goes on, claims nothing, and ends; then the lock goes.
	close(release)
	start = time.Now()
	e.svc.Close()
	if waited := time.Since(start); waited > closeWait/2 || locked(t, e.cfg.LockPath) {
		t.Fatalf("the lock was not let go once the check ended (closing again took %v)", waited)
	}
	if files := e.runFiles(); len(files) != 0 {
		t.Fatalf("claimed after Close: %v", files)
	}
	if !other.Owner() {
		t.Fatal("the lock was not taken over")
	}
	other.Tick(t0ctx())
	other.Wait()
	if r := readRun(t, e, "s1-20261116t0900"); r.Kind != RunScheduled {
		t.Fatalf("the run due, by the next scheduler: %+v", r)
	}
	e.waitPrompts(1)
}
