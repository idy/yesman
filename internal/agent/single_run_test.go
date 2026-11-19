package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func TestRunOnceWaitsForRecordedInputsWorkersAndContinuations(t *testing.T) {
	since := time.Now().UTC()
	y := yesmen.Yesman{Name: "team", HiredAt: since}
	lead := &agent{info: Info{ID: "team/lead", Yesman: y.Name, Role: RoleLead, Status: Idle}, workspaceSince: since, proc: &proc{}, settled: 2, inbox: 2, settledInbox: 2}
	w := &agent{info: Info{ID: "team/w", Yesman: y.Name, Role: RoleWorker, Status: Stopped}, workspaceSince: since}
	r := &singleRun{lead: lead.info.ID, recorded: true, lastInput: 1, pending: map[Origin]int{}}
	m := &Manager{run: r, agents: map[string]*agent{lead.info.ID: lead, w.info.ID: w}, continuations: map[string]*continuation{}}
	check := func(want string) {
		t.Helper()
		var out RunResult
		m.checkRunLocked(y, true, &out)
		if out.Status != want {
			t.Fatalf("got %q, want %q (%+v)", out.Status, want, out)
		}
	}
	check("completed")
	r.recorded = false
	check("")
	r.recorded = true
	origin := Origin{Source: OriginYesman, Kind: "report", Worker: "w"}
	r.pending[origin] = 1
	check("")
	m.observeRun(lead, lead.proc, "message_end", json.RawMessage(`{"message":{"role":"user","yesman":{"source":"yesman","kind":"report","worker":"w"}}}`))
	check("")
	lead.settled++
	check("completed")
	w.reports = 1
	check("")
	w.reports = 0
	lead.queue.FollowUp = 1
	check("")
	lead.queue.FollowUp = 0
	w.starting = make(chan struct{})
	check("")
	w.starting = nil
	c := &continuation{running: true}
	m.continuations[y.Name] = c
	check("")
	c.running = false
	c.timer = time.NewTimer(time.Hour)
	check("")
	c.timer.Stop()
	c.timer = nil
	w.info.Status = Waiting
	w.info.Dialog = json.RawMessage(`{"id":"q","method":"confirm"}`)
	check("")
	w.handedOver = "q"
	check("needs_input")
	w.handedOver = ""
	lead.info.Status = Waiting
	lead.info.Dialog = json.RawMessage(`{"id":"lead-q","method":"input"}`)
	check("needs_input")
	lead.info.Status, w.info.Status = Idle, Stopped
	lead.info.RunError = &RunError{Message: "provider unavailable"}
	check("failure")
}

func TestRunOnceRejectsEffectiveModelBeforeSubmitting(t *testing.T) {
	store := yesmen.New(filepath.Join(t.TempDir(), "yesmen"), t.TempDir())
	y, err := store.Hire("team")
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, pin := range []Choice{{"beta", "shared", "high"}, {"alpha", "other", "high"}, {"alpha", "shared", "max"}} {
		// This stub silently ignores all selector flags and starts as alpha/shared:high.
		m := New(Config{Pi: exe, Env: []string{"YESMAN_FAKE_PI=1"}, Yesmen: store, ExactModel: pin})
		out := m.RunOnce(t.Context(), y, "a single message")
		if out.Status != "failure" || !strings.Contains(out.Error, "Pi selected alpha/shared:high") || out.Reply != "" {
			t.Fatalf("%s: %+v", pin, out)
		}
		if m.running(ID(y.Name, yesmen.LeadName)) != nil {
			t.Fatal("kept a mismatched process")
		}
	}
}

func TestRunOnceStopsPendingSubmissionAndDrains(t *testing.T) {
	for _, status := range []string{"cancelled", "timed_out"} {
		t.Run(status, func(t *testing.T) {
			store := yesmen.New(filepath.Join(t.TempDir(), "yesmen"), t.TempDir())
			y, err := store.Hire("team")
			if err != nil {
				t.Fatal(err)
			}
			exe, _ := os.Executable()
			received := make(chan struct{}, 1)
			m := New(Config{Pi: exe, Env: []string{"YESMAN_FAKE_PI=1", "FAKE_PI_HOLD_PROMPTS=1"}, Yesmen: store, OnEvent: func(e Event) {
				if strings.Contains(string(e.Data), `"type":"test_prompt_received"`) {
					select {
					case received <- struct{}{}:
					default:
					}
				}
			}})
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			out := make(chan RunResult, 1)
			go func() { out <- m.RunOnce(ctx, y, "keep waiting") }()
			select {
			case <-received:
			case <-ctx.Done():
				t.Fatal("fake Pi never got the request")
			}
			if status == "cancelled" {
				cancel()
			}
			res := <-out
			if res.Status != status {
				t.Fatalf("%+v", res)
			}
			id := ID(y.Name, yesmen.LeadName)
			if m.running(id) != nil {
				t.Fatal("trial process is still running")
			}
			m.mu.Lock()
			writer := m.agents[id].deliveryWriter
			closed := m.closed
			m.mu.Unlock()
			if !closed || !writer.finished() {
				t.Fatal("manager or receipt writer still active")
			}
			if _, err := os.Stat(filepath.Join(y.SessionDir(yesmen.LeadName), "delivery.json")); err != nil {
				t.Fatal(err)
			}
			if err := m.Prompt(t.Context(), id, "late message"); !errors.Is(err, errStopped) {
				t.Fatalf("late admission: %v", err)
			}
		})
	}
}
