package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/restart"
	"github.com/idy/yesman/internal/yesmen"
)

const repairNote = "[yesman] Yes Man restarted while you were working. Continue from your session."

func repairEntry(t *testing.T, store *yesmen.Store, y yesmen.Yesman, role, reason string) restart.Agent {
	t.Helper()
	r := restartLead(y)
	if role == RoleWorker {
		w := addWorker(t, store, "worker")
		r.Name, r.Role, r.AgentSince = w.Name, role, w.StartedAt
	}
	r.Reason = reason
	if reason == restart.ReasonReport {
		r.Report = &restart.Report{State: restart.ReportNotSent, Digest: digest("done")}
	}
	return r
}

func TestF48MessagesLeavesRecoveryEligible(t *testing.T) {
	for _, role := range []string{RoleLead, RoleWorker} {
		for _, reason := range []string{restart.ReasonWorking, restart.ReasonNeedsYou, restart.ReasonReport} {
			if role == RoleLead && reason == restart.ReasonReport {
				continue
			}
			t.Run(role+"/"+reason, func(t *testing.T) {
				store, tmp, y := restartStore(t)
				r := repairEntry(t, store, y, role, reason)
				m, got := restartInstance(t, store, tmp)
				finish := m.HoldContinuations()
				defer finish()
				id := ID(r.Yesman, r.Name)
				if _, err := m.Messages(context.Background(), id); err != nil {
					t.Fatal(err)
				}
				observed := m.running(id)
				want := restart.Resumed
				if reason == restart.ReasonNeedsYou {
					want = restart.Waiting
				}
				if reason == restart.ReasonReport {
					want = restart.Reported
				}
				if o, err := m.ResumeAfterRestart(context.Background(), r, repairNote); o != want || err != nil {
					t.Fatalf("got %s %v, want %s", o, err, want)
				}
				if reason == restart.ReasonWorking && got.notes(id, repairNote) != 1 {
					t.Fatal("history hid the recovery note")
				}
				if role == RoleLead && reason == restart.ReasonWorking && m.running(id) != observed {
					t.Fatal("recovery replaced the observed process")
				}
				if reason == restart.ReasonReport && (len(got.to(id)) != 0 || len(got.reports("alice/lead", r.Name)) != 1) {
					t.Fatal("report was lost or a new worker turn ran")
				}
				if reason == restart.ReasonNeedsYou {
					if len(got.to(id)) != 0 {
						t.Fatal("human wait received work")
					}
					m.StopAll()
					next, after := restartInstance(t, store, tmp)
					if o, _ := next.ResumeAfterRestart(context.Background(), r, repairNote); o != restart.Stopped {
						t.Fatalf("human wait lost across open: %s", o)
					}
					if len(after.to(id)) != 0 {
						t.Fatal("reopen woke human wait")
					}
				}
			})
		}
	}
}

// The production Messages call owns a slow startup. Recovery joins that same
// start, while actual new input or Stop still preempts it.
func TestF48HistoryStartupAndIncomingWork(t *testing.T) {
	for _, action := range []string{"observe", "input", "stop"} {
		for _, role := range []string{RoleLead, RoleWorker} {
			t.Run(role+"/"+action, func(t *testing.T) {
				store, tmp, y := restartStore(t)
				r := repairEntry(t, store, y, role, restart.ReasonWorking)
				m, got := restartInstance(t, store, tmp)
				finish := m.HoldContinuations()
				defer finish()
				entered, release := make(chan struct{}), make(chan struct{})
				var starts atomic.Int32
				var unblock sync.Once
				t.Cleanup(func() { unblock.Do(func() { close(release) }) })
				m.cfg.Command = func(ctx context.Context) (string, []string, error) {
					if starts.Add(1) == 1 {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
							return "", nil, ctx.Err()
						}
					}
					return m.cfg.Pi, nil, nil
				}
				id := ID(r.Yesman, r.Name)
				read := make(chan error, 1)
				go func() { _, err := m.Messages(context.Background(), id); read <- err }()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("history did not start")
				}
				var input chan error
				if action == "input" {
					input = make(chan error, 1)
					go func() { input <- m.PromptFromLead(context.Background(), id, "new incoming work") }()
					eventually(t, "incoming work before startup", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.restartHasInputLocked(r) })
				} else if action == "stop" {
					m.StopByUser(id)
				}
				result := make(chan string, 1)
				go func() { o, _ := m.ResumeAfterRestart(context.Background(), r, repairNote); result <- o }()
				unblock.Do(func() { close(release) })
				var o string
				select {
				case o = <-result:
				case <-time.After(3 * time.Second):
					t.Fatal("recovery did not end")
				}
				want := restart.Resumed
				if action == "input" {
					want = restart.Active
				}
				if action == "stop" {
					want = restart.Stopped
				}
				if o != want {
					t.Fatalf("got %s, want %s", o, want)
				}
				select {
				case err := <-read:
					if action != "stop" && err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("history did not end")
				}
				if input != nil {
					select {
					case err := <-input:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("input did not end")
					}
				}
				if starts.Load() != 1 {
					t.Fatalf("started %d processes", starts.Load())
				}
				if action != "observe" && got.notes(id, repairNote) != 0 {
					t.Fatal("recovery overrode input or Stop")
				}
			})
		}
	}
}

