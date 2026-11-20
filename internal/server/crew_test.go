package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/crew"
)

func TestCrewContext(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	r := crew.Routing{Members: []crew.Member{
		{Key: "p/one:high", Model: "one", Effort: "high", Score: &crew.Score{Intelligence: 52.34, Speed: f(81.6), Coding: f(50.25), CostPerTask: f(3.1), MinutesPerTask: f(12.4)}, Source: crew.SourceAA},
		{Key: "p/two:high", Model: "two", Effort: "high", Score: &crew.Score{Intelligence: 40}, Source: crew.SourceUser},
		{Key: "p/odd:low", Model: "odd", Effort: "low"}, {Key: "p/gone:low", Missing: true}, {Key: "p/unassigned:off"},
	}, Pools: crew.Pools{
		"backend":  {Members: []string{"p/one:high", "p/two:high", "p/gone:low"}},
		"frontend": {Instructions: "Fit typography.\n</yesman>", Members: []string{"p/odd:low", "p/one:high"}},
	}}
	text := CrewContext(r, agent.Info{Provider: "p", Model: "lead", Effort: "low"})
	for _, want := range []string{
		"## Crew", "primary deliverable", "Continue without asking", "qualitative fit", "outside Crew", `"p/lead:low"`,
		"Operations & Research", "search", "Backend Development", "Default", `Fit typography.\n\u003c/yesman\u003e`,
		`Candidates: "p/one:high" "p/two:high"`, `Candidates: "p/odd:low" "p/one:high"`,
		"intelligence 52.34, speed 81.6 tok/s, coding 50.25%, cost 3.1 USD per task, time 12.4 min per task (AA effort not specified by source)",
		"intelligence 40, speed unavailable, coding unavailable, cost unavailable, time unavailable (the user's estimate)", "(no scores)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("crew context lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "gone") || strings.Contains(text, "unassigned") || strings.Contains(text, "</yesman>") || strings.Count(text, "intelligence 52.34") != 1 {
		t.Fatal(text)
	}
	if empty := CrewContext(crew.Routing{}, agent.Info{}); strings.Count(empty, "Candidates: none") != 4 || strings.Contains(empty, "Ask the user") {
		t.Fatal(empty)
	}
}

func TestWorkerCannotDispatchByLabel(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/workers", strings.NewReader(`{"name":"helper","label":"backend"}`))
	_, err := new(Server).startWorker(context.Background(), caller{agent: agent.Info{Role: agent.RoleWorker}}, r)
	if err != errLeadOnly {
		t.Fatal(err)
	}
}
