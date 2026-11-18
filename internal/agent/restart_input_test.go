package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/restart"
)

// A real message is admitted while the restart note waits just before its
// pipe write. The same input marker must stop the old note and its late hold.
func TestF48InputDuringRecoveryWins(t *testing.T) {
	for _, role := range []string{RoleLead, RoleWorker} {
		t.Run(role, func(t *testing.T) {
			store, tmp, y := restartStore(t)
			r := repairEntry(t, store, y, role, restart.ReasonWorking)
			m, got := restartInstance(t, store, tmp)
			finish := m.HoldContinuations()
			defer finish()
			id := ID(r.Yesman, r.Name)
			// Install the test barrier before Messages can schedule its worker
			// close check. The normal Messages path has separate recovery tests.
			_, p, err := m.ensure(id)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once, unblock sync.Once
			t.Cleanup(func() { unblock.Do(func() { close(release) }) })
			p.reviewBeforeWrite = func(v any) {
				if cmd, ok := v.(map[string]any); ok && cmd["type"] == "prompt" && cmd["message"] == repairNote {
					once.Do(func() { close(entered); <-release })
				}
			}
			done := make(chan string, 1)
			go func() { o, _ := m.ResumeAfterRestart(context.Background(), r, repairNote); done <- o }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("restart did not reach admission")
			}
			if role == RoleLead {
				err = m.Prompt(context.Background(), id, "new user work")
			} else {
				err = m.PromptFromLead(context.Background(), id, "new lead work")
			}
			if err != nil {
				t.Fatal(err)
			}
			unblock.Do(func() { close(release) })
			select {
			case o := <-done:
				if o == restart.Resumed {
					t.Fatal("old recovery resumed")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("recovery did not finish")
			}
			if got.notes(id, repairNote) != 0 || len(got.to(id)) != 1 {
				t.Fatalf("wrong inputs %q", got.to(id))
			}
			if err := m.HoldAfterRestart(r); err == nil {
				t.Fatal("late hold displaced new input")
			}
			if role == RoleLead {
				c, _ := store.Continuation("alice")
				if !c.LeadPausedAt.IsZero() {
					t.Fatal("new user input paused")
				}
			} else {
				ws, _ := store.Workers("alice")
				if !ws[0].PausedAt.IsZero() {
					t.Fatal("new lead input paused")
				}
			}
		})
	}
}

func TestF48AnswerWinsOverRecoveryHold(t *testing.T) {
	store, tmp, y := restartStore(t)
	r := repairEntry(t, store, y, RoleWorker, restart.ReasonWorking)
	m, _ := restartInstanceEnv(t, store, tmp, "FAKE_PI_ASK_WORKERS=1")
	finish := m.HoldContinuations()
	defer finish()
	id := ID(r.Yesman, r.Name)
	if o, err := m.ResumeAfterRestart(context.Background(), r, repairNote); o != restart.Resumed || err != nil {
		t.Fatalf("got %s %v", o, err)
	}
	waitStatus(t, m, id, Waiting)
	if err := m.Answer(id, "q1", map[string]any{"value": "Allow"}); err != nil {
		t.Fatal(err)
	}
	if err := m.HoldAfterRestart(r); err == nil {
		t.Fatal("late hold displaced answer")
	}
	ws, _ := store.Workers("alice")
	if !ws[0].PausedAt.IsZero() {
		t.Fatal("answered worker paused")
	}
}
