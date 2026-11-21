package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/crew"
	"github.com/idy/yesman/internal/home"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/server"
	"github.com/idy/yesman/internal/yesmen"
)

// The production tool calls the Go server and a real worker sends each task
// to a local provider. Check the model and reasoning on the provider call itself.
func TestWorkerRoutingWithPi(t *testing.T) {
	runtimeDir := os.Getenv("YESMAN_TEST_RUNTIME")
	if runtimeDir == "" || os.Getenv("YESMAN_TEST_WORKER_ROUTING_PI") != "1" {
		t.Skip("set YESMAN_TEST_WORKER_ROUTING_PI=1 and YESMAN_TEST_RUNTIME to a prepared runtime")
	}
	root, err := os.Getwd()
	must(t, err)
	h := home.Home{Root: t.TempDir()}
	must(t, h.Ensure())
	config := filepath.Join(h.Root, "fixture-pi")
	must(t, os.MkdirAll(config, 0700))
	settings, err := json.Marshal(map[string]any{"extensions": []string{filepath.Join(root, "scripts", "fixtures", "worker-routing-provider.ts")}, "packages": []string{}, "defaultProjectTrust": "no", "defaultProvider": "routing-fixture", "defaultModel": "lead", "defaultThinkingLevel": "medium", "compaction": map[string]bool{"enabled": false}, "retry": map[string]bool{"enabled": false}})
	must(t, err)
	must(t, os.WriteFile(filepath.Join(config, "settings.json"), settings, 0600))
	node, err := exec.LookPath("node")
	must(t, err)
	cli := filepath.Join(runtimeDir, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "cli.js")
	audit, gate := filepath.Join(h.Root, "calls.jsonl"), filepath.Join(h.Root, "release")
	store := yesmen.New(h.Yesmen(), h.Archive())
	y, err := store.Hire("lab")
	must(t, err)
	srv := &server.Server{Pool: pool.New(h.Repos()), Yesmen: store}
	must(t, srv.Start())
	cfg := agent.Config{Pi: node, Command: func(context.Context) (string, []string, error) { return node, []string{cli}, nil }, Extension: filepath.Join(root, "pi", "yesman.ts"), Yesmen: store,
		Env: append(isolatedRecallBridgeEnv(), "HOME="+h.Root, "USERPROFILE="+h.Root, "PI_CODING_AGENT_DIR="+config, "YESMAN_API="+srv.URL(), "ROUTING_AUDIT="+audit, "ROUTING_GATE="+gate), OnEvent: func(agent.Event) {}}
	a := &app{home: h, yesmen: store, agents: agent.New(cfg)}
	a.catalog = newCatalog(func(ctx context.Context) ([]agent.Model, error) { return a.agents.Catalog(ctx, y.Path) })
	srv.Agents, srv.Routing = a.agents, a.workerRouting
	s := &service{app: a}
	defer func() { a.agents.StopAll() }()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	const provider = "routing-fixture"
	const build = "route/build:a"
	const low = provider + "/" + build + ":low"
	const high = provider + "/" + build + ":high"
	const fallback = provider + "/fallback:low"
	for _, o := range []crew.Option{{Provider: provider, Model: build, Effort: "low"}, {Provider: provider, Model: build, Effort: "high"}, {Provider: provider, Model: "fallback", Effort: "low"}} {
		_, err := s.AddCrewOption(ctx, o.Provider, o.Model, o.Effort, nil)
		must(t, err)
	}
	patch := filepath.Join(h.Root, "fixture.csv")
	must(t, os.WriteFile(patch, []byte(crew.PatchHeader+provider+","+build+",high,,,89,,\n"), 0600))
	must(t, a.importScorePatch(patch))
	setPool := func(id, instructions string, members ...string) {
		t.Helper()
		_, err := s.SetWorkerPool(ctx, id, a.crewConfig().Pools[id].Revision(), crew.Pool{Instructions: instructions, Members: members})
		must(t, err)
	}
	setPool("backend", "Database deliverables (fixture guidance)", low, high)
	setPool("default", "", fallback)
	// The saved profile survives a cold open whose Settings catalog probe
	// fails. Worker processes still report their own live catalog at dispatch.
	cold := &app{home: h, settings: loadSettings(h.Settings()), catalog: newCatalog(func(context.Context) ([]agent.Model, error) {
		return nil, errors.New("fixture catalog probe failed")
	})}
	var probeDown atomic.Bool
	verified := a.workerRouting
	srv.Routing = func(ctx context.Context, live []agent.Model) crew.Routing {
		if live == nil && probeDown.Load() {
			return cold.workerRouting(ctx, nil)
		}
		return verified(ctx, live)
	}
	lead := agent.ID(y.Name, yesmen.LeadName)
	controls, err := s.LeadModels(ctx, y.Name, y.HiredAt, agent.Choice{}, agent.Choice{})
	must(t, err)
	original := agent.Choice{Provider: controls.Agent.Provider, Model: controls.Agent.Model, Effort: controls.Agent.Effort}
	controls, err = s.LeadModels(ctx, y.Name, y.HiredAt, original, agent.Choice{Provider: provider, Model: "interactive"})
	must(t, err)
	if _, err = s.LeadModels(ctx, y.Name, y.HiredAt, original, agent.Choice{Effort: "low"}); err == nil {
		t.Fatal("stale model editor changed the new selection")
	}
	current := agent.Choice{Provider: controls.Agent.Provider, Model: controls.Agent.Model, Effort: controls.Agent.Effort}
	if _, err = s.LeadModels(ctx, y.Name, y.HiredAt.Add(-time.Second), current, agent.Choice{Effort: "low"}); err == nil {
		t.Fatal("stale workspace generation changed the lead")
	}
	_, err = s.LeadModels(ctx, y.Name, y.HiredAt, current, agent.Choice{Effort: "high"})
	must(t, err)
	type entry struct {
		Agent, Provider, Model, Effort, Text, Route string
		HasTool, HasLabel                           bool
	}
	entries := func() []entry {
		data, _ := os.ReadFile(audit)
		var out []entry
		for _, line := range strings.Split(string(data), "\n") {
			var e entry
			if json.Unmarshal([]byte(line), &e) == nil {
				out = append(out, e)
			}
		}
		return out
	}
	wait := func(what string, pred func() bool) {
		t.Helper()
		deadline := time.Now().Add(22 * time.Second)
		for !pred() {
			if ctx.Err() != nil || time.Now().After(deadline) {
				t.Fatalf("waiting for %s: agents=%+v calls=%+v", what, a.agents.List(), entries())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	start := func(name, label, key string) {
		t.Helper()
		args := map[string]string{"name": name}
		if label != "" {
			args["label"] = label
		}
		if key != "" {
			args["crew"] = key
		}
		data, _ := json.Marshal(args)
		must(t, a.agents.Prompt(ctx, lead, "START "+string(data)))
	}
	verify := func(name, text, model, effort string) {
		t.Helper()
		wait("worker provider call", func() bool {
			for _, e := range entries() {
				if e.Agent == name && e.Text == text {
					if e.Provider != provider || e.Model != model || e.Effort != effort || e.HasTool || e.Route != "" {
						t.Fatalf("incorrect worker dispatch: %+v", e)
					}
					return true
				}
			}
			return false
		})
		t.Logf("%s received %q on %s/%s:%s", name, text, provider, model, effort)
		wait("finished worker and idle lead", func() bool {
			i, _ := a.agents.Get(agent.ID(y.Name, name))
			l, _ := a.agents.Get(lead)
			return !i.FinishedAt.IsZero() && l.Status == agent.Idle
		})
	}
	start("matched", "backend", "")
	verify("matched", "Deliver matched", build, "high")
	first := entries()[0]
	if !first.HasTool || !first.HasLabel || !strings.Contains(first.Route, "coding 89% (user CSV)") || !strings.Contains(first.Route, "Database deliverables (fixture guidance)") {
		t.Fatalf("lead context/schema: %+v", first)
	}
	probeDown.Store(true)
	start("catalog-down", "backend", "")
	verify("catalog-down", "Deliver catalog-down", build, "high")
	var advisory string
	for _, e := range entries() {
		if e.Agent == "lead" && strings.Contains(e.Text, `"name":"catalog-down"`) {
			advisory = e.Route
			break
		}
	}
	for _, want := range []string{"availability is unknown", low, high, fallback, "coding 89% (user CSV)"} {
		if !strings.Contains(advisory, want) {
			t.Fatalf("failed probe lost %q in lead context: %s", want, advisory)
		}
	}
	start("slot", "backend", high)
	verify("slot", "Deliver slot", "fallback", "low")
	probeDown.Store(false)
	start("unmatched", "default", high)
	verify("unmatched", "Deliver unmatched", "fallback", "low")
	start("legacy", "", high)
	verify("legacy", "Deliver legacy", "fallback", "low")
	start("stale", "backend", high)
	wait("old pool in the lead turn", func() bool {
		for _, e := range entries() {
			if strings.Contains(e.Text, `"name":"stale"`) && strings.Contains(e.Route, high) {
				return true
			}
		}
		return false
	})
	_, err = s.RemoveCrewOption(ctx, high)
	must(t, err)
	must(t, os.WriteFile(gate, nil, 0600))
	verify("stale", "Deliver stale", build, "low")
	setPool("default", "")
	start("own", "frontend", low)
	verify("own", "Deliver own", "interactive", "high")
	// Cold Manager, catalog and settings. The original worker resumes its own
	// high effort even though that effort was removed from Crew in the meantime.
	a.agents.StopAll()
	a = &app{home: h, yesmen: store, settings: loadSettings(h.Settings()), agents: agent.New(cfg)}
	a.catalog = newCatalog(func(ctx context.Context) ([]agent.Model, error) { return a.agents.Catalog(ctx, y.Path) })
	srv.Agents, srv.Routing = a.agents, a.workerRouting
	must(t, a.agents.PromptFromLead(ctx, agent.ID(y.Name, "matched"), "Reopen matched"))
	verify("matched", "Reopen matched", build, "high")
	start("reloaded", "backend", high)
	verify("reloaded", "Deliver reloaded", build, "low")
}
