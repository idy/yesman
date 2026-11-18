package agent

import (
	"context"
	"testing"
	"time"

	"github.com/idy/yesman/internal/restart"
	"github.com/idy/yesman/internal/yesmen"
)

func TestRestartCompositionAmbiguousNoticeIsReservedNotDelivered(t *testing.T) {
	store, tmp, y := restartStore(t)
	addTasks(t, store, yesmen.TaskEdit{Title: str("interrupted goal")})
	m, got := restartInstanceEnv(t, store, tmp, "FAKE_PI_HOLD_PROMPTS=1")
	finish := m.HoldContinuations()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if o, err := m.ResumeAfterRestart(ctx, restartLead(y), "ambiguous restart note"); o != restart.Uncertain || err == nil {
		t.Fatalf("got %s %v", o, err)
	}
	if got.notes("alice/lead", "ambiguous restart note") != 1 {
		t.Fatal("fixture did not actually receive the note")
	}
	c, err := store.Continuation("alice")
	if err != nil || !c.Tasks["t1"].RestartReserved || c.Tasks["t1"].Delivered || len(c.Attempts) != 1 {
		t.Fatalf("ambiguous notice misrepresented: %+v %v", c, err)
	}
	finish()
	m.StopAll()
	next, after := restartInstance(t, store, tmp)
	next.Resume()
	quiet(t, after.to, "alice/lead", 0)
	if next.running("alice/lead") != nil {
		t.Fatal("reopen replayed ambiguous work")
	}
}
