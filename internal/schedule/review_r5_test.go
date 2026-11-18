package schedule

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// The review of 443af43 (t87 R5) found these two; both tests are its own,
// kept as they were written apart from formatting.

// R5-F1: a tick held after it read the time, while a manual run commits its
// real board task and prompt, must not count that task removed, nor start
// the plan's daily run beside it.
func TestR5BoardSnapshotDoesNotInventRemoval(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	p := e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Set(utc("2026-11-16T09:00:01Z"))
	reached, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	e.svc.cfg.Now = func() time.Time {
		if armed.CompareAndSwap(true, false) {
			close(reached)
			<-release
		}
		return e.clock.Now()
	}
	done := make(chan struct{})
	go func() { e.svc.Tick(t0ctx()); close(done) }()
	<-reached
	r, err := e.svc.RunNow(ws, p.ID, e.requestID(), "")
	if err != nil {
		close(release)
		<-done
		t.Fatal(err)
	}
	e.svc.Wait() // real Board + receipt + deterministic RPC have committed
	before := e.board()
	t.Logf("before releasing the held tick: board=%+v prompts=%d", before.Tasks, len(e.prompts()))
	close(release)
	<-done
	e.svc.Wait()
	got := e.run(r.ID)
	e.tick()
	after := e.run(r.ID)
	t.Logf("after the held tick: outcome=%+v; after a fresh tick=%+v; board tasks=%d prompts=%d", got.Outcome, after.Outcome, len(e.board().Tasks), len(e.prompts()))
	if got.Outcome != nil && got.Outcome.Removed {
		t.Error("existing task permanently recorded removed")
	}
	if len(e.prompts()) != 1 {
		t.Errorf("open real Board task did not prevent overlap: prompts=%d want 1", len(e.prompts()))
	}
}

// R5-F2: a pruning ledger repaired on disk is used without a restart.
func TestR5RepairLedgerRecoversWithoutRestart(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	p := e.save(PlanInput{Schedule: daily("09:00")})
	path := filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules", "pruned.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	e.restart("")
	_, err := e.svc.RunNow(ws, p.ID, e.requestID(), "")
	if err == nil {
		t.Fatal("corrupt ledger admitted run")
	}
	t.Logf("corrupt refusal=%v", err)
	if err := os.WriteFile(path, []byte(`{"version":1,"plans":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	e.view()
	e.tick()
	_, err = e.svc.RunNow(ws, p.ID, e.requestID(), "")
	t.Logf("after repair, refresh, tick: %v", err)
	if err != nil {
		t.Errorf("repaired ledger remains cached unreadable: %v", err)
	}
	e.svc.Wait()
	if n := len(e.prompts()); n != 1 {
		t.Errorf("the run admitted after the repair sent %d prompts, want 1", n)
	}
	// The review's restart control, after the run above was sent: a restart
	// reads the same repaired ledger and admits the user's next run.
	e.restart("")
	_, err = e.svc.RunNow(ws, p.ID, e.requestID(), "")
	if err != nil {
		t.Errorf("restart control: %v", err)
	}
	e.svc.Wait()
	t.Logf("restart control prompts=%d", len(e.prompts()))
	if n := len(e.prompts()); n != 2 {
		t.Errorf("after a restart %d prompts, want 2", n)
	}
}

// A run recorded removed follows its task again once the board has it
// back, as a board restored from a copy does: its task is open again, so
// the plan's next run waits for it.
func TestRemovedRunFollowsItsRestoredTask(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	p := e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	r, err := e.svc.RunNow(ws, p.ID, e.requestID(), "")
	if err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	y, err := e.store.Get(ws)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(y.BoardFile())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.ChangeBoard(ws, yesmen.BoardChange{Remove: []string{e.run(r.ID).Board}}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if o := e.run(r.ID).Outcome; o == nil || !o.Removed {
		t.Fatalf("outcome after its task was removed: %+v", o)
	}
	if err := os.WriteFile(y.BoardFile(), saved, 0o600); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if o := e.run(r.ID).Outcome; o == nil || o.Removed || o.Status != "todo" {
		t.Fatalf("outcome after its task was restored: %+v", o)
	}
	e.clock.Set(utc("2026-11-16T09:00:01Z"))
	e.tick()
	e.svc.Wait()
	if n := len(e.prompts()); n != 1 {
		t.Fatalf("the daily run went beside the restored open task: %d prompts", n)
	}
}
