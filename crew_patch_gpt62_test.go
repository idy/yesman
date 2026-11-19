package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/crew"
	"github.com/idy/yesman/internal/server"
)

const (
	patchAstraID = "codex-remote/codex/gpt-6.2-astra"
	patchSolID   = "codex-remote/codex/gpt-6.2-sol"
	gpt62Patch   = crew.PatchHeader +
		"synthetic," + patchAstraID + ",high,0,,,0,\n" +
		"synthetic," + patchSolID + ",high,,123.5,,,0\n" +
		"synthetic,local/patch-only,off,,37.125,,,0\n"
)

// Shared by the service regression and production-page fixture. No live catalog.
func configureGPT62PatchCrew(t *testing.T, a *app) {
	t.Helper()
	f := func(v float64) *float64 { return &v }
	estimate := &crew.Score{Intelligence: 12, Speed: f(13), Coding: f(14), CostPerTask: f(15), MinutesPerTask: f(16)}
	a.catalog = newCatalog(func(context.Context) ([]agent.Model, error) {
		return []agent.Model{
			{Provider: "synthetic", ID: patchAstraID, Name: "GPT-6.2 Astra", Levels: []string{"high", "xhigh", "off"}},
			{Provider: "synthetic", ID: patchSolID, Name: "GPT-6.2 Sol", Levels: []string{"high", "xhigh", "off"}},
			{Provider: "other", ID: patchAstraID, Name: "GPT-6.2 Astra", Levels: []string{"high"}},
			{Provider: "synthetic", ID: "another-route/gpt-6.2-astra", Name: "GPT-6.2 Astra", Levels: []string{"high"}},
			{Provider: "synthetic", ID: "local/patch-only", Name: "Patch only", Levels: []string{"off"}},
		}, nil
	})
	if err := a.changeCrew(func(c *crew.Config) error {
		*c = crew.Config{Options: []crew.Option{
			{Provider: "synthetic", Model: patchAstraID, Effort: "high", Score: estimate},
			{Provider: "synthetic", Model: patchAstraID, Effort: "xhigh"},
			{Provider: "synthetic", Model: patchSolID, Effort: "high", Score: estimate},
			{Provider: "synthetic", Model: patchSolID, Effort: "xhigh"},
			{Provider: "other", Model: patchAstraID, Effort: "high"},
			{Provider: "synthetic", Model: "another-route/gpt-6.2-astra", Effort: "high"},
			{Provider: "synthetic", Model: patchAstraID, Effort: "off", Score: estimate},
			{Provider: "synthetic", Model: patchSolID, Effort: "off"},
			{Provider: "synthetic", Model: "local/patch-only", Effort: "off"},
		}, Yesman: "synthetic/" + patchAstraID + ":high"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestScorePatchGPT62Composition(t *testing.T) {
	a, _ := crewApp(t)
	configureGPT62PatchCrew(t, a)
	ctx := context.Background()
	s := &service{app: a}
	configBefore := a.crewConfig()
	source := filepath.Join(t.TempDir(), "input.csv")
	if err := os.WriteFile(source, []byte(gpt62Patch), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.importScorePatch(source); err != nil {
		t.Fatal(err)
	}
	settingsBefore, err := os.ReadFile(a.home.Settings())
	if err != nil {
		t.Fatal(err)
	}
	checkBytes := func() {
		t.Helper()
		patch, err := os.ReadFile(a.scorePatchPath())
		if err != nil || string(patch) != gpt62Patch {
			t.Fatalf("patch bytes: %q, %v", patch, err)
		}
		settings, err := os.ReadFile(a.home.Settings())
		if err != nil || string(settings) != string(settingsBefore) {
			t.Fatalf("settings changed: %v", err)
		}
	}
	// Resolve the same persisted bytes against the historical base, then the
	// actual new Embedded snapshot. Do not replace Embedded with a fixture.
	oldCSV, err := os.ReadFile("internal/crew/scores/aa-top50-scores-2026-11-14.csv")
	if err != nil {
		t.Fatal(err)
	}
	oldScores, err := crew.Parse(string(oldCSV))
	if err != nil {
		t.Fatal(err)
	}
	patch, _ := a.effectiveScorePatch()
	models, _, err := a.catalog.get(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	oldMembers := crew.Resolve(configBefore, models, crew.Snapshot{Scores: oldScores}, patch)
	if oldMembers[0].Score.Intelligence != 0 || *oldMembers[0].Score.Speed != 13 || oldMembers[0].FieldSource("speed") != crew.SourceUser || oldMembers[1].Score != nil {
		t.Fatalf("old-base control: %+v", oldMembers)
	}
	want := [][5]float64{
		{0, 60.7, 53.6, 0, 4.9}, {61.6, 59.9, 56.5, 2.52, 6.2},
		{54.9, 123.5, 49.4, .56, 0}, {55.7, 80.2, 49.4, .65, 5.1},
		{60.1, 60.7, 53.6, 2.07, 4.9}, {60.1, 60.7, 53.6, 2.07, 4.9},
		{12, 13, 14, 15, 16},
	}
	check := func(v CrewView) {
		t.Helper()
		if v.Error != "" || v.Patch.Error != "" || v.Date != "2026-11-18" || len(v.Models) != 5 || len(v.Members) != 9 || v.Yesman != configBefore.Yesman {
			t.Fatalf("view: %+v", v)
		}
		if !reflect.DeepEqual(v.Members, a.leadCrew(ctx)) || !reflect.DeepEqual(a.crewConfig(), configBefore) {
			t.Fatal("Models/Lead disagree or stored estimate/options/default changed")
		}
		for i, w := range want {
			m := v.Members[i]
			if m.Missing || m.Score == nil || m.Score.IntelligenceMissing || m.Score.Speed == nil || m.Score.Coding == nil || m.Score.CostPerTask == nil || m.Score.MinutesPerTask == nil {
				t.Fatalf("member %d unavailable: %+v", i, m)
			}
			sc := m.Score
			got := [5]float64{sc.Intelligence, *sc.Speed, *sc.Coding, *sc.CostPerTask, *sc.MinutesPerTask}
			if got != w {
				t.Fatalf("member %s: %v, want %v", m.Key, got, w)
			}
			for _, field := range []string{"intelligence", "speed", "coding", "costPerTask", "minutesPerTask"} {
				origin := crew.SourceAA
				if i == 6 {
					origin = crew.SourceUser
				} else if i == 0 && (field == "intelligence" || field == "costPerTask") || i == 2 && (field == "speed" || field == "minutesPerTask") {
					origin = crew.SourcePatch
				}
				if m.FieldSource(field) != origin {
					t.Fatalf("%s %s source: %q", m.Key, field, m.FieldSource(field))
				}
			}
			if i < 6 && sc.Effort != m.Effort {
				t.Fatalf("lost source effort: %+v", m)
			}
		}
		if v.Members[7].Score != nil || !reflect.DeepEqual(v.Members[8], oldMembers[8]) || !v.Members[8].Score.IntelligenceMissing {
			t.Fatal("unmeasured effort borrowed AA or patch-only row changed")
		}
		text := server.CrewContext(a.leadCrew(ctx), agent.Info{})
		for _, fragment := range []string{"intelligence 0 (user CSV), speed 60.7 tok/s (inherited AA), coding 53.6% (inherited AA), cost 0 USD per task (user CSV), time 4.9 min per task (inherited AA)", "intelligence 54.9 (inherited AA), speed 123.5 tok/s (user CSV), coding 49.4% (inherited AA), cost 0.56 USD per task (inherited AA), time 0 min per task (user CSV)", "AA source effort: high", "AA source effort: xhigh", "intelligence unavailable, speed 37.12 tok/s (user CSV)"} {
			if !strings.Contains(text, fragment) {
				t.Fatalf("Lead lacks %q: %s", fragment, text)
			}
		}
		checkBytes()
	}
	check(s.Crew(ctx))
	check(s.RefreshCrew(ctx))
	for _, bad := range []string{gpt62Patch + "synthetic," + patchAstraID + ",high,9,,,,\n", crew.PatchHeader + "synthetic," + patchSolID + ",high,,invalid,,,\n"} {
		if err := os.WriteFile(source, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if err := a.importScorePatch(source); err == nil || !strings.Contains(err.Error(), "row ") {
			t.Fatalf("invalid replacement: %v", err)
		}
		original, err := os.ReadFile(source)
		if err != nil || string(original) != bad {
			t.Fatal("changed import source")
		}
		check(s.Crew(ctx))
	}
	disabled, err := s.SetScorePatchEnabled(ctx, false)
	if err != nil || disabled.Members[0].Score.Intelligence != 60.1 || *disabled.Members[2].Score.Speed != 79.3 || disabled.Members[8].Score != nil {
		t.Fatalf("disable: %+v, %v", disabled, err)
	}
	a = &app{home: a.home, catalog: a.catalog, settings: loadSettings(a.home.Settings())}
	s = &service{app: a}
	if s.Crew(ctx).Patch.Enabled {
		t.Fatal("disabled patch re-enabled on restart")
	}
	if _, err := s.SetScorePatchEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	a = &app{home: a.home, catalog: a.catalog, settings: loadSettings(a.home.Settings())}
	s = &service{app: a}
	check(s.Crew(ctx))
}
