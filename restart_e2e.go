//go:build e2e

package main

// Restart and continue, end to end, in a build tagged e2e running beside
// any other yesman (YESMAN_E2E_NAME) with a home of its own. With
// YESMAN_E2E_RESTART set, the app hires the yesman "alice" if it has none,
// gives its lead that message, and once the lead works, optionally starts a
// worker and puts it to sleep as the user does, then restarts through the
// window's own call (Restart, twice, as a double click) or, with
// YESMAN_E2E_RESTART_MODE=quit, quits. A start that finds the step file
// done waits for the restart's result, or YESMAN_E2E_SETTLE, writes
// e2e-result.json in the home and quits. No notification is shown.
import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"

	"github.com/idy/yesman/internal/agent"
)

func init() {
	if os.Getenv("YESMAN_E2E_RESTART") == "" {
		return
	}
	e2eReady = func(a *app) {
		a.notifier = func(id, title, body string) { log.Printf("e2e notification for %s: %s", id, title) }
		go e2eRestart(a)
	}
}

func e2eRestart(a *app) {
	step := filepath.Join(a.home.Root, "e2e-step")
	if _, err := os.Stat(step); err == nil {
		e2eFinish(a)
		return
	}
	if err := os.WriteFile(step, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600); err != nil {
		log.Fatal(err)
	}
	if _, err := a.yesmen.Get("alice"); err != nil {
		if _, err := a.yesmen.Hire("alice"); err != nil {
			log.Fatal(err)
		}
	}
	ctx := context.Background()
	lead := agent.ID("alice", "lead")
	if err := a.agents.Prompt(ctx, lead, os.Getenv("YESMAN_E2E_RESTART")); err != nil {
		log.Fatal(err)
	}
	e2eWait(a, lead, agent.Working)
	if task := os.Getenv("YESMAN_E2E_SLEEPER"); task != "" {
		if _, err := a.agents.StartWorker(ctx, "alice", "sleeper", "e2e sleeper", task); err != nil {
			log.Fatal(err)
		}
		e2eWait(a, agent.ID("alice", "sleeper"), agent.Working)
		a.agents.Stop(agent.ID("alice", "sleeper")) // the user puts it to sleep
	}
	s := &service{app: a}
	if os.Getenv("YESMAN_E2E_RESTART_MODE") == "quit" {
		mygo.App.Quit()
		return
	}
	for range 2 { // a double click restarts once
		if err := s.Restart(); err != nil {
			log.Fatal(err)
		}
	}
}

func e2eWait(a *app, id string, want agent.Status) {
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if i, err := a.agents.Get(id); err == nil && i.Status == want {
			return
		}
	}
	log.Fatalf("e2e: %s did not become %s", id, want)
}

// e2eFinish records what this start saw and quits.
func e2eFinish(a *app) {
	settle, _ := time.ParseDuration(os.Getenv("YESMAN_E2E_SETTLE"))
	deadline := time.Now().Add(30 * time.Second)
	if settle > 0 {
		deadline = time.Now().Add(settle)
	}
	var v RestartView
	for time.Now().Before(deadline) {
		if v = a.restarts.view(); v.Last != nil && settle == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(time.Second) // the resumed run settles
	out, _ := json.MarshalIndent(map[string]any{"pid": os.Getpid(), "restart": v, "agents": a.agents.List()}, "", "  ")
	if err := os.WriteFile(filepath.Join(a.home.Root, "e2e-result.json"), out, 0o600); err != nil {
		log.Print(err)
	}
	mygo.App.Quit()
}
