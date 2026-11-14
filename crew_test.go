package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/crew"
	"github.com/idy/yesman/internal/home"
	"github.com/idy/yesman/internal/yesmen"
)

// crewApp is an app whose pi has Claude Opus 5.5 at low, xhigh and max,
// which the embedded scores know at all three, and a model they do not
// know.
func crewApp(t *testing.T) (*app, *atomic.Int32) {
	t.Helper()
	h := home.Home{Root: t.TempDir()}
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	reads := new(atomic.Int32)
	a := &app{home: h, yesmen: yesmen.New(filepath.Join(h.Root, "yesmen"), filepath.Join(h.Root, "archive"))}
	a.catalog = newCatalog(func(context.Context) ([]agent.Model, error) {
		reads.Add(1)
		return []agent.Model{
			{Provider: "magpie", ID: "auto/claude-opus-5-5", Name: "Claude Opus 5.5 · routing group", Levels: []string{"low", "xhigh", "max"}},
			{Provider: "magpie", ID: "local/tiny", Name: "Tiny", Levels: []string{"off"}},
		}, nil
	})
	return a, reads
}

func memberKeys(members []crew.Member) []string {
	var out []string
	for _, m := range members {
		out = append(out, m.Key)
	}
	return out
}

const (
	opusLow  = "magpie/auto/claude-opus-5-5:low"
	opusMax  = "magpie/auto/claude-opus-5-5:max"
	tinyOff  = "magpie/local/tiny:off"
	tinyName = "local/tiny"
)

func TestCrewView(t *testing.T) {
	a, reads := crewApp(t)
	s := &service{app: a}
	ctx := context.Background()
	v := s.Crew(ctx)
	if v.Error != "" || v.Date != "2026-11-14" || v.ReadAt.IsZero() || v.Source == "" || v.Top != 64.6 || len(v.Members) != 0 || v.Yesman != "" {
		t.Fatalf("view = %+v", v)
	}
	if len(v.Models) != 2 || !slices.Equal(v.Models[0].Scored, []string{"low", "xhigh", "max"}) || len(v.Models[1].Scored) != 0 {
		t.Errorf("models = %+v", v.Models)
	}
	s.Crew(ctx)
	if reads.Load() != 1 {
		t.Errorf("read the catalog %d times", reads.Load())
	}
	s.RefreshCrew(ctx)
	if reads.Load() != 2 {
		t.Errorf("refreshing read the catalog %d times", reads.Load())
	}
}

func TestCrewOptions(t *testing.T) {
	a, _ := crewApp(t)
	s := &service{app: a}
	ctx := context.Background()
	f := func(v float64) *float64 { return &v }

	for _, c := range []struct {
		provider, model, effort, want string
		score                         *crew.Score
	}{
		{"magpie", "nope", "low", "pi has no model", nil},
		{"magpie", "auto/claude-opus-5-5", "high", "does not take the high effort", nil},
		{"magpie", tinyName, "off", "give your own", nil},
		{"magpie", tinyName, "off", "intelligence score", &crew.Score{Intelligence: 0}},
	} {
		if _, err := s.AddCrewOption(ctx, c.provider, c.model, c.effort, c.score); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("adding %s:%s: %v, want %q", c.model, c.effort, err, c.want)
		}
	}

	// The first option is what new yesmen run as. A scored option keeps no
	// score of the user's.
	v, err := s.AddCrewOption(ctx, "magpie", "auto/claude-opus-5-5", "max", &crew.Score{Intelligence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if v.Yesman != opusMax || len(v.Members) != 1 || v.Members[0].Source != crew.SourceAA || v.Members[0].Score.Intelligence != 59.1 {
		t.Fatalf("after adding opus: %+v", v)
	}
	if _, err := s.AddCrewOption(ctx, "magpie", "auto/claude-opus-5-5", "max", nil); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("added it twice: %v", err)
	}
	if _, err := s.AddCrewOption(ctx, "magpie", "auto/claude-opus-5-5", "low", nil); err != nil {
		t.Fatal(err)
	}
	v, err = s.AddCrewOption(ctx, "magpie", tinyName, "off", &crew.Score{Name: "ignored", Intelligence: 12.5, Speed: f(300)})
	if err != nil {
		t.Fatal(err)
	}
	if got := memberKeys(v.Members); !slices.Equal(got, []string{opusMax, opusLow, tinyOff}) {
		t.Errorf("crew = %v", got)
	}
	tiny := v.Members[2]
	if tiny.Source != crew.SourceUser || tiny.Score.Intelligence != 12.5 || *tiny.Score.Speed != 300 || tiny.Score.Name != "" {
		t.Errorf("tiny = %+v %+v", tiny, tiny.Score)
	}
	if v, err = s.SetCrewScore(ctx, tinyOff, crew.Score{Intelligence: 20}); err != nil || v.Members[2].Score.Intelligence != 20 || v.Members[2].Score.Speed != nil {
		t.Errorf("rescoring: %+v, %v", v.Members[2].Score, err)
	}
	if _, err := s.SetCrewScore(ctx, "magpie/x:low", crew.Score{Intelligence: 20}); err == nil {
		t.Error("scored an option not in the crew")
	}

	// What new yesmen run as.
	if _, err := s.SetYesmanModel(ctx, "magpie/x:low"); err == nil {
		t.Error("new yesmen run as an option not in the crew")
	}
	if v, err = s.SetYesmanModel(ctx, opusLow); err != nil || v.Yesman != opusLow {
		t.Errorf("choosing: %q, %v", v.Yesman, err)
	}
	if key, err := a.yesmanModel(ctx); err != nil || key != opusLow {
		t.Errorf("a new yesman runs as %q, %v", key, err)
	}

	// Removing it makes new yesmen run as the first option left.
	if v, err = s.RemoveCrewOption(ctx, opusLow); err != nil || v.Yesman != opusMax || len(v.Members) != 2 {
		t.Errorf("removing: %+v, %v", v, err)
	}
	if _, err := s.RemoveCrewOption(ctx, opusLow); err == nil {
		t.Error("removed it twice")
	}
	saved := loadSettings(a.home.Settings()).Crew
	if saved.Yesman != opusMax || len(saved.Options) != 2 || saved.Options[1].Score == nil || saved.Options[1].Score.Intelligence != 20 {
		t.Errorf("saved %+v", saved)
	}
	s.RemoveCrewOption(ctx, opusMax)
	if v, _ := s.RemoveCrewOption(ctx, tinyOff); v.Yesman != "" || len(v.Members) != 0 {
		t.Errorf("an empty crew: %+v", v)
	}
}

