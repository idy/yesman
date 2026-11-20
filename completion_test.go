package main

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

func TestCompletionWaitsForUninterruptedQuiet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var notices completionNotices
		got := make(chan agent.Info, 10)
		notify := func(_ context.Context, i agent.Info) { got <- i }
		lead := agent.Info{ID: "alice/lead", Yesman: "alice", Role: agent.RoleLead, Status: agent.Working}
		list := []agent.Info{lead}
		notices.update(list, notify)
		list[0].Status = agent.Idle
		notices.update(list, notify)
		time.Sleep(3 * time.Second)

		// Work resumes before the old timer fires, then ends again.
		list[0].Status = agent.Working
		notices.update(list, notify)
		time.Sleep(500 * time.Millisecond)
		list[0].Status = agent.Idle
		notices.update(list, notify)
		time.Sleep(time.Second)
		synctest.Wait()
		if len(got) != 0 {
			t.Fatal("the first idle period announced completion during the new grace interval")
		}
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if len(got) != 1 {
			t.Fatalf("completion notification count = %d", len(got))
		}
		if i := <-got; i.ID != lead.ID {
			t.Fatalf("completion notification = %+v", i)
		}
		// A repeated idle snapshot must not schedule another notice.
		notices.update(list, notify)
		time.Sleep(settleGrace)
		synctest.Wait()
		if len(got) != 0 {
			t.Fatal("completion was announced twice")
		}
	})
}

type noticeFixture struct {
	*app
	workspace yesmen.Yesman
	lead      string
	settled   chan struct{}
}

const (
	failedTurn    = `ASSISTANT {"role":"assistant","stopReason":"error","errorMessage":"fixture connection refused"}`
	recoveredTurn = `ASSISTANT {"role":"assistant","stopReason":"stop"}`
)

func newNoticeFixture(t *testing.T) *noticeFixture {
	t.Helper()
	a, _ := crewApp(t)
	workspace, err := a.yesmen.HireNamed("Test Project", "")
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	id := agent.ID(workspace.Name, "lead")
	settled := make(chan struct{}, 4)
	a.agents = agent.New(agent.Config{Yesmen: a.yesmen, Pi: exe,
		Env: []string{"YESMAN_NEEDS_YOU_FAKE_PI=1", "GORACE=atexit_sleep_ms=0"},
		OnEvent: func(e agent.Event) {
			var event struct{ Type string }
			if e.Agent == id && json.Unmarshal(e.Data, &event) == nil && event.Type == "agent_settled" {
				settled <- struct{}{}
			}
		},
	})
	t.Cleanup(a.agents.StopAll)
	a.keepAwake = func(string) func() { return func() {} }
	return &noticeFixture{app: a, workspace: workspace, lead: id, settled: settled}
}

func (f *noticeFixture) waitForLead(t *testing.T) {
	t.Helper()
	select {
	case <-f.settled:
	case <-time.After(5 * time.Second):
		t.Fatal("scripted Lead did not settle")
	}
}

func (f *noticeFixture) finish(t *testing.T, message string) []agent.Info {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := f.agents.Prompt(ctx, f.lead, message); err != nil {
		t.Fatal(err)
	}
	f.waitForLead(t)
	return f.agents.List()
}

