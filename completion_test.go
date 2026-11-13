package main

import (
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
