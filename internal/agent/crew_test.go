package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/yesmen"
)

func TestLeadStartsAsItsYesmansModel(t *testing.T) {
	m, store := newTestManager(t, 0)
	if _, err := store.HireAs("bob", "beta/shared"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.HireAs("carol", "alpha/shared:low"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for id, want := range map[string][2]string{
		ID("bob", yesmen.LeadName):   {"beta", "off"},
		ID("carol", yesmen.LeadName): {"alpha", "low"},
		ID("alice", yesmen.LeadName): {"alpha", "high"}, // hired with no model: pi's default
	} {
		if err := m.Prompt(ctx, id, "hi"); err != nil {
			t.Fatal(err)
		}
		if i, _ := m.Get(id); i.Provider != want[0] || i.Effort != want[1] {
			t.Errorf("%s runs as %s at %s, want %v", id, i.Provider, i.Effort, want)
		}
	}
}

func TestAMissingModelStillStarts(t *testing.T) {
	m, store := newTestManager(t, 0)
	if _, err := store.HireAs("bob", "gone/away:high"); err != nil {
		t.Fatal(err)
	}
	lead := ID("bob", yesmen.LeadName)
	if err := m.Prompt(context.Background(), lead, "hi"); err != nil {
		t.Fatal(err)
	}
	if i, _ := m.Get(lead); i.Provider != "alpha" {
		t.Errorf("lead runs as %s", i.Provider)
	}
}

func TestResumedSessionsKeepTheirModel(t *testing.T) {
	m, store := newTestManager(t, 0)
	y, err := store.HireAs("bob", "beta/shared")
	if err != nil {
		t.Fatal(err)
	}
	dir := y.SessionDir(yesmen.LeadName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lead := ID("bob", yesmen.LeadName)
	if err := m.Prompt(context.Background(), lead, "hi"); err != nil {
		t.Fatal(err)
	}
	if i, _ := m.Get(lead); i.Provider != "alpha" {
		t.Errorf("a resumed lead switched to %s", i.Provider)
	}
}

func TestStartWorkerAs(t *testing.T) {
	// Asking keeps the worker running, so its model can be read.
	m, store := newTestManager(t, 0, "FAKE_PI_ASK_WORKERS=1")
	ctx := context.Background()
	if _, err := m.StartWorkerAs(ctx, "alice", "w1", "Build", "a task", Choice{Provider: "alpha", Model: "shared", Effort: "max"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, ID("alice", "w1"), Waiting)
	if i, _ := m.Get(ID("alice", "w1")); i.Provider != "alpha" || i.Effort != "max" {
		t.Errorf("worker runs as %s at %s", i.Provider, i.Effort)
	}

	_, err := m.StartWorkerAs(ctx, "alice", "w2", "Build", "a task", Choice{Provider: "alpha", Model: "shared", Effort: "xhigh"})
	if err == nil || !strings.Contains(err.Error(), "alpha/shared:xhigh") {
		t.Errorf("an effort the model lacks: %v", err)
	}
	if _, err := m.StartWorkerAs(ctx, "alice", "w3", "Build", "a task", Choice{Provider: "gone", Model: "away"}); err == nil {
		t.Error("a worker started as a model pi does not have")
	}
	ws, _ := store.Workers("alice")
	if len(ws) != 1 || ws[0].Name != "w1" {
		t.Errorf("workers after failed starts: %+v", ws)
	}
}

func TestParseChoice(t *testing.T) {
	c, ok := ParseChoice("magpie/codex-remote/codex/gpt-6.1-sol:high")
	if !ok || c != (Choice{Provider: "magpie", Model: "codex-remote/codex/gpt-6.1-sol", Effort: "high"}) || c.String() != "magpie/codex-remote/codex/gpt-6.1-sol:high" {
		t.Errorf("ParseChoice = %+v, %v", c, ok)
	}
	if _, ok := ParseChoice(""); ok {
		t.Error("an empty key is a choice")
	}
}

func TestCatalog(t *testing.T) {
	m, _ := newTestManager(t, 0)
	models, err := m.Catalog(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].Provider != "alpha" || models[1].Provider != "beta" {
		t.Errorf("catalog = %+v", models)
	}
	if len(m.List()) != 1 {
		t.Errorf("reading the catalog started agents: %+v", m.List())
	}
}
