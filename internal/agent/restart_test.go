package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/restart"
	"github.com/idy/yesman/internal/yesmen"
)

// received collects the prompts the fake pi processes were sent.
type received struct {
	mu   sync.Mutex
	list []struct{ agent, message, behavior string }
}

func (r *received) on(e Event) {
	var d struct {
		Type              string `json:"type"`
		Message           string `json:"message"`
		StreamingBehavior string `json:"streamingBehavior"`
	}
	if json.Unmarshal(e.Data, &d) == nil && d.Type == "test_prompt_received" {
		r.mu.Lock()
		r.list = append(r.list, struct{ agent, message, behavior string }{e.Agent, d.Message, d.StreamingBehavior})
		r.mu.Unlock()
	}
}

// notes are the prompts to id that were the given note.
func (r *received) notes(id, note string) int {
	n := 0
	for _, p := range r.to(id) {
		if p == note {
			n++
		}
	}
	return n
}

func (r *received) to(id string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, p := range r.list {
		if p.agent == id {
			out = append(out, p.message)
		}
	}
	return out
}

// restartInstance is one app instance's Manager over a shared store, as
// the old and the new process of a restart are.
func restartInstance(t *testing.T, store *yesmen.Store, tmp string) (*Manager, *received) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := &received{}
	m := New(Config{
		Pi: exe, Extension: filepath.Join(tmp, "none.ts"),
		Env:    []string{"YESMAN_FAKE_PI=1", "FAKE_PI_DELAY=0s"},
		Yesmen: store, OnEvent: r.on,
	})
	t.Cleanup(m.StopAll)
	return m, r
}

func restartStore(t *testing.T) (*yesmen.Store, string, yesmen.Yesman) {
	t.Helper()
	tmp := t.TempDir()
	store := yesmen.New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	y, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	return store, tmp, y
}

func addWorker(t *testing.T, store *yesmen.Store, name string) yesmen.Worker {
	t.Helper()
	w := yesmen.Worker{Name: name, Title: "work", Task: "do it", StartedAt: time.Now().UTC().Truncate(time.Millisecond)}
	if err := store.AddWorker("alice", w); err != nil {
		t.Fatal(err)
	}
	ws, _ := store.Workers("alice")
	for _, got := range ws {
		if got.Name == name {
			return got
		}
	}
	t.Fatalf("worker %s not recorded", name)
	return w
}

// started runs an agent with its fake pi and sets what it is doing.
func started(t *testing.T, m *Manager, id string, set func(a *agent)) {
	t.Helper()
	a, _, err := m.ensure(id)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	set(a)
	m.mu.Unlock()
}

