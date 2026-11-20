package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
	// The last worker run ended on a model error. It can be replaced or
	// retried, and its diagnostic persists while the Lead decides what to do.
	w.givenWork = true
	w.info.RunError = &RunError{Message: "worker request refused"}
	check("completed")
	w.info.Status = Failed // a worker start or process exit, already reported
	check("completed")
	w.proc, w.info.Status = &proc{}, Working // the Lead retried it
	check("")
	w.info.Status = Idle
	check("") // still closing this worker or preparing the retry
	w.handoff.state = "not-sent"
	w.inbox++ // a later prompt was admitted before its agent_start
	check("")
	w.settledInbox = w.inbox
	check("failure") // exhausted report delivery, with the worker now idle
	w.handoff = reportHandoff{}
	w.proc, w.info.Status = nil, Stopped
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
	// A replacement succeeding cannot close the run while the Lead still
	// has to process its report, even if the Lead's last turn also failed.
	r.pending[origin] = 1
	check("")
	delete(r.pending, origin)
	lead.info.RunError = nil
	check("completed")
	lead.info.Status = Failed
	check("failure")
}

// A crashed worker's failure notice and a settled worker's final report use
// different handoff bookkeeping. A refused notification is a delivery error
// in both cases, even though the worker's own error is recoverable.
func TestRunOnceRequiresDeliveryOfWorkerNotices(t *testing.T) {
	for _, crash := range []string{"", "1"} {
		t.Run("crash="+crash, func(t *testing.T) {
			m, store := newTestManager(t, 0, "FAKE_PI_CRASH_WORKERS="+crash, "FAKE_PI_FAIL_REPORTS=1")
			y, err := store.Get("alice")
			if err != nil {
				t.Fatal(err)
			}
			lead, p, err := m.ensure("alice/lead")
			if err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			m.run = &singleRun{lead: lead.info.ID, recorded: true, pending: map[Origin]int{}}
			m.mu.Unlock()
			m.record(lead, p, "agent_settled", runSettled)
			old := reportBackoff
			reportBackoff = time.Millisecond
			defer func() { reportBackoff = old }()
			if _, err := m.StartWorker(t.Context(), y.Name, "first", "Write", "a task"); err != nil {
				t.Fatal(err)
			}
			var out RunResult
			eventually(t, "delivery failure", func() bool {
				m.mu.Lock()
				defer m.mu.Unlock()
				m.checkRunLocked(y, true, &out)
				return out.Status != ""
			})
			if out.Status != "failure" || !strings.Contains(out.Error, "alice/first") || !strings.Contains(out.Error, "rejected") {
				t.Fatalf("lost worker message: %+v", out)
			}
		})
	}
}

func TestRunOnceReportsLeadErrorWithUnidentifiedOriginal(t *testing.T) {
	m, store := newTestManager(t, 0)
	y, err := store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	// This external Pi reports the terminal assistant error and settle,
	// with no identity for the original submission.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	out := m.RunOnce(ctx, y, "test-run:error")
	if out.Status != "failure" || !strings.Contains(out.Error, "alice/lead: model error:") {
		t.Fatalf("Lead model error: %+v", out)
	}
}

func TestRunOnceReportsAReceiptDrainFailure(t *testing.T) {
	m, store := newTestManager(t, 0, "FAKE_PI_HOLD_PROMPTS=1")
	y, err := store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	m.cfg.deliveryWrite = func(path string, data []byte) error {
		m.mu.Lock()
		closed := m.closed
		m.mu.Unlock()
		if closed {
			return errors.New("synthetic final receipt write failed")
		}
		return writeDeliveryFile(path, data)
	}
	gotPrompt := make(chan struct{}, 1)
	m.cfg.OnEvent = func(ev Event) {
		if strings.Contains(string(ev.Data), `"type":"test_prompt_received"`) {
			gotPrompt <- struct{}{}
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan RunResult, 1)
	go func() { done <- m.RunOnce(ctx, y, "a task") }()
	select {
	case <-gotPrompt:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	m.mu.Lock()
	a := m.agents["alice/lead"]
	p := a.proc
	m.mu.Unlock()
	m.record(a, p, "extension_ui_request", json.RawMessage(`{"type":"extension_ui_request","id":"q","method":"confirm"}`))
	out := <-done
	if out.Status != "failure" || !strings.Contains(out.Error, "synthetic final receipt write failed") {
		t.Fatalf("failed drain: %+v", out)
	}
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
			var want []RunInterruptedAgent
			if status == "timed_out" {
				want = []RunInterruptedAgent{{ID: id, Role: RoleLead, Since: y.HiredAt.UTC(), Phase: "input_pending"}}
			}
			if !slices.Equal(res.InterruptedAgents, want) {
				t.Fatalf("snapshot = %+v, want %+v", res.InterruptedAgents, want)
			}
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
