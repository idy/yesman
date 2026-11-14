package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// A handed-over question is announced once, however the lists it is seen in
// interleave, and a new question is announced again.
func TestHandedOverQuestionIsAnnouncedOnce(t *testing.T) {
	a, _ := crewApp(t)
	a.keepAwake = func(string) func() { return func() {} }
	var notes []string
	a.notifier = func(id, _, body string) { notes = append(notes, id+": "+body) }
	worker := func(handed bool, request string) []agent.Info {
		return []agent.Info{{ID: "alice/w1", Yesman: "alice", Name: "w1", Role: agent.RoleWorker, Status: agent.Waiting, HandedOver: handed,
			Dialog: json.RawMessage(`{"id":"` + request + `","method":"confirm"}`)}}
	}
	for _, list := range [][]agent.Info{worker(false, "q1"), worker(true, "q1"), worker(false, "q1"), worker(true, "q1")} {
		a.agentsChanged(list)
	}
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "alice/w1: ") {
		t.Fatalf("notifications = %q", notes)
	}
	if a.waiting != 1 {
		t.Errorf("waiting = %d, want the handed-over question", a.waiting)
	}
	a.agentsChanged(worker(true, "q2"))
	a.agentsChanged(worker(false, "q2"))
	if len(notes) != 2 {
		t.Fatalf("a new question: notifications = %q", notes)
	}
	if a.waiting != 0 {
		t.Errorf("waiting = %d after the question went back to the lead", a.waiting)
	}
}

// Open handoffs count for the tray; closing one, or archiving its yesman,
// takes it off; the lead hears what the user did.
func TestHandoffsCountAndNotice(t *testing.T) {
	a, _ := crewApp(t)
	a.keepAwake = func(string) func() { return func() {} }
	var notes []string
	a.notifier = func(id, _, body string) { notes = append(notes, id+": "+body) }
	a.agents = agent.New(agent.Config{Yesmen: a.yesmen})
	t.Cleanup(a.agents.StopAll)
	y, err := a.yesmen.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	h, _, err := a.yesmen.AddHandoff("alice", yesmen.Handoff{Type: yesmen.HandoffInstall, Title: "Install Xcode"})
	if err != nil {
		t.Fatal(err)
	}
	a.needsYouChanged("alice", &h)
	if a.waiting != 1 || len(notes) != 1 || notes[0] != "alice/lead: Install Xcode" {
		t.Fatalf("waiting %d, notifications %q", a.waiting, notes)
	}
	s := &service{app: a}
	if _, err := s.ResolveHandoff("alice", y.HiredAt.Add(-time.Second), h.ID, yesmen.HandoffDone); err == nil {
		t.Fatal("a stale window closed a handoff")
	}
	if _, err := s.ResolveHandoff("alice", y.HiredAt, h.ID, yesmen.HandoffResolved); err == nil {
		t.Fatal("the user resolved a handoff as the lead")
	}
	closed, err := s.ResolveHandoff("alice", y.HiredAt, h.ID, yesmen.HandoffDone)
	if err != nil || closed.ClosedBy != "user" || closed.Resolution != yesmen.HandoffDone {
		t.Fatalf("resolve = %+v, %v", closed, err)
	}
	if a.waiting != 0 {
		t.Errorf("waiting = %d after closing", a.waiting)
	}
	if _, err := s.ResolveHandoff("alice", y.HiredAt, h.ID, yesmen.HandoffNotNeeded); err == nil {
		t.Error("closed twice")
	}
	for _, c := range []struct {
		h    yesmen.Handoff
		want string
	}{
		{yesmen.Handoff{ID: "n1", Type: "install", Title: "Install Xcode", Task: "t4", Resolution: "done"}, `[yesman] The user marked n1 (install) "Install Xcode", for task t4 as done. That is their own confirmation: check it worked before relying on it.`},
		{yesmen.Handoff{ID: "n2", Type: "decision", Title: "Pick", Resolution: "done"}, `[yesman] The user marked n2 (decision) "Pick" as done. Their answer, if they gave one, is in their messages to you.`},
		{yesmen.Handoff{ID: "n3", Type: "login", Title: "Log in", Resolution: "not-needed"}, `[yesman] The user marked n3 (login) "Log in" as not needed. Go on without it.`},
	} {
		if got := handoffNotice(c.h); got != c.want {
			t.Errorf("notice = %q\nwant %q", got, c.want)
		}
	}
}