func TestCloseForRestartListsInterruptedWorkOnly(t *testing.T) {
	store, tmp, y := restartStore(t)
	for _, n := range []string{"busy", "asks", "handed", "reporting", "idle", "sleeping", "starting"} {
		addWorker(t, store, n)
	}
	m, _ := restartInstance(t, store, tmp)
	started(t, m, ID("alice", "lead"), func(a *agent) { a.info.Status = Working })
	started(t, m, ID("alice", "busy"), func(a *agent) { a.info.Status = Working })
	started(t, m, ID("alice", "asks"), func(a *agent) {
		a.info.Status, a.info.Dialog = Waiting, json.RawMessage(`{"id":"q1","method":"select"}`)
	})
	started(t, m, ID("alice", "handed"), func(a *agent) {
		a.info.Status, a.info.Dialog, a.handedOver = Waiting, json.RawMessage(`{"id":"q2","method":"confirm"}`), "q2"
	})
	started(t, m, ID("alice", "reporting"), func(a *agent) { a.reports = 1 })
	started(t, m, ID("alice", "idle"), func(a *agent) {})                                                // includes a run the user stopped: idle
	started(t, m, ID("alice", "sleeping"), func(a *agent) { a.info.Status, a.stopping = Working, true }) // being put to sleep
	started(t, m, ID("alice", "starting"), func(a *agent) { a.starting = make(chan struct{}) })

	got := m.CloseForRestart()
	m.mu.Lock()
	m.agents[ID("alice", "starting")].starting = nil
	m.mu.Unlock()

	want := map[string]string{"lead": restart.ReasonWorking, "busy": restart.ReasonWorking, "asks": restart.ReasonQuestion,
		"handed": restart.ReasonNeedsYou, "reporting": restart.ReasonReport}
	if len(got) != len(want) {
		t.Fatalf("listed %+v", got)
	}
	for _, g := range got {
		if want[g.Name] != g.Reason || !g.WorkspaceHiredAt.Equal(y.HiredAt) || g.AgentSince.IsZero() {
			t.Errorf("%s: %+v", g.Name, g)
		}
		if g.Name == "lead" && !g.AgentSince.Equal(y.HiredAt) {
			t.Errorf("the lead's identity is not its hiring: %+v", g)
		}
	}
	in := restart.Intent{Version: restart.Version, ID: restart.NewID(), Kind: restart.KindRestart, CreatedAt: time.Now(), Agents: got}
	if err := in.Validate(time.Now()); err != nil {
		t.Fatalf("the snapshot is not a valid intent: %v", err)
	}
	// Admission is closed: no new start slips in before the agents stop.
	if err := m.Prompt(context.Background(), ID("alice", "idle"), "late"); err == nil {
		m.mu.Lock()
		running := m.agents[ID("alice", "idle")].proc != nil
		m.mu.Unlock()
		_ = running // idle still runs until StopAll; the prompt goes to it
	}
	if _, _, err := m.ensure(ID("alice", "handed")); err != nil && !errors.Is(err, errStopped) {
		t.Fatal(err)
	}
	m.Stop(ID("alice", "handed"))
	if _, _, err := m.ensure(ID("alice", "handed")); !errors.Is(err, errStopped) {
		t.Fatalf("a stopped agent started after the restart closed the manager: %v", err)
	}
}

