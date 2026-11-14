package server

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

func needsYouServer(t *testing.T) (*Server, yesmen.Yesman, *[]string) {
	t.Helper()
	dir := t.TempDir()
	store := yesmen.New(filepath.Join(dir, "yesmen"), filepath.Join(dir, "archive"))
	y, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorker(y.Name, yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	title := "Publish"
	if _, err := store.ChangeBoard(y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: &title}}}); err != nil {
		t.Fatal(err)
	}
	m := agent.New(agent.Config{Yesmen: store})
	t.Cleanup(m.StopAll)
	var told []string
	s := &Server{Yesmen: store, Agents: m, NeedsYouChanged: func(yesman string, added *yesmen.Handoff) {
		id := ""
		if added != nil {
			id = added.ID
		}
		told = append(told, yesman+":"+id)
	}}
	return s, y, &told
}

func needsYouCall(s *Server, c caller, body string) (any, error) {
	return s.changeNeedsYou(context.Background(), c, httptest.NewRequest("POST", "/v1/needs-you", strings.NewReader(body)))
}

func TestNeedsYouIsTheLeadsAndExplicit(t *testing.T) {
	s, y, told := needsYouServer(t)
	lead := caller{agent: agent.Info{ID: "alice/lead", Yesman: "alice", Name: "lead", Role: agent.RoleLead}, yesman: y}
	worker := caller{agent: agent.Info{ID: "alice/w1", Yesman: "alice", Name: "w1", Role: agent.RoleWorker}, yesman: y}

	if _, err := needsYouCall(s, worker, `{"add":{"type":"login","title":"Log in"}}`); err != errLeadOnly {
		t.Errorf("a worker registered a handoff: %v", err)
	}
	if _, err := s.needsYou(context.Background(), worker, httptest.NewRequest("GET", "/v1/needs-you", nil)); err != errLeadOnly {
		t.Errorf("a worker read Needs you: %v", err)
	}
	if _, err := needsYouCall(s, lead, `{"add":{"type":"login","title":"x"},"resolve":{"id":"n1"}}`); err == nil {
		t.Error("two changes at once were accepted")
	}
	if text := s.needsYouContext(lead); text != "" {
		t.Errorf("context with nothing waiting: %q", text)
	}

	body := `{"add":{"type":"login","title":"Log in to the registry","detail":"Run npm login.","task":"#1","worker":"w1"},"toolCall":"call-7"}`
	out, err := needsYouCall(s, lead, body)
	h, _ := out.(yesmen.Handoff)
	if err != nil || h.ID != "n1" || h.Task != "t1" || h.Worker != "w1" || h.ToolCall != "call-7" {
		t.Fatalf("add = %+v, %v", out, err)
	}
	// The same call again (a retried tool call) adds and announces nothing new.
	if out, err := needsYouCall(s, lead, body); err != nil || out.(yesmen.Handoff).ID != "n1" {
		t.Fatalf("repeated add = %+v, %v", out, err)
	}
	if got := strings.Join(*told, ","); got != "alice:n1,alice:" {
		t.Errorf("announced %s", got)
	}
	text := s.needsYouContext(lead)
	if !strings.Contains(text, "1 waiting on the user") || !strings.Contains(text, "n1 (login): Log in to the registry") || strings.Contains(text, "Run npm login") {
		t.Errorf("context:\n%s", text)
	}

	view, err := s.needsYou(context.Background(), lead, httptest.NewRequest("GET", "/v1/needs-you", nil))
	if v := view.(NeedsYouView); err != nil || len(v.Handoffs) != 1 || len(v.Questions) != 0 {
		t.Fatalf("list = %+v, %v", view, err)
	}
	detail, err := s.needsYou(context.Background(), lead, httptest.NewRequest("GET", "/v1/needs-you?id=n1", nil))
	if err != nil || detail.(yesmen.Handoff).Detail != "Run npm login." {
		t.Fatalf("detail = %+v, %v", detail, err)
	}

	// Handing over needs a question the worker actually waits on.
	if _, err := needsYouCall(s, lead, `{"handOver":{"worker":"w1","requestId":"q1"}}`); err == nil {
		t.Error("handed over a question nobody asked")
	}
	if _, err := needsYouCall(s, lead, `{"handOver":{"worker":"lead","requestId":"q1"}}`); err == nil {
		t.Error("handed over the lead's own question")
	}

	// A lead of an archived yesman cannot write into its same-name successor.
	stale := lead
	stale.yesman.HiredAt = y.HiredAt.Add(-time.Hour)
	if _, err := needsYouCall(s, stale, `{"resolve":{"id":"n1"}}`); err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Errorf("a stale lead resolved a handoff: %v", err)
	}

	if out, err := needsYouCall(s, lead, `{"resolve":{"id":"n1","note":"Logged in with the CI token"}}`); err != nil || out.(yesmen.Handoff).ClosedBy != "lead" {
		t.Fatalf("resolve = %+v, %v", out, err)
	}
	if _, err := needsYouCall(s, lead, `{"resolve":{"id":"n1"}}`); err == nil {
		t.Error("resolved twice")
	}
	if text := s.needsYouContext(lead); text != "" {
		t.Errorf("context after resolving: %q", text)
	}
}