func TestHireRunsAsTheYesmanModel(t *testing.T) {
	a, _ := crewApp(t)
	s := &service{app: a}
	ctx := context.Background()
	if _, err := s.Hire(ctx, "bob"); err == nil || !strings.Contains(err.Error(), "Settings › yesman") {
		t.Errorf("hired with no model chosen: %v", err)
	}
	if _, err := a.yesmen.Get("bob"); err == nil {
		t.Error("a refused hire left a workspace")
	}
	if _, err := s.AddCrewOption(ctx, "magpie", "auto/claude-opus-5-5", "xhigh", nil); err != nil {
		t.Fatal(err)
	}
	if key, err := a.yesmanModel(ctx); err != nil || key != "magpie/auto/claude-opus-5-5:xhigh" {
		t.Errorf("a new yesman runs as %q, %v", key, err)
	}
	// An option pi no longer has runs nothing.
	a.catalog = newCatalog(func(context.Context) ([]agent.Model, error) { return []agent.Model{}, nil })
	if _, err := a.yesmanModel(ctx); err == nil {
		t.Error("a new yesman runs as a model pi lacks")
	}
	if members := a.leadCrew(ctx); len(members) != 1 || !members[0].Missing {
		t.Errorf("lead's crew = %+v", members)
	}
}

func TestCatalogRetriesAfterFailing(t *testing.T) {
	fail := true
	reads := 0
	c := newCatalog(func(context.Context) ([]agent.Model, error) {
		reads++
		if fail {
			return nil, errors.New("no pi")
		}
		return []agent.Model{{Provider: "p", ID: "m", Levels: []string{"off"}}}, nil
	})
	ctx := context.Background()
	if models, _, err := c.get(ctx, false); err == nil || models != nil {
		t.Fatal("a failed read said nothing")
	}
	fail = false
	models, at, err := c.get(ctx, false)
	if err != nil || len(models) != 1 || at.IsZero() || reads != 2 || models[0].Levels[0] != "off" {
		t.Errorf("after pi came back: %v %v %v, %d reads", models, at, err, reads)
	}
	// A caller that gives up leaves the read going for the next.
	slow := make(chan struct{})
	c = newCatalog(func(context.Context) ([]agent.Model, error) { <-slow; return nil, nil })
	short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, _, err := c.get(short, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("an impatient get: %v", err)
	}
	close(slow)
	if models, at, err := c.get(ctx, false); err != nil || at.IsZero() || models == nil {
		t.Errorf("the read did not finish: %v", err)
	}
}