func TestResumeAfterRestart(t *testing.T) {
	store, tmp, y := restartStore(t)
	busy := addWorker(t, store, "busy")
	asks := addWorker(t, store, "asks")
	done := addWorker(t, store, "done")
	reporting := addWorker(t, store, "reporting")
	if err := store.FinishWorker("alice", "done", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	entry := func(w yesmen.Worker, reason string) restart.Agent {
		return restart.Agent{Yesman: "alice", Name: w.Name, Role: RoleWorker, WorkspaceHiredAt: y.HiredAt, AgentSince: w.StartedAt, Reason: reason}
	}
	lead := restart.Agent{Yesman: "alice", Name: "lead", Role: RoleLead, WorkspaceHiredAt: y.HiredAt, AgentSince: y.HiredAt, Reason: restart.ReasonWorking}

	m, got := restartInstance(t, store, tmp) // the new instance
	ctx := context.Background()
	note := "[yesman] Yes Man restarted while you were working. test-note"

	for _, c := range []struct {
		name  string
		entry restart.Agent
		want  string
	}{
		{"lead working", lead, restart.Resumed},
		{"worker working", entry(busy, restart.ReasonWorking), restart.Resumed},
		{"worker question", entry(asks, restart.ReasonQuestion), restart.Resumed},
		{"historical done worker", entry(done, restart.ReasonWorking), restart.Finished},
		{"needs you", func() restart.Agent {
			e := entry(busy, restart.ReasonNeedsYou)
			e.Name = "asks"
			e.AgentSince = asks.StartedAt
			return e
		}(), restart.Waiting},
		{"worker replaced", func() restart.Agent {
			e := entry(busy, restart.ReasonWorking)
			e.AgentSince = e.AgentSince.Add(-time.Hour)
			return e
		}(), restart.Gone},
		{"workspace re-hired", func() restart.Agent {
			e := lead
			e.WorkspaceHiredAt, e.AgentSince = y.HiredAt.Add(-time.Hour), y.HiredAt.Add(-time.Hour)
			return e
		}(), restart.Gone},
		{"workspace gone", func() restart.Agent { e := lead; e.Yesman = "bob"; return e }(), restart.Gone},
	} {
		t.Run(c.name, func(t *testing.T) {
			outcome, err := m.Resume(ctx, c.entry, note)
			if outcome != c.want {
				t.Fatalf("Resume = %s (%v), want %s", outcome, err, c.want)
			}
		})
	}
	waitStatus(t, m, ID("alice", "lead"), Idle)
	for _, id := range []string{ID("alice", "lead"), ID("alice", "busy"), ID("alice", "asks")} {
		if n := got.notes(id, note); n != 1 {
			t.Errorf("%s was sent the note %d times: %q", id, n, got.to(id))
		}
	}
	if p := got.to(ID("alice", "done")); len(p) != 0 {
		t.Errorf("a finished worker was woken: %q", p)
	}

	// A second resume of the same agent, as a duplicate, does not send it
	// again while it runs.
	if outcome, _ := m.Resume(ctx, lead, note); outcome != restart.Active {
		t.Fatalf("a second resume = %s", outcome)
	}
	if n := got.notes(ID("alice", "lead"), note); n != 1 {
		t.Fatalf("the lead got %d notes", n)
	}

	// A worker whose report was on its way hands it to the lead again, once,
	// and is closed as finished.
	outcome, err := m.Resume(ctx, entry(reporting, restart.ReasonReport), note)
	if outcome != restart.Reported || err != nil {
		t.Fatalf("report Resume = %s %v", outcome, err)
	}
	var report []string
	for _, p := range got.to(ID("alice", "lead")) {
		if strings.HasPrefix(p, `[yesman] Worker "reporting" finished a run. Its final message:`) {
			report = append(report, p)
		}
	}
	if len(report) != 1 || !strings.Contains(report[0], "Sent again") || !strings.Contains(report[0], "done") {
		t.Fatalf("report to the lead: %q", report)
	}
	if p := got.to(ID("alice", "reporting")); len(p) != 0 {
		t.Errorf("the reporting worker was sent %q; its report needs no new run", p)
	}
	ws, _ := store.Workers("alice")
	for _, w := range ws {
		if w.Name == "reporting" && w.FinishedAt.IsZero() {
			t.Error("the reporting worker was not recorded finished")
		}
	}

	// The app quitting during resume stops the rest.
	late := addWorker(t, store, "late")
	m.StopAll()
	if outcome, _ := m.Resume(ctx, entry(late, restart.ReasonWorking), note); outcome != restart.Stopped {
		t.Fatalf("Resume after quit = %s", outcome)
	}
}

// A message the user sent first goes on; the restart's note is not added.
func TestResumeYieldsToFirstMessage(t *testing.T) {
	store, tmp, y := restartStore(t)
	m, got := restartInstance(t, store, tmp)
	lead := restart.Agent{Yesman: "alice", Name: "lead", Role: RoleLead, WorkspaceHiredAt: y.HiredAt, AgentSince: y.HiredAt, Reason: restart.ReasonWorking}
	if err := m.Prompt(context.Background(), ID("alice", "lead"), "user first"); err != nil {
		t.Fatal(err)
	}
	if outcome, _ := m.Resume(context.Background(), lead, "note"); outcome != restart.Active {
		t.Fatalf("Resume = %s", outcome)
	}
	if p := got.to(ID("alice", "lead")); len(p) != 1 || p[0] != "user first" {
		t.Fatalf("lead got %q", p)
	}
}

// The user putting the agent to sleep while it starts wins over the resume.
func TestResumeStoppedWhileStarting(t *testing.T) {
	store, tmp, y := restartStore(t)
	exe, _ := os.Executable()
	r := &received{}
	m := New(Config{Pi: exe, Extension: filepath.Join(tmp, "none.ts"),
		Env: []string{"YESMAN_FAKE_PI=1", "FAKE_PI_DELAY=1s"}, Yesmen: store, OnEvent: r.on})
	t.Cleanup(m.StopAll)
	lead := restart.Agent{Yesman: "alice", Name: "lead", Role: RoleLead, WorkspaceHiredAt: y.HiredAt, AgentSince: y.HiredAt, Reason: restart.ReasonWorking}
	res := make(chan string, 1)
	go func() { o, _ := m.Resume(context.Background(), lead, "note"); res <- o }()
	waitStatus(t, m, ID("alice", "lead"), Starting)
	m.Stop(ID("alice", "lead"))
	if o := <-res; o != restart.Stopped {
		t.Fatalf("Resume = %s", o)
	}
	if p := r.to(ID("alice", "lead")); len(p) != 0 {
		t.Fatalf("a stopped agent was sent %q", p)
	}
}
