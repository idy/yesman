package server

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

type needsYouFixture struct {
	s      *Server
	y      yesmen.Yesman
	told   []string
	tokens string // where the fake pi writes each agent's credential
}

// needsYouServer serves the API for a yesman alice, with a worker w1 on record
// and a board task t1. Agents run as the fake pi of fakepi_test.go.
func needsYouServer(t *testing.T) *needsYouFixture {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f := &needsYouFixture{tokens: filepath.Join(dir, "tokens")}
	if err := os.Mkdir(f.tokens, 0o700); err != nil {
		t.Fatal(err)
	}
	store := yesmen.New(filepath.Join(dir, "yesmen"), filepath.Join(dir, "archive"))
	if f.y, err = store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorker(f.y.Name, yesmen.Worker{Name: "w1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	title := "Publish"
	if _, err := store.ChangeBoard(f.y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: &title}}}); err != nil {
		t.Fatal(err)
	}
	m := agent.New(agent.Config{
		Pi: exe, Extension: filepath.Join(dir, "none.ts"), Yesmen: store,
		Env: []string{"YESMAN_SERVER_FAKE_PI=1", "FAKE_PI_TOKENS=" + f.tokens},
	})
	t.Cleanup(m.StopAll)
	var mu sync.Mutex
	f.s = &Server{Yesmen: store, Agents: m, NeedsYouChanged: func(yesman string, added *yesmen.Handoff) {
		id := ""
		if added != nil {
			id = added.ID
		}
		mu.Lock()
		f.told = append(f.told, yesman+":"+id)
		mu.Unlock()
	}}
	return f
}

// start starts an agent and returns it as a caller of the API, with the
// credential its process got.
func (f *needsYouFixture) start(t *testing.T, id string) caller {
	t.Helper()
	if _, err := f.s.Agents.Messages(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(filepath.Join(f.tokens, strings.ReplaceAll(id, "/", "_")))
	if err != nil {
		t.Fatal(err)
	}
	info, err := f.s.Agents.Authenticate(string(token))
	if err != nil {
		t.Fatal(err)
	}
	y, err := f.s.Yesmen.Get(info.Yesman)
	if err != nil {
		t.Fatal(err)
	}
	return caller{agent: info, yesman: y, token: string(token)}
}

func needsYouCall(s *Server, c caller, body string) (any, error) {
	return s.changeNeedsYou(context.Background(), c, httptest.NewRequest("POST", "/v1/needs-you", strings.NewReader(body)))
}

func TestNeedsYouIsTheLeadsAndExplicit(t *testing.T) {
	f := needsYouServer(t)
	s, y := f.s, f.y
	lead := f.start(t, "alice/lead")
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
	if got := strings.Join(f.told, ","); got != "alice:n1,alice:" {
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

// pausedBody holds a request at its first read, after authentication.
type pausedBody struct {
	io.Reader
	entered, release chan struct{}
	once             sync.Once
}

func (b *pausedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.Reader.Read(p)
}

// A request authenticated before its lead was stopped and restarted, or its
// workspace archived and hired again, changes nothing when its body arrives:
// the commit checks the same process still holds the credential.
func TestNeedsYouChangeFromARetiredLeadIsRefused(t *testing.T) {
	for _, c := range []struct{ name, action, body string }{
		{"add after restart", "restart", `{"add":{"type":"manual","title":"late write"},"toolCall":"late"}`},
		{"resolve after restart", "restart", `{"resolve":{"id":"n1","note":"late"}}`},
		{"add after archive and rehire", "rehire", `{"add":{"type":"manual","title":"late write"},"toolCall":"late"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := needsYouServer(t)
			lead := f.start(t, "alice/lead")
			if _, err := needsYouCall(f.s, lead, `{"add":{"type":"login","title":"Log in"},"toolCall":"first"}`); err != nil {
				t.Fatal(err)
			}
			body := &pausedBody{Reader: strings.NewReader(c.body), entered: make(chan struct{}), release: make(chan struct{})}
			req := httptest.NewRequest("POST", "/v1/needs-you", body)
			req.Header.Set("Authorization", "Bearer "+lead.token)
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); f.s.Handler().ServeHTTP(w, req) }()
			select {
			case <-body.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the request did not authenticate")
			}
			switch c.action {
			case "restart":
				f.s.Agents.Stop("alice/lead")
				f.start(t, "alice/lead")
			case "rehire":
				if _, err := f.s.Yesmen.Archive("alice", func(yesmen.Yesman) error { return f.s.Agents.StopYesman("alice") }); err != nil {
					t.Fatal(err)
				}
				f.s.Agents.ForgetYesman("alice", f.y.HiredAt)
				if _, err := f.s.Yesmen.Hire("alice"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.Agents.Authenticate(lead.token); err == nil {
				t.Fatal("the old credential still authenticates")
			}
			close(body.release)
			<-done
			hs, err := f.s.Yesmen.Handoffs("alice")
			if err != nil {
				t.Fatal(err)
			}
			if w.Code == 200 {
				t.Errorf("the retired request succeeded: %s", w.Body)
			}
			want := 1 // the first handoff, still open
			if c.action == "rehire" {
				want = 0 // the new yesman starts with none
			}
			if len(hs.Items) != want || len(hs.Open()) != want {
				t.Errorf("handoffs after the retired request: %+v", hs.Items)
			}
		})
	}
}
