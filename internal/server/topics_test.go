package server

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

type topicFixture struct {
	s       *Server
	alice   yesmen.Yesman
	bob     yesmen.Yesman
	changed []string
}

func newTopicFixture(t *testing.T) *topicFixture {
	t.Helper()
	dir := t.TempDir()
	store := yesmen.New(filepath.Join(dir, "yesmen"), filepath.Join(dir, "archive"))
	alice, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Hire("bob")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddWorker("alice", yesmen.Worker{Name: "helper", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	m := agent.New(agent.Config{Yesmen: store})
	t.Cleanup(m.StopAll)
	f := &topicFixture{alice: alice, bob: bob}
	f.s = &Server{Yesmen: store, Agents: m, TopicsChanged: func(y string) { f.changed = append(f.changed, y) }}
	return f
}

func (f *topicFixture) caller(t *testing.T, y yesmen.Yesman, name string) caller {
	t.Helper()
	i, err := f.s.Agents.Get(agent.ID(y.Name, name))
	if err != nil {
		t.Fatal(err)
	}
	return caller{agent: i, yesman: y}
}

func (f *topicFixture) post(t *testing.T, c caller, body string) (any, error) {
	t.Helper()
	return f.s.changeTopics(context.Background(), c, httptest.NewRequest("POST", "/v1/topics", strings.NewReader(body)))
}

func TestTopicsAPIIsLeadOnlyAndScoped(t *testing.T) {
	f := newTopicFixture(t)
	lead, worker, otherLead := f.caller(t, f.alice, "lead"), f.caller(t, f.alice, "helper"), f.caller(t, f.bob, "lead")

	if _, err := f.post(t, worker, `{"create":{"title":"from a worker"}}`); !errors.Is(err, errTopicsLeadOnly) {
		t.Fatalf("worker write: %v", err)
	}
	out, err := f.post(t, lead, `{"create":{"title":"Why topics?","questions":["Which entry?"],"sources":[{"kind":"worker","agent":"helper","label":"its report"},{"kind":"conversation"}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	tp := out.(yesmen.Topic)
	if tp.ID != "tp1" || tp.CreatedBy != yesmen.TopicByLead || len(f.changed) != 1 || f.changed[0] != "alice" {
		t.Fatalf("created %+v, changed %v", tp, f.changed)
	}
	helper, _ := f.s.Agents.Get(agent.ID("alice", "helper"))
	if !tp.Sources[0].Since.Equal(helper.Since) || tp.Sources[1].Agent != "lead" || !tp.Sources[1].Since.Equal(f.alice.HiredAt) {
		t.Fatalf("sources not tied to their generation: %+v", tp.Sources)
	}

	// A stale revision is refused with the current one; nothing changes.
	if _, err := f.post(t, lead, `{"id":"tp1","patch":{"revision":0,"title":"x"}}`); err == nil || !strings.Contains(err.Error(), "revision 1") {
		t.Fatalf("stale patch: %v", err)
	}
	out, err = f.post(t, lead, `{"id":"tp1","patch":{"revision":1,"addConclusions":["Use the info panel"],"removeQuestions":["q1"]}}`)
	if err != nil || out.(yesmen.Topic).Revision != 2 {
		t.Fatalf("patch %v %v", out, err)
	}
	for _, body := range []string{`{}`, `{"id":"tp1"}`, `{"create":{"title":"x"},"id":"tp1","patch":{"revision":2}}`, `{"id":"../../board","patch":{"revision":1}}`} {
		if _, err := f.post(t, lead, body); err == nil {
			t.Errorf("accepted %s", body)
		}
	}

	// Any agent of the workspace reads; another yesman sees only its own.
	got, err := f.s.topics(context.Background(), worker, httptest.NewRequest("GET", "/v1/topics?id=tp1", nil))
	if err != nil || got.(yesmen.Topic).Conclusions[0].Text != "Use the info panel" {
		t.Fatalf("worker read %v %v", got, err)
	}
	list, err := f.s.topics(context.Background(), otherLead, httptest.NewRequest("GET", "/v1/topics", nil))
	if err != nil || len(list.(map[string]any)["topics"].([]yesmen.Topic)) != 0 {
		t.Fatalf("bob sees %v %v", list, err)
	}
	if _, err := f.s.topics(context.Background(), otherLead, httptest.NewRequest("GET", "/v1/topics?id=tp1", nil)); err == nil {
		t.Fatal("bob read alice's topic")
	}
	if _, err := f.post(t, otherLead, `{"id":"tp1","patch":{"revision":2,"title":"hijack"}}`); err == nil {
		t.Fatal("bob changed alice's topic")
	}
}

func TestTopicsContextListsTitlesOnly(t *testing.T) {
	var topics []yesmen.Topic
	for i := 1; i <= 23; i++ {
		topics = append(topics, yesmen.Topic{ID: fmt.Sprintf("tp%d", i), Title: fmt.Sprintf("Open %d", i), Status: yesmen.TopicOpen, Summary: "SECRET-BODY", Questions: []yesmen.TopicItem{{ID: "q1", Text: "BODY-QUESTION"}}})
	}
	topics = append(topics, yesmen.Topic{ID: "tp30", Title: "Done", Status: yesmen.TopicResolved}, yesmen.Topic{ID: "tp31", Title: "Old", Status: yesmen.TopicArchived})
	text := TopicsContext(topics, nil)
	for _, want := range []string{"## Topics", "- tp1 Open 1 (1 open question)", "- tp20 Open 20", "… and 3 more open topics", "1 resolved and 1 archived", "[Topic tp3: title]", "never under conclusions", "not topics"} {
		if !strings.Contains(text, want) {
			t.Errorf("context lacks %q:\n%s", want, text)
		}
	}
	for _, leak := range []string{"SECRET-BODY", "BODY-QUESTION", "tp21 ", "Done", "Old"} {
		if strings.Contains(text, leak) {
			t.Errorf("context includes %q", leak)
		}
	}
	if text := TopicsContext(nil, nil); !strings.Contains(text, "No topics yet.") {
		t.Errorf("empty: %s", text)
	}
	if text := TopicsContext(nil, errors.New("topics.json cannot be read")); !strings.Contains(text, "cannot be read now") {
		t.Errorf("error: %s", text)
	}
}
