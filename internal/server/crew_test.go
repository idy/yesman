package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/crew"
)

func testCrew() []crew.Member {
	f := func(v float64) *float64 { return &v }
	return []crew.Member{
		{Key: "magpie/kimi:high", Provider: "magpie", Model: "kimi", Effort: "high", ModelName: "Kimi K3.1 Pro · China Magpie",
			Score: &crew.Score{Intelligence: 40}, Source: crew.SourceUser},
		{Key: "magpie/auto/claude-opus-5-5:high", Provider: "magpie", Model: "auto/claude-opus-5-5", Effort: "high", ModelName: "Claude Opus 5.5 · routing group",
			Score: &crew.Score{Name: "Claude Opus 5.5 (high)", Intelligence: 52.34, Speed: f(81.6), Coding: f(50.25), CostPerTask: f(3.1), MinutesPerTask: f(12.4)}, Source: crew.SourceAA},
		{Key: "magpie/gone:low", Provider: "magpie", Model: "gone", Effort: "low", Missing: true},
		{Key: "magpie/odd:low", Provider: "magpie", Model: "odd", Effort: "low"},
	}
}

func TestCrewContext(t *testing.T) {
	if got := CrewContext(nil, agent.Info{}); !strings.Contains(got, "The crew is empty") {
		t.Errorf("no crew says %q", got)
	}
	opus := agent.Info{Provider: "magpie", Model: "auto/claude-opus-5-5", Effort: "high"}
	text := CrewContext(testCrew(), opus)
	for _, want := range []string{
		"## Crew",
		"crew parameter of yesman_worker_start",
		"a worker runs as your own model, magpie/auto/claude-opus-5-5:high.",
		"- magpie/auto/claude-opus-5-5:high: Claude Opus 5.5 at high effort, intelligence 52.3, 82 tok/s, coding 50.2%, $3.10 per task, 12 min per task\n" +
			"- magpie/kimi:high: Kimi K3.1 Pro at high effort, intelligence 40.0 (the user's estimate)\n" +
			"- magpie/odd:low: odd at low effort (no scores)\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("crew context lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "gone") {
		t.Errorf("lists a model pi no longer has:\n%s", text)
	}
	text = CrewContext(testCrew(), agent.Info{Provider: "magpie", Model: "auto/claude-opus-5-5", Effort: "low"})
	if !strings.Contains(text, "your own model, magpie/auto/claude-opus-5-5:low, is not in the crew, so name a member for every worker") {
		t.Errorf("for a lead outside the crew:\n%s", text)
	}
}

func TestWorkerChoice(t *testing.T) {
	s := &Server{Crew: func(context.Context) []crew.Member { return testCrew() }}
	ctx := context.Background()
	opus := agent.Info{Provider: "magpie", Model: "auto/claude-opus-5-5", Effort: "high"}
	if c, err := s.workerChoice(ctx, opus, "magpie/kimi:high"); err != nil || c != (agent.Choice{Provider: "magpie", Model: "kimi", Effort: "high"}) {
		t.Errorf("a named member: %+v, %v", c, err)
	}
	// Without one, a worker runs as its lead, at the lead's effort.
	if c, err := s.workerChoice(ctx, opus, ""); err != nil || c != (agent.Choice{Provider: "magpie", Model: "auto/claude-opus-5-5", Effort: "high"}) {
		t.Errorf("the lead's own: %+v, %v", c, err)
	}
	for key, want := range map[string]string{"magpie/auto/claude-opus-5-5:max": "no crew member", "magpie/gone:low": "no crew member"} {
		if _, err := s.workerChoice(ctx, opus, key); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", key, err)
		}
	}
	if _, err := s.workerChoice(ctx, agent.Info{Provider: "magpie", Model: "auto/claude-opus-5-5", Effort: "low"}, ""); err == nil || !strings.Contains(err.Error(), "is not in the crew") {
		t.Errorf("a lead outside the crew: %v", err)
	}
	s.Crew = func(context.Context) []crew.Member { return testCrew()[2:3] }
	if _, err := s.workerChoice(ctx, opus, ""); err != errNoCrew {
		t.Errorf("an empty crew: %v", err)
	}
	// Without a crew, as before there was one.
	s.Crew = nil
	if c, err := s.workerChoice(ctx, opus, ""); err != nil || !c.IsZero() {
		t.Errorf("without a crew: %+v, %v", c, err)
	}
	if _, err := s.workerChoice(ctx, opus, "magpie/kimi:high"); err == nil {
		t.Error("a member without a crew")
	}
}

func TestStartWorkerChecksItsCrew(t *testing.T) {
	s := &Server{Crew: func(context.Context) []crew.Member { return testCrew() }}
	r := httptest.NewRequest("POST", "/v1/workers", strings.NewReader(`{"name":"w1","title":"Build","task":"a task","crew":"magpie/nope"}`))
	_, err := s.startWorker(context.Background(), caller{agent: agent.Info{Role: agent.RoleLead}}, r)
	if err == nil || !strings.Contains(err.Error(), `no crew member "magpie/nope"`) {
		t.Errorf("an unknown member: %v", err)
	}
}