// Drive the production desktop observer and notification callback. The real
// Manager supplies the live Lead state when the quiet timer expires.
func TestDesktopNotificationAfterModelError(t *testing.T) {
	a := newNoticeFixture(t)
	if _, err := a.agents.StartWorker(t.Context(), a.workspace.Name, "helper", "First attempt", failedTurn); err != nil {
		t.Fatal(err)
	}
	a.waitForLead(t) // the worker's model error has reached its Lead
	for _, tc := range []struct {
		name, message, body string
	}{
		{"failed", failedTurn, "The model request failed. Open the conversation for details."},
		{"recovered", recoveredTurn, "Finished and waiting for you."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := a.finish(t, tc.message)
			if len(list) != 2 || list[0].Status != agent.Idle || (list[0].RunError != nil) != (tc.message == failedTurn) || list[1].RunError == nil {
				t.Fatalf("unexpected settled team: %+v", list)
			}
			// The Manager owns the run lifetime outside the timer's fake clock.
			_ = list[0].SettledRun().Done()
			synctest.Test(t, func(t *testing.T) {
				var notices []string
				a.notifier = func(_ context.Context, gotID, title, body string) {
					if gotID != a.lead || title != a.workspace.Label() {
						t.Errorf("notification for %q (%q), want %q (%q)", gotID, title, a.lead, a.workspace.Label())
					}
					notices = append(notices, body)
				}
				working := slices.Clone(list)
				working[0].Status = agent.Working
				a.agentsChanged(working)
				// Model retries and report turns can start during the grace period.
				// A coalesced snapshot used to arm the timer can also be stale.
				queued := slices.Clone(list)
				if queued[0].RunError == nil {
					queued[0].RunError = list[1].RunError
				} else {
					queued[0].RunError = nil
				}
				a.agentsChanged(queued)
				time.Sleep(settleGrace / 2)
				a.agentsChanged(working)
				time.Sleep(settleGrace)
				synctest.Wait()
				if len(notices) != 0 {
					t.Fatalf("notified while retrying: %q", notices)
				}
				a.agentsChanged(queued)
				a.agentsChanged(list)
				time.Sleep(settleGrace)
				synctest.Wait()
				a.agentsChanged(list)
				time.Sleep(settleGrace)
				synctest.Wait()
				if !slices.Equal(notices, []string{tc.body}) {
					t.Fatalf("desktop notifications = %q, want one %q", notices, tc.body)
				}
			})
		})
	}
	t.Run("retired hire and stopped process", func(t *testing.T) {
		current := a.agents.List()[0]
		synctest.Test(t, func(t *testing.T) {
			var bodies []string
			a.notifier = func(_ context.Context, _, _, body string) { bodies = append(bodies, body) }
			send := func(lead agent.Info, status agent.Status) {
				lead.Status = status
				a.agentsChanged([]agent.Info{lead})
			}
			retired := current
			retired.Since = current.Since.Add(-time.Second)
			send(retired, agent.Working)
			send(retired, agent.Idle)
			time.Sleep(settleGrace)
			synctest.Wait()
			if len(bodies) != 0 {
				t.Fatalf("old hire notified the new one: %q", bodies)
			}
			// A process failure already has an immediate notification. A
			// pending idle timer, repeated failure and Stop must add none.
			send(current, agent.Working)
			send(current, agent.Idle)
			current.Detail = "fixture process exited"
			send(current, agent.Failed)
			send(current, agent.Failed)
			send(current, agent.Stopped)
			time.Sleep(settleGrace)
			synctest.Wait()
			if !slices.Equal(bodies, []string{current.Detail}) {
				t.Fatalf("process notifications = %q", bodies)
			}
		})
	})
}

// Pause the first notification after its timer has fired. The second run
// finishes on the very same Lead and process. Only its own grace interval
// can authorize delivery. Exercise both the callback and a queued platform
// delivery (e.g. while waiting for notification permission).
func TestExpiredNoticeCannotAnnounceNextRun(t *testing.T) {
	type delivery struct {
		body string
		at   time.Time
	}
	for _, stage := range []string{"before live recheck", "during platform delivery"} {
		t.Run(stage, func(t *testing.T) {
			a := newNoticeFixture(t)
			old := a.finish(t, failedTurn)[0]
			if old.Status != agent.Idle || old.RunError == nil {
				t.Fatalf("first run: %+v", old)
			}
			entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			seen := make(chan delivery, 4)
			first := true
			a.notifier = func(ctx context.Context, _, _, body string) {
				if stage == "during platform delivery" && first {
					first = false
					close(entered)
					<-release
				}
				if ctx.Err() == nil {
					seen <- delivery{body, time.Now()}
				}
			}
			working := old
			working.Status = agent.Working
			a.agentsChanged([]agent.Info{working})
			a.completions.update([]agent.Info{old}, func(ctx context.Context, lead agent.Info) {
				defer close(returned)
				if stage == "before live recheck" {
					close(entered)
					<-release
				}
				a.notifySettled(ctx, lead)
			})
			select {
			case <-entered:
			case <-time.After(settleGrace + 3*time.Second):
				t.Fatal("first timer did not enter dispatch")
			}
			if stage == "before live recheck" {
				a.agentsChanged([]agent.Info{working})
			}
			current := a.finish(t, recoveredTurn)[0]
			if current.RunError != nil || !old.Since.Equal(current.Since) || !old.StartedAt.Equal(current.StartedAt) {
				t.Fatalf("second run: %+v", current)
			}
			// At the platform boundary also exercise a coalesced Working
			// snapshot. The Manager cancels delivery before the UI sees Idle.
			if stage == "during platform delivery" && old.SettledRun().Err() == nil {
				t.Fatal("second run left the first notification live")
			}
			armed := time.Now()
			a.agentsChanged([]agent.Info{current})
			unblock()
			<-returned
			select {
			case d := <-seen:
				elapsed := d.at.Sub(armed)
				if d.body != "Finished and waiting for you." || elapsed < settleGrace {
					t.Fatalf("notification %q at %s into the new quiet interval", d.body, elapsed)
				}
				t.Logf("second run dispatched %q after %s of quiet", d.body, elapsed)
			case <-time.After(settleGrace + 3*time.Second):
				t.Fatal("second timer did not notify")
			}
			// The old callback has returned and the new timer sends only once.
			select {
			case d := <-seen:
				t.Fatalf("extra notification: %q", d.body)
			default:
			}
		})
	}
}

func TestWorkerResumingCancelsPendingDelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var notices completionNotices
		team := []agent.Info{
			{ID: "alice/lead", Yesman: "alice", Role: agent.RoleLead, Status: agent.Idle},
			{ID: "alice/w", Yesman: "alice", Role: agent.RoleWorker, Status: agent.Working},
		}
		started := make(chan context.Context, 1)
		done := make(chan struct{}, 1)
		pending := func(ctx context.Context, _ agent.Info) { started <- ctx; <-ctx.Done() }
		notices.update(team, pending)
		team[1].Status = agent.Stopped
		notices.update(team, pending)
		time.Sleep(settleGrace)
		ctx := <-started // its timer has already fired
		team[1].Status = agent.Working
		notices.update(team, pending)
		if ctx.Err() == nil {
			t.Fatal("worker activity left the first delivery live")
		}
		team[1].Status = agent.Stopped
		notices.update(team, func(context.Context, agent.Info) { done <- struct{}{} })
		time.Sleep(settleGrace - time.Millisecond)
		synctest.Wait()
		if len(done) != 0 {
			t.Fatal("announced before the crew's new quiet interval")
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if len(done) != 1 {
			t.Fatalf("crew notifications = %d", len(done))
		}
	})
}

func TestSettledNoticeAfterRehire(t *testing.T) {
	a := newNoticeFixture(t)
	first := a.finish(t, recoveredTurn)[0]
	if err := a.agents.StopYesmanContext(t.Context(), a.workspace.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := a.yesmen.Archive(a.workspace.Name, nil); err != nil {
		t.Fatal(err)
	}
	a.agents.ForgetYesman(a.workspace.Name, a.workspace.HiredAt)
	if _, err := a.yesmen.HireAs(a.workspace.Name, ""); err != nil {
		t.Fatal(err)
	}
	current := a.finish(t, recoveredTurn)[0]
	if first.ID != current.ID || first.Since.Equal(current.Since) {
		t.Fatal("expected a new hire with the same ID")
	}
	var bodies []string
	a.notifier = func(_ context.Context, _, _, body string) { bodies = append(bodies, body) }
	a.notifySettled(context.Background(), first)
	if len(bodies) != 0 {
		t.Fatalf("retired hire notified: %q", bodies)
	}
	a.notifySettled(current.SettledRun(), current)
	if !slices.Equal(bodies, []string{"Finished and waiting for you."}) {
		t.Fatalf("new hire notifications: %q", bodies)
	}
}

func TestCompletionWaitsForWorkersAndIgnoresPassiveStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var notices completionNotices
		notified := make(chan struct{}, 10)
		notify := func(context.Context, agent.Info) { notified <- struct{}{} }
		list := []agent.Info{
			{ID: "alice/lead", Yesman: "alice", Role: agent.RoleLead, Status: agent.Starting},
		}
		notices.update(list, notify)
		list[0].Status = agent.Idle
		notices.update(list, notify)
		time.Sleep(settleGrace)
		synctest.Wait()
		if len(notified) != 0 {
			t.Fatal("opening a conversation announced completion")
		}

		list = append(list, agent.Info{ID: "alice/w1", Yesman: "alice", Role: agent.RoleWorker, Status: agent.Working})
		notices.update(list, notify)
		time.Sleep(settleGrace)
		synctest.Wait()
		if len(notified) != 0 {
			t.Fatal("completion announced with a worker still working")
		}
		list[1].Status = agent.Stopped
		notices.update(list, notify)
		time.Sleep(settleGrace)
		synctest.Wait()
		if len(notified) != 1 {
			t.Fatalf("notifications after worker stopped = %d", len(notified))
		}

		list[0].Status = agent.Working
		notices.update(list, notify)
		list[0].Status = agent.Idle
		notices.update(list, notify)
		// Dismissing the workspace cancels its pending notice.
		notices.update(nil, notify)
		time.Sleep(settleGrace)
		synctest.Wait()
		if len(notified) != 1 {
			t.Fatal("dismissed workspace announced completion")
		}
	})
}
