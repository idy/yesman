package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/home"
	"github.com/idy/yesman/internal/usage"
	"github.com/idy/yesman/internal/yesmen"
)

func usageApp(t *testing.T) *app {
	t.Helper()
	h := home.Home{Root: t.TempDir()}
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	a := &app{home: h, yesmen: yesmen.New(filepath.Join(h.Root, "yesmen"), filepath.Join(h.Root, "archive"))}
	a.catalog = newCatalog(func(context.Context) ([]agent.Model, error) {
		return []agent.Model{{Provider: "p", ID: "fast", Name: "Fast · Somewhere", Levels: []string{"high"}}}, nil
	})
	return a
}

// sessionLine is an assistant message as pi writes it, with made-up values.
func sessionLine(id string, at time.Time, model, effort string, ms, output int64) string {
	m := map[string]any{"role": "assistant", "content": []any{}, "api": "openai-completions", "provider": "p", "model": model,
		"thinkingLevel": effort, "durationMs": ms, "stopReason": "stop", "timestamp": at.UnixMilli(),
		"usage": map[string]any{"input": 1, "output": output, "cacheRead": 10, "cacheWrite": 0, "totalTokens": 11 + output}}
	b, _ := json.Marshal(map[string]any{"type": "message", "id": id, "parentId": nil, "timestamp": at.Add(time.Duration(ms) * time.Millisecond).UTC().Format(time.RFC3339Nano), "message": m})
	return string(b) + "\n"
}