func TestF48RefusalHoldAndExplicitInput(t *testing.T) {
	for _, timing := range []string{"ordinary-open", "before-hold", "during-persist", "after-hold"} {
		t.Run(timing, func(t *testing.T) {
			store, tmp, y := restartStore(t)
			addTasks(t, store, yesmen.TaskEdit{Title: str("unfinished goal")})
			m, got := restartInstanceEnv(t, store, tmp, "FAKE_PI_FAIL_RESTARTS=1")
			finish := m.HoldContinuations()
			defer finish()
			r := restartLead(y)
			if o, err := m.ResumeAfterRestart(context.Background(), r, repairNote); o != restart.Failed || err == nil {
				t.Fatalf("got %s %v", o, err)
			}
			c, _ := store.Continuation("alice")
			if len(c.Tasks) != 0 || len(c.Attempts) != 0 || got.notes("alice/lead", repairNote) != 0 {
				t.Fatal("refused note left a reservation or accepted input")
			}
			if timing == "before-hold" {
				if err := m.Prompt(context.Background(), "alice/lead", "new user work"); err != nil {
					t.Fatal(err)
				}
				// Even closing the process must not forget genuine incoming work.
				m.Stop("alice/lead")
				if err := m.HoldAfterRestart(r); err == nil {
					t.Fatal("late hold displaced user work")
				}
			} else if timing == "during-persist" {
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				t.Cleanup(func() { once.Do(func() { close(release) }) })
				m.reviewBeforePersistPause = func(paused bool) {
					if paused {
						close(entered)
						<-release
					}
				}
				held := make(chan error, 1)
				go func() { held <- m.HoldAfterRestart(r) }()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("hold did not reach persistence")
				}
				if err := m.Prompt(context.Background(), "alice/lead", "new user work"); err != nil {
					t.Fatal(err)
				}
				once.Do(func() { close(release) })
				select {
				case err := <-held:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("hold did not finish")
				}
			} else {
				if err := m.HoldAfterRestart(r); err != nil {
					t.Fatal(err)
				}
				c, _ = store.Continuation("alice")
				if c.LeadPausedAt.IsZero() {
					t.Fatal("failed recovery hold missing")
				}
				finish()
				if timing == "ordinary-open" {
					m.StopAll()
					next, after := restartInstance(t, store, tmp)
					next.continueTasks("alice")
					if next.running("alice/lead") != nil || len(after.to("alice/lead")) != 0 {
						t.Fatal("ordinary reopen resumed refused recovery")
					}
					return
				}
				if err := m.Prompt(context.Background(), "alice/lead", "new user work"); err != nil {
					t.Fatal(err)
				}
			}
			c, _ = store.Continuation("alice")
			if !c.LeadPausedAt.IsZero() || got.notes("alice/lead", "new user work") != 1 {
				t.Fatal("explicit input did not keep ownership")
			}
		})
	}
}

func TestF48UncertainHoldDoesNotReplay(t *testing.T) {
	store, tmp, y := restartStore(t)
	addTasks(t, store, yesmen.TaskEdit{Title: str("unfinished goal")})
	m, got := restartInstanceEnv(t, store, tmp, "FAKE_PI_HOLD_PROMPTS=1")
	finish := m.HoldContinuations()
	defer finish()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	r := restartLead(y)
	if o, err := m.ResumeAfterRestart(ctx, r, repairNote); o != restart.Uncertain || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %s %v", o, err)
	}
	if got.notes("alice/lead", repairNote) != 1 {
		t.Fatal("ambiguous note never reached fake pi")
	}
	if err := m.HoldAfterRestart(r); err != nil {
		t.Fatal(err)
	}
	c, _ := store.Continuation("alice")
	if c.LeadPausedAt.IsZero() || !c.Tasks["t1"].RestartReserved || c.Tasks["t1"].Delivered {
		t.Fatalf("wrong uncertain hold/ledger %+v", c)
	}
	finish()
	m.StopAll()
	next, after := restartInstance(t, store, tmp)
	next.continueTasks("alice")
	if next.running("alice/lead") != nil || len(after.to("alice/lead")) != 0 {
		t.Fatal("reopen replayed uncertain input")
	}
}
