package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/crew"
)

func TestWorkerPoolPersistenceAndAdmission(t *testing.T) {
	a, _ := crewApp(t)
	ctx := context.Background()
	s := &service{app: a}
	if v := s.Crew(ctx); len(v.Pools) != 4 || len(a.crewConfig().Pools) != 0 {
		t.Fatal(v.Pools)
	}
	for _, o := range []crew.Option{{Provider: "magpie", Model: "auto/claude-opus-5-5", Effort: "low"}, {Provider: "magpie", Model: "auto/claude-opus-5-5", Effort: "max"}, {Provider: "magpie", Model: tinyName, Effort: "off"}} {
		_, err := s.AddCrewOption(ctx, o.Provider, o.Model, o.Effort, nil)
		must(t, err)
	}
	if m := s.Crew(ctx).Members[2]; m.Score != nil || m.Missing {
		t.Fatalf("unscored: %+v", m)
	}
	set := func(id string, p crew.Pool) error {
		_, err := s.SetWorkerPool(ctx, id, a.crewConfig().Pools[id].Revision(), p)
		return err
	}
	must(t, set("backend", crew.Pool{Instructions: "Use for APIs.\n选择后台。", Members: []string{opusLow, opusMax, opusLow}}))
	old := a.crewConfig()
	if !slices.Equal(old.Pools["backend"].Members, []string{opusLow, opusMax}) {
		t.Fatal(old.Pools)
	}
	must(t, set("default", crew.Pool{Members: []string{tinyOff}}))
	_, err := s.SetYesmanModel(ctx, opusMax)
	must(t, err)
	must(t, s.SetAppearance("dark"))
	if got := loadSettings(a.home.Settings()); got.Appearance != "dark" || got.Crew.Yesman != opusMax || got.Crew.Pools["backend"].Revision() != old.Pools["backend"].Revision() {
		t.Fatalf("cold settings %+v", got)
	}
	check := func(label, preferred, want string) {
		t.Helper()
		r := a.workerRouting(ctx, nil)
		m, err := r.Select(label, preferred)
		if err != nil || m.Key != want {
			t.Fatalf("%q %q => %+v %v; want %q", label, preferred, m, err, want)
		}
	}
	check("backend", opusMax, opusMax)
	check("backend", tinyOff, opusLow)
	check("operations", opusMax, tinyOff)
	check("unknown", opusMax, tinyOff)
	check("", opusLow, tinyOff)
	// A new worker may see a narrower catalog than the Settings probe.
	r := a.workerRouting(ctx, []agent.Model{{Provider: "magpie", ID: "auto/claude-opus-5-5", Levels: []string{"max"}}})
	m, err := r.Select("backend", opusLow)
	if err != nil || m.Key != opusMax {
		t.Fatal(m, err)
	}
	r = a.workerRouting(ctx, []agent.Model{})
	m, err = r.Select("backend", opusLow)
	if err != nil || m.Key != "" {
		t.Fatal(m, err)
	}
	// An unavailable saved member can be removed. It cannot be newly assigned.
	a.catalog = newCatalog(func(context.Context) ([]agent.Model, error) {
		return []agent.Model{{Provider: "magpie", ID: tinyName, Levels: []string{"off"}}}, nil
	})
	s.RefreshCrew(ctx)
	check("backend", opusMax, tinyOff)
	before, err := os.ReadFile(a.home.Settings())
	must(t, err)
	for _, bad := range []struct {
		id string
		p  crew.Pool
	}{
		{"other", crew.Pool{}}, {"frontend", crew.Pool{Members: []string{opusLow}}}, {"frontend", crew.Pool{Members: []string{"magpie/local/tiny:high"}}},
		{"frontend", crew.Pool{Instructions: strings.Repeat("画", 1001)}}, {"frontend", crew.Pool{Instructions: "a\x00b"}}, {"frontend", crew.Pool{Members: make([]string, crew.MaxPoolMembers+1)}},
	} {
		if err := set(bad.id, bad.p); err == nil {
			t.Fatal("accepted invalid pool", bad.id)
		}
	}
	after, err := os.ReadFile(a.home.Settings())
	must(t, err)
	if !bytes.Equal(before, after) {
		t.Fatal("refused edit rewrote settings")
	}
	v, err := s.RemoveCrewOption(ctx, opusMax)
	must(t, err)
	if v.Yesman != opusLow || !slices.Equal(v.Pools[1].Members, []string{opusLow}) || len(old.Pools["backend"].Members) != 2 {
		t.Fatal("removed member or mutated prior snapshot", v.Pools)
	}
	if _, err = s.SetWorkerPool(ctx, "backend", old.Pools["backend"].Revision(), old.Pools["backend"]); err == nil {
		t.Fatal("stale editor revived removed option")
	}
	v, err = s.RemoveCrewOption(ctx, tinyOff)
	must(t, err)
	check("backend", opusLow, "")
	if len(v.Pools[3].Members) != 0 {
		t.Fatal(v.Pools)
	}
	// If the very first catalog read fails, an unknown catalog is never an
	// affirmative membership grant, either in Settings or lead context.
	a.catalog = newCatalog(func(context.Context) ([]agent.Model, error) { return nil, errors.New("offline") })
	if v = s.Crew(ctx); v.Error == "" || !v.Members[0].Missing {
		t.Fatal(v)
	}
	check("backend", opusLow, "")
}

func TestWorkerPoolConcurrentEditors(t *testing.T) {
	a, _ := crewApp(t)
	s := &service{app: a}
	ctx := context.Background()
	initial := s.Crew(ctx).Pools[1]
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, value := range []string{"First rule", "Second rule"} {
		wg.Go(func() {
			<-start
			_, err := s.SetWorkerPool(ctx, initial.ID, initial.Revision, crew.Pool{Instructions: value})
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	count := 0
	for err := range results {
		if err == nil {
			count++
		} else if !strings.Contains(err.Error(), "pool changed") {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("%d edits won", count)
	}
	if p := loadSettings(a.home.Settings()).Crew.Pools[initial.ID]; p.Instructions == "" || p.Revision() == initial.Revision {
		t.Fatal(p)
	}
}

func TestLegacyWorkerChoice(t *testing.T) {
	const key = "p/route/model:high"
	flat := crew.Routing{Members: []crew.Member{{Key: key, Provider: "p", Model: "route/model", Effort: "high"}}}
	m, err := flat.Select("", key)
	if err != nil || m.Key != key {
		t.Fatal(m, err)
	}
	if _, err = flat.Select("", "absent"); err == nil {
		t.Fatal("legacy explicit unknown key accepted")
	}
	for _, label := range []string{"", "backend", "default", "unknown"} {
		m, err := flat.Select(label, "")
		if err != nil || m.Key != "" {
			t.Fatal(m, err)
		}
	}
	if m, err = flat.Select("backend", key); err != nil || m.Key != "" {
		t.Fatal("unassigned member admitted", m, err)
	}
	flat.Pools = crew.Pools{"backend": {Members: []string{key}}}
	if m, err = flat.Select("", key); err != nil || m.Key != "" {
		t.Fatal("legacy caller bypassed configured Default", m, err)
	}
}
