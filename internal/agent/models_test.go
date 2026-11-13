package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func TestModelAndEffortSelection(t *testing.T) {
	m, store := newTestManager(t, 0)
	ctx := context.Background()
	id := ID("alice", yesmen.LeadName)
	opts, err := m.Models(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.Models) != 2 || opts.Models[0].ContextWindow != 200000 || opts.Agent.Model != "shared" || opts.Agent.Provider != "alpha" || opts.Agent.Since.IsZero() {
		t.Fatalf("initial choices: %+v", opts)
	}
	info, err := m.SetEffort(ctx, id, "max")
	if err != nil || info.Effort != "max" {
		t.Fatalf("max effort: %+v, %v", info, err)
	}
	if _, err := m.SetEffort(ctx, id, "xhigh"); err == nil {
		t.Fatal("unsupported effort was silently clamped")
	}
	// The two providers have the same model ID. The provider is part of the
	// selection, and moving to a model without reasoning changes the effort.
	info, err = m.SetModel(ctx, id, "beta", "shared")
	if err != nil || info.Provider != "beta" || info.Effort != "off" || strings.Join(info.EffortLevels, ",") != "off" {
		t.Fatalf("plain model: %+v, %v", info, err)
	}
	if _, err := m.SetEffort(ctx, id, "high"); err == nil {
		t.Fatal("reasoning was enabled on a model without reasoning")
	}
	if _, err := m.SetModel(ctx, id, "missing", "shared"); err == nil {
		t.Fatal("unknown provider was accepted")
	}
	if info, _ = m.Get(id); info.Provider != "beta" || info.Effort != "off" {
		t.Fatalf("failed selection changed the agent: %+v", info)
	}
	// Another agent has its own pi session and selection.
	if err := store.AddWorker("alice", yesmen.Worker{Name: "w1", Task: "task", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	worker, err := m.Models(ctx, ID("alice", "w1"))
	if err != nil || worker.Agent.Provider != "alpha" || worker.Agent.Effort != "high" {
		t.Fatalf("worker inherited the lead's settings: %+v, %v", worker, err)
	}
}

func TestModelChangeWhileWorkingIsRefused(t *testing.T) {
	m, _ := newTestManager(t, 0, "FAKE_PI_BUSY=1")
	ctx := context.Background()
	id := ID("alice", yesmen.LeadName)
	if _, err := m.SetModel(ctx, id, "beta", "shared"); err == nil {
		t.Fatal("model changed during a run")
	}
	if _, err := m.SetEffort(ctx, id, "low"); err == nil {
		t.Fatal("effort changed during a run")
	}
	info, _ := m.Get(id)
	if info.Provider != "alpha" || info.Effort != "high" {
		t.Fatalf("busy selections changed the agent: %+v", info)
	}
}
