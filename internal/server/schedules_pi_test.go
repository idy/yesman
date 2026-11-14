package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/schedule"
	"github.com/idy/yesman/internal/yesmen"
)

// TestScheduledRunWithPiFixture runs the installed pi through the app's real
// Manager, API, extension and scheduler, answered by a local pi-messages
// backend: no model, key or user settings, and a scheduling clock of its own.
//
//	YESMAN_TEST_PI_FIXTURE=1 go test ./internal/server -run ScheduledRunWithPiFixture -v
//
// While the lead answers the user, a due run goes into pi's follow-up queue,
// never steering or aborting that answer. The lead reads it after; the window
// is told it is a scheduled task's run (plan, run, receipt), beside the
// message, also once pi restarts, while the model and the message itself
// carry none of that.
func TestScheduledRunWithPiFixture(t *testing.T) {
	var (
		held    sync.Once
		release = make(chan struct{})
	)
	f := startPiFixture(t, func(body string) []any {
		// Hold the answer to the user's question until the run is queued.
		if strings.Contains(body, "What changed today?") && !strings.Contains(body, "Scheduled task s1") {
			first := false
			held.Do(func() { first = true })
			if first {
				<-release
			}
		}
		return []any{
			map[string]any{"type": "text_start", "contentIndex": 0},
			map[string]any{"type": "text_end", "contentIndex": 0, "content": "fixture reply"},
		}
	})
	m, store := f.m, f.store
	y, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	var clockMu sync.Mutex
	now := time.Date(2026, 11, 16, 8, 59, 0, 0, time.UTC)
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	svc := schedule.New(schedule.Config{Yesmen: store, Agents: m, Now: clock, LockPath: filepath.Join(f.tmp, ".runtime", "schedules.lock")})
	t.Cleanup(svc.Close)
	f.srv.Schedules = svc
	f.mu.Lock()
	f.hook = svc.OnAgentEvent // as the app wires it
	f.mu.Unlock()
	svc.Start()
	if _, err := svc.Save("alice", schedule.PlanInput{Title: "Nightly check", Instructions: "Check the overnight builds.", Schedule: schedule.Spec{Kind: schedule.KindDaily, Time: "09:00"}, Timezone: "UTC", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	lead := agent.ID("alice", yesmen.LeadName)
	ctx := context.Background()
	human := fmt.Sprintf("%012x%036x", time.Now().UnixMilli(), 1)
	if d, err := m.Submit(ctx, lead, human, "What changed today?", nil, y.HiredAt); err != nil || d.State == "failed" {
		t.Fatalf("the user's message: %+v %v", d, err)
	}
	f.wait("the lead answers", func() bool { i, _ := m.Get(lead); return i.Status == agent.Working })

	clockMu.Lock()
	now = time.Date(2026, 11, 16, 9, 0, 5, 0, time.UTC)
	clockMu.Unlock()
	svc.Tick(ctx)
	svc.Wait()
	id := "s1-20261116t0900"
	run := scheduledRun(t, svc, id)
	if run.State != schedule.StateQueued || run.Attempts[0].State != "queued" {
		t.Fatalf("a run due while the lead answers: %+v", run)
	}
	if i, _ := m.Get(lead); i.Status != agent.Working {
		t.Fatalf("the lead's answer was interrupted: %s", i.Status)
	}
	close(release)

	f.wait("the lead reads the run", func() bool {
		i, _ := m.Get(lead)
		return len(f.users(lead)) >= 2 && i.Status == agent.Idle
	})
	attempt := scheduledRun(t, svc, id).Attempts[0].ID
	want := agent.Origin{Source: agent.OriginSchedule, Plan: "s1", Run: id, Submission: attempt}
	check := func(when string) {
		t.Helper()
		users := f.users(lead)
		if users[0].Yesman == nil || *users[0].Yesman != (agent.Origin{Source: agent.OriginUser, Submission: human}) {
			t.Fatalf("%s: the user's message records %+v", when, users[0].Yesman)
		}
		text := string(users[1].Content)
		if users[1].Yesman == nil || *users[1].Yesman != want || !strings.Contains(text, "Scheduled task s1") || !strings.Contains(text, "Check the overnight builds.") {
			t.Fatalf("%s: the run records %+v: %s", when, users[1].Yesman, text)
		}
		for _, private := range []string{id, attempt, human} {
			if strings.Contains(text, private) {
				t.Errorf("%s: the message itself shows %q", when, private)
			}
		}
	}
	check("live")
	f.noOriginSentToModels()
	for _, b := range f.requests() {
		for _, private := range []string{id, attempt, human, `"plan":"s1"`} {
			if strings.Contains(b, private) {
				t.Errorf("a model request shows %q", private)
			}
		}
	}
	// The session keeps the origin as a custom entry beside the message,
	// never in the message.
	sessions, _ := filepath.Glob(filepath.Join(y.SessionDir(yesmen.LeadName), "*.jsonl"))
	var entries int
	for _, s := range sessions {
		data, _ := os.ReadFile(s)
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, id) {
				continue
			}
			if !strings.Contains(line, `"yesman-origin"`) {
				t.Errorf("a session entry other than the origin shows the run: %.300s", line)
			}
			entries++
		}
	}
	if entries != 1 {
		t.Fatalf("%d origin entries name the run", entries)
	}
	// pi restarts: the window is still told, from the session.
	m.Stop(lead)
	check("after pi restarts")

	f.wait("the run follows its receipt", func() bool { return scheduledRun(t, svc, id).Attempts[0].State != "queued" })
	if run := scheduledRun(t, svc, id); run.State != schedule.StateQueued || run.Board != "t1" {
		t.Fatalf("after the lead read it: %+v", run)
	}
	v, err := svc.View("alice")
	if err != nil {
		t.Fatal(err)
	}
	if status := v.Plans[0].Runs[0].Status; status != schedule.StateSent {
		t.Fatalf("shown as %s once pi no longer reports it queued", status)
	}
}

func scheduledRun(t *testing.T, svc *schedule.Service, id string) schedule.Run {
	t.Helper()
	v, err := svc.View("alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range v.Plans {
		for _, r := range p.Runs {
			if r.ID == id {
				return r.Run
			}
		}
	}
	t.Fatalf("no run %s", id)
	return schedule.Run{}
}
