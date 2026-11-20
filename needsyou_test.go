package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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
	a.notifier = func(_ context.Context, id, _, body string) { notes = append(notes, id+": "+body) }
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
	a.notifier = func(_ context.Context, id, _, body string) { notes = append(notes, id+": "+body) }
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

// On startup, the handoffs kept from before count for the tray at once,
// without a notification; agents and handoffs count once each.
func TestStartupCountsKeptHandoffs(t *testing.T) {
	original, _ := crewApp(t)
	if _, err := original.yesmen.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := original.yesmen.AddHandoff("alice", yesmen.Handoff{Type: yesmen.HandoffManual, Title: "kept"}); err != nil {
		t.Fatal(err)
	}
	var notes []string
	restarted := &app{yesmen: original.yesmen, notifier: func(_ context.Context, _, _, body string) { notes = append(notes, body) }}
	restarted.keepAwake = func(string) func() { return func() {} }
	// This app has no agent manager: a lead going idle here must not leave
	// a finished notice to fire after the test, while later tests run.
	t.Cleanup(func() { restarted.completions.update(nil, func(context.Context, agent.Info) {}) })
	restarted.countHandoffs()
	if restarted.waiting != 1 || len(notes) != 0 {
		t.Fatalf("on startup: waiting %d, notifications %q", restarted.waiting, notes)
	}
	lead := agent.Info{ID: "alice/lead", Yesman: "alice", Name: "lead", Role: agent.RoleLead, Status: agent.Waiting}
	restarted.agentsChanged([]agent.Info{lead})
	restarted.countHandoffs("alice")
	if restarted.waiting != 2 {
		t.Errorf("a waiting lead and a handoff: waiting %d", restarted.waiting)
	}
	lead.Status = agent.Idle
	restarted.agentsChanged([]agent.Info{lead})
	if restarted.waiting != 1 {
		t.Errorf("the lead answered: waiting %d", restarted.waiting)
	}
}

// Only the user's window starts a new tool-call memory, for the yesman it
// showed, as it showed it.
func TestResetCallMemoryIsTheUsers(t *testing.T) {
	a, _ := crewApp(t)
	a.keepAwake = func(string) func() { return func() {} }
	y, err := a.yesmen.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"items":[],"next":0,"trimmedCallsBefore":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(y.HandoffsFile(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	hs, _ := a.yesmen.Handoffs("alice")
	m := hs.CallMemory()
	if m.State != "blocked" {
		t.Fatalf("call memory %+v", m)
	}
	s := &service{app: a}
	if _, err := s.ResetCallMemory("alice", y.HiredAt.Add(-time.Second), m.Epoch, m.Used, m.Forgettable); err == nil {
		t.Fatal("a stale window reset the memory")
	}
	if _, err := s.ResetCallMemory("alice", y.HiredAt, m.Epoch+1, m.Used, m.Forgettable); !errors.Is(err, yesmen.ErrCallMemoryChanged) {
		t.Fatalf("a confirmation of another memory: %v", err)
	}
	if data, _ := os.ReadFile(y.HandoffsFile()); string(data) != raw {
		t.Fatal("a refused reset changed the file")
	}
	got, err := s.ResetCallMemory("alice", y.HiredAt, m.Epoch, m.Used, m.Forgettable)
	if err != nil || got.State != "" || got.Epoch != 1 {
		t.Fatalf("reset = %+v, %v", got, err)
	}
	if _, created, err := a.yesmen.AddHandoff("alice", yesmen.Handoff{Type: yesmen.HandoffManual, Title: "x", ToolCall: "toolu_1"}); err != nil || !created {
		t.Fatalf("a call after the reset: %v %v", created, err)
	}
}