func writeSession(t *testing.T, y yesmen.Yesman, agentName, text string) string {
	t.Helper()
	path := filepath.Join(y.SessionDir(agentName), "2026-11-14T10-00-00-000Z_s.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	return path
}

func hireWith(t *testing.T, a *app, name string, workers ...string) yesmen.Yesman {
	t.Helper()
	y, err := a.yesmen.Hire(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range workers {
		// Started before the made-up history, which goes back three days.
		if err := a.yesmen.AddWorker(name, yesmen.Worker{Name: w, Task: "x", StartedAt: time.Now().Add(-72 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	return y
}

func findRow(v ModelUsageView, model, effort string) *usage.Row {
	for i := range v.Rows {
		if v.Rows[i].Model == model && v.Rows[i].Effort == effort {
			return &v.Rows[i]
		}
	}
	return nil
}

func TestModelUsageSummarizesCurrentYesmen(t *testing.T) {
	a := usageApp(t)
	alpha := hireWith(t, a, "alpha", "w1")
	beta := hireWith(t, a, "beta", "w1")
	at := time.Now().Add(-time.Hour)
	writeSession(t, alpha, "lead", sessionLine("l1", at, "fast", "high", 1000, 50))
	writeSession(t, alpha, "w1", sessionLine("a1", at, "fast", "high", 2000, 100)+sessionLine("a2", at.Add(time.Minute), "slow", "xhigh", 4000, 40))
	writeSession(t, beta, "w1", sessionLine("b1", at.Add(-48*time.Hour), "fast", "high", 1000, 300))
	if _, err := a.yesmen.ChangeBoard("beta", yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: ptr("Done"), Status: ptr("done"), Owner: ptr("w1")}}}); err != nil {
		t.Fatal(err)
	}

	v, err := a.modelUsage(context.Background(), ModelUsageQuery{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if v.Query.Agents != "workers" || v.Query.Period != "all" || len(v.Yesmen) != 2 || v.Yesmen[0].Name != "alpha" {
		t.Fatalf("view = %+v", v)
	}
	fast := findRow(v, "fast", "high")
	if fast == nil || fast.Workers != 2 || fast.Leads != 0 || fast.Responses != 2 || fast.Output != 400 || fast.Tasks != 1 {
		t.Fatalf("fast row = %+v", fast)
	}
	if fast.Speed == nil || *fast.Speed != 400.0/3 {
		t.Errorf("fast speed = %v", fast.Speed)
	}
	if v.Total.Workers != 2 || v.Overlap != 1 || v.Total.Tasks != 1 || v.Coverage.Workspaces != 2 || v.Coverage.Files != 3 {
		t.Errorf("total = %+v, overlap %d, coverage %+v", v.Total, v.Overlap, v.Coverage)
	}
	if len(v.Names) != 0 {
		t.Errorf("names before pi's catalog was read: %v", v.Names)
	}

	// One yesman, its lead, the last day; pi's names once its catalog is read.
	if _, _, err := a.catalog.get(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	v, err = a.modelUsage(context.Background(), ModelUsageQuery{Yesman: "alpha", Agents: "leads", Period: "24h"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r := findRow(v, "fast", "high"); r == nil || r.Leads != 1 || r.Workers != 0 || r.Output != 50 || len(v.Rows) != 1 {
		t.Fatalf("alpha leads = %+v", v.Rows)
	}
	if v.Names["p/fast"] != "Fast · Somewhere" || v.Since.IsZero() {
		t.Errorf("names = %v, since %v", v.Names, v.Since)
	}
	v, err = a.modelUsage(context.Background(), ModelUsageQuery{Agents: "all", Period: "24h"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// beta's response is older than a day; its task was done just now, so
	// it counts, with its owner among the model's workers.
	if r := findRow(v, "fast", "high"); r == nil || r.Output != 150 || r.Tasks != 1 || r.Workers != 2 || r.Leads != 1 {
		t.Fatalf("all agents, 24h = %+v", r)
	}

	for _, q := range []ModelUsageQuery{{Yesman: "nobody"}, {Agents: "robots"}, {Period: "1y"}} {
		if _, err := a.modelUsage(context.Background(), q, time.Now()); err == nil {
			t.Errorf("%+v succeeded", q)
		}
	}
}

// Reading while pi appends counts each response once, from any number of
// concurrent readers.
func TestModelUsageReadsConcurrentlyWithWriters(t *testing.T) {
	a := usageApp(t)
	y := hireWith(t, a, "alpha", "w1")
	at := time.Now().Add(-time.Hour)
	writeSession(t, y, "w1", sessionLine("a0", at, "fast", "high", 1000, 1))
	const lines = 200
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 1; i <= lines; i++ {
			line := sessionLine(fmt.Sprintf("a%d", i), at.Add(time.Duration(i)*time.Second), "fast", "high", 1000, 1)
			// Half a line at a time, as a write in progress.
			writeSession(t, y, "w1", line[:len(line)/2])
			writeSession(t, y, "w1", line[len(line)/2:])
		}
	})
	for range 4 {
		wg.Go(func() {
			for range 20 {
				if _, err := a.modelUsage(context.Background(), ModelUsageQuery{}, time.Now()); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	v, err := a.modelUsage(context.Background(), ModelUsageQuery{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r := findRow(v, "fast", "high"); r == nil || r.Responses != lines+1 || r.Output != lines+1 || v.Coverage.Unreadable != 0 {
		t.Fatalf("after concurrent reads: %+v, coverage %+v", r, v.Coverage)
	}
}

// A dismissed yesman leaves the summary; one hired again under its name
// starts afresh. A yesman whose records cannot be read is named while the
// rest are counted.
func TestModelUsageFollowsDismissalAndFailures(t *testing.T) {
	a := usageApp(t)
	alpha := hireWith(t, a, "alpha", "w1")
	beta := hireWith(t, a, "beta", "w1")
	at := time.Now().Add(-time.Hour)
	writeSession(t, alpha, "w1", sessionLine("a1", at, "fast", "high", 1000, 7))
	writeSession(t, beta, "w1", sessionLine("b1", at, "fast", "high", 1000, 5))
	if _, err := a.modelUsage(context.Background(), ModelUsageQuery{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.yesmen.Archive("alpha", nil); err != nil {
		t.Fatal(err)
	}
	v, err := a.modelUsage(context.Background(), ModelUsageQuery{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r := findRow(v, "fast", "high"); r == nil || r.Output != 5 || len(v.Yesmen) != 1 {
		t.Fatalf("after dismissal: %+v", v.Rows)
	}
	a.usage.mu.Lock()
	_, kept := a.usage.spaces["alpha"]
	a.usage.mu.Unlock()
	if kept {
		t.Error("a dismissed yesman's records were kept")
	}

	time.Sleep(time.Millisecond) // a new hire time
	again := hireWith(t, a, "alpha", "w1")
	writeSession(t, again, "w1", sessionLine("n1", at, "fast", "high", 1000, 100))
	if err := os.WriteFile(filepath.Join(beta.MetaDir(), "workers.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err = a.modelUsage(context.Background(), ModelUsageQuery{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r := findRow(v, "fast", "high"); r == nil || r.Output != 100 {
		t.Fatalf("after hiring again: %+v", v.Rows)
	}
	if len(v.Coverage.Errors) != 1 || !strings.HasPrefix(v.Coverage.Errors[0], "beta: ") || v.Coverage.Workspaces != 1 {
		t.Errorf("errors = %v, workspaces %d", v.Coverage.Errors, v.Coverage.Workspaces)
	}
}

// A name started, removed and started again keeps each start's usage
// apart, through the Store's own record of starts.
func TestModelUsageKeepsRemovedWorkersApart(t *testing.T) {
	a := usageApp(t)
	y := hireWith(t, a, "alpha")
	base := time.Now().Add(-3 * time.Hour).UTC()
	for i, model := range []string{"one", "two", "three"} {
		start := base.Add(time.Duration(i) * time.Hour)
		if err := a.yesmen.AddWorker("alpha", yesmen.Worker{Name: "w", Task: "x", StartedAt: start}); err != nil {
			t.Fatal(err)
		}
		writeSession(t, y, "w", sessionLine(fmt.Sprintf("r%d", i), start.Add(time.Minute), model, "high", 1000, 10))
		if err := a.yesmen.RemoveWorker("alpha", "w"); err != nil {
			t.Fatal(err)
		}
	}
	v, err := a.modelUsage(context.Background(), ModelUsageQuery{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Rows) != 3 || v.Total.Workers != 3 || v.Overlap != 0 || len(v.Coverage.Errors) != 0 {
		t.Fatalf("rows = %+v, total %+v, overlap %d, errors %v", v.Rows, v.Total, v.Overlap, v.Coverage.Errors)
	}
}

func ptr(s string) *string { return &s }
