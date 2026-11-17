package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/schedule"
	"github.com/idy/yesman/internal/yesmen"
)

// Only a lead reads or drafts scheduled tasks; a draft is always off, and a
// run's board task names its plan in the context.
func TestSchedulesAreTheLeadsAndDraftsStayOff(t *testing.T) {
	tmp := t.TempDir()
	store := yesmen.New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	y, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	m := agent.New(agent.Config{Yesmen: store})
	defer m.StopAll()
	svc := schedule.New(schedule.Config{Yesmen: store, Agents: m})
	defer svc.Close()
	s := &Server{Pool: pool.New(filepath.Join(tmp, "repos")), Yesmen: store, Agents: m, Schedules: svc}
	// No agent runs here: the lead's credential is one this test says is
	// admitted. schedules_lifetime_test.go admits real processes.
	s.beginChange = func(token string, y yesmen.Yesman) (func(), error) {
		if token != "lead-credential" || y.Name != "alice" {
			return nil, agent.ErrRetired
		}
		return func() {}, nil
	}
	lead := caller{agent: agent.Info{ID: "alice/lead", Yesman: "alice", Name: "lead", Role: agent.RoleLead}, yesman: y, token: "lead-credential"}
	worker := caller{agent: agent.Info{ID: "alice/w1", Yesman: "alice", Name: "w1", Role: agent.RoleWorker}, yesman: y}
	ctx := context.Background()
	draft := `{"title":"Morning check","instructions":"Check overnight CI.","kind":"daily","time":"09:00","timezone":"Asia/Shanghai"}`

	if _, err := s.draftSchedule(ctx, worker, httptest.NewRequest("POST", "/v1/schedules/draft", strings.NewReader(draft))); err == nil {
		t.Fatal("a worker drafted a scheduled task")
	}
	if _, err := s.schedules(ctx, worker, httptest.NewRequest("GET", "/v1/schedules", nil)); err == nil {
		t.Fatal("a worker listed scheduled tasks")
	}
	out, err := s.draftSchedule(ctx, lead, httptest.NewRequest("POST", "/v1/schedules/draft", strings.NewReader(`{"title":"Morning check","instructions":"Check overnight CI.","kind":"daily","time":"09:00","timezone":"Asia/Shanghai","enabled":true}`)))
	if err != nil || !strings.Contains(out.(string), "It is off") {
		t.Fatalf("draft: %v %v", out, err)
	}
	v, err := svc.View("alice")
	if err != nil || len(v.Plans) != 1 || v.Plans[0].Enabled || v.Plans[0].CreatedBy != "lead" {
		t.Fatalf("view: %+v %v", v, err)
	}
	list, err := s.schedules(ctx, lead, httptest.NewRequest("GET", "/v1/schedules", nil))
	if err != nil || !strings.Contains(list.(string), `s1 "Morning check": every day at 09:00 (Asia/Shanghai), off, drafted by you`) {
		t.Fatalf("list: %v %v", list, err)
	}
	if _, err := s.draftSchedule(ctx, lead, httptest.NewRequest("POST", "/v1/schedules/draft", strings.NewReader(`{"title":"Bad","instructions":"x","kind":"daily","time":"9","timezone":"Asia/Shanghai"}`))); err == nil {
		t.Fatal("an invalid draft was saved")
	}
	// A lead whose credential ended (stopped, restarted, dismissed), or of
	// an earlier workspace generation, saves nothing.
	stopped, earlier := lead, lead
	stopped.token = "ended"
	earlier.yesman.HiredAt = y.HiredAt.Add(-time.Second)
	for _, c := range []caller{stopped, earlier} {
		if _, err := s.draftSchedule(ctx, c, httptest.NewRequest("POST", "/v1/schedules/draft", strings.NewReader(draft))); !errors.Is(err, schedule.ErrStaleCaller) {
			t.Fatalf("a stale lead's draft: %v", err)
		}
	}
	if v, _ := svc.View("alice"); len(v.Plans) != 1 {
		t.Fatalf("%d plans after stale drafts", len(v.Plans))
	}

	if _, _, err := store.AddScheduledTask("alice", yesmen.TaskSchedule{Plan: "s1", Run: "s1-m-0123456789abcdef"}, "Morning check", "detail"); err != nil {
		t.Fatal(err)
	}
	board, _ := store.Board("alice")
	text := BuildContext(ctx, s.Pool, m, y, lead.agent, board) + svc.LeadContext("alice")
	for _, want := range []string{"- t1 [todo] Morning check (a run of scheduled task s1)", "## Scheduled tasks", "Never draft one on your own", `- s1 "Morning check": every day at 09:00 (Asia/Shanghai), off`} {
		if !strings.Contains(text, want) {
			t.Errorf("the lead's context lacks %q:\n%s", want, text)
		}
	}
}

// The board agents read and change names a scheduled run's plan, never the
// scheduler's run handle, for a lead and a worker alike.
func TestAgentBoardLeavesOutTheRunHandle(t *testing.T) {
	// The board's real fixture: a change is admitted for the caller's
	// running process (agent.BeginChange), as an agent's request is.
	f := newBoardFixture(t)
	y := f.hire("alice")
	const run = "s1-m-0123456789abcdef"
	if _, _, err := f.store.AddScheduledTask("alice", yesmen.TaskSchedule{Plan: "s1", Run: run}, "Morning check", "detail"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"alice/lead", "alice/helper"} {
		if id == "alice/helper" {
			if _, err := f.m.StartWorker(ctx, "alice", "helper", "Helper", "help"); err != nil {
				t.Fatal(err)
			}
		}
		tr := f.start(id)
		info, err := f.m.Authenticate(tr.Token)
		if err != nil {
			t.Fatal(err)
		}
		c := caller{agent: info, yesman: y, token: tr.Token}
		read, err := f.srv.board(ctx, c, httptest.NewRequest("GET", "/v1/board", nil))
		if err != nil {
			t.Fatal(err)
		}
		changed, err := f.srv.changeBoard(ctx, c, httptest.NewRequest("POST", "/v1/board", strings.NewReader(`{"update":[{"id":"t1","status":"doing"}]}`)))
		if err != nil {
			t.Fatal(err)
		}
		empty, err := f.srv.changeBoard(ctx, c, httptest.NewRequest("POST", "/v1/board", strings.NewReader(`{}`)))
		if err != nil {
			t.Fatal(err)
		}
		for _, out := range []any{read, changed, empty} {
			data, _ := json.Marshal(out)
			if strings.Contains(string(data), run) || !strings.Contains(string(data), `"schedule":{"plan":"s1"}`) || !strings.Contains(string(data), `"id":"t1"`) {
				t.Fatalf("%s (%s) sees %s", id, info.Role, data)
			}
		}
	}
	// The run handle stays on the board itself, for the scheduler.
	if b, _ := f.store.Board("alice"); b.Tasks[0].Schedule == nil || b.Tasks[0].Schedule.Run != run || b.Tasks[0].Status != yesmen.TaskDoing {
		t.Fatalf("board: %+v", b.Tasks[0])
	}
}
