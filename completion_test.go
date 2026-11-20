package main

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/idy/yesman/internal/agent"
)

func TestCompletionWaitsForUninterruptedQuiet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var notices completionNotices
		got := make(chan agent.Info, 10)
		notify := func(i agent.Info) { got <- i }
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

// Drive the production desktop observer and notification callback. The real
// Manager supplies the live Lead state when the quiet timer expires. The
// scripted Pi emits assistant messages locally and the fake clock controls
// when the desktop receives its snapshots.
func TestDesktopNotificationAfterModelError(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	const failed = `ASSISTANT {"role":"assistant","stopReason":"error","errorMessage":"fixture connection refused"}`
	const succeeded = `ASSISTANT {"role":"assistant","stopReason":"stop"}`
	waitForLead := func(t *testing.T) {
		t.Helper()
		select {
		case <-settled:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if _, err := a.agents.StartWorker(ctx, workspace.Name, "helper", "First attempt", failed); err != nil {
		t.Fatal(err)
	}
	waitForLead(t) // the worker's model error has reached its Lead
	for _, tc := range []struct {
		name, message, body string
	}{
		{"failed", failed, "The model request failed. Open the conversation for details."},
		{"recovered", succeeded, "Finished and waiting for you."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := a.agents.Prompt(ctx, id, tc.message); err != nil {
				t.Fatal(err)
			}
			waitForLead(t)
			list := a.agents.List()
			if len(list) != 2 || list[0].Status != agent.Idle || (list[0].RunError != nil) != (tc.message == failed) || list[1].RunError == nil {
				t.Fatalf("unexpected settled team: %+v", list)
			}
			synctest.Test(t, func(t *testing.T) {
				var notices []string
				a.notifier = func(gotID, title, body string) {
					if gotID != id || title != workspace.Label() {
						t.Errorf("notification for %q (%q), want %q (%q)", gotID, title, id, workspace.Label())
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
			a.notifier = func(_, _, body string) { bodies = append(bodies, body) }
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

func TestCompletionWaitsForWorkersAndIgnoresPassiveStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var notices completionNotices
		notified := make(chan struct{}, 10)
		notify := func(agent.Info) { notified <- struct{}{} }
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
