package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// TestRealPiFollowUpWhileTheLeadAnswers runs the installed pi, with yesman's
// extension as a lead, isolated configuration and the deterministic local
// provider of scripts/fixtures, behind the actual agent manager. It never
// calls a real model. Set YESMAN_TEST_REAL_PI to the pi executable to run it:
//
//	YESMAN_TEST_REAL_PI=$(command -v pi) go test ./internal/schedule -run RealPi -v
//
// While the lead answers a question of the user's, a due scheduled run goes
// into pi's follow-up queue, never steering or aborting that answer; the lead
// reads it after, with no private ID in what the model sees, the transcript
// or the session log.
func TestRealPiFollowUpWhileTheLeadAnswers(t *testing.T) {
	real := os.Getenv("YESMAN_TEST_REAL_PI")
	if real == "" {
		t.Skip("set YESMAN_TEST_REAL_PI to the pi executable")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the wrapper is a POSIX shell script")
	}
	root := t.TempDir()
	repo, _ := filepath.Abs("../..")
	provider := filepath.Join(repo, "scripts", "fixtures", "delivery-provider.ts")
	extension := filepath.Join(repo, "pi", "yesman.ts")
	config := filepath.Join(root, "pi-config")
	if err := os.MkdirAll(config, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"packages":[],"extensions":[],"skills":[],"promptTemplates":[],"themes":[],"compaction":{"enabled":false},"retry":{"enabled":false},"defaultProjectTrust":"no"}`
	if err := os.WriteFile(filepath.Join(config, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	// pi with the local provider: the manager adds its own arguments first.
	wrapper := filepath.Join(root, "pi")
	script := fmt.Sprintf("#!/bin/sh\nexec %q \"$@\" --provider delivery-test --model m --no-builtin-tools -e %q\n", real, provider)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// The app's API, as far as the extension asks it before a run.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/context" {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "Fixture context")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"env":{},"hide":[]}`)
	}))
	defer api.Close()
	audit, gate := filepath.Join(root, "audit"), filepath.Join(root, "gate")

	store := yesmen.New(filepath.Join(root, "yesmen"), filepath.Join(root, "archive"))
	y, err := store.Hire(ws)
	if err != nil {
		t.Fatal(err)
	}
	// Wired as the app wires them: receipts' events reach the scheduler.
	var svc *Service
	m := agent.New(agent.Config{
		Pi: wrapper, Extension: extension, Yesmen: store,
		Env:     []string{"YESMAN_API=" + api.URL, "PI_CODING_AGENT_DIR=" + config, "DELIVERY_AUDIT=" + audit, "DELIVERY_GATE=" + gate},
		OnEvent: func(e agent.Event) { svc.OnAgentEvent(e) },
	})
	defer m.StopAll()
	clock := &fakeClock{t: utc("2026-11-16T08:59:00Z")}
	svc = New(Config{Yesmen: store, Agents: m, Now: clock.Now, LockPath: filepath.Join(root, ".runtime", "schedules.lock")})
	svc.Start()
	defer svc.Close()
	if _, err := svc.Save(ws, PlanInput{Title: "Nightly check", Instructions: "Check the overnight builds.", Schedule: daily("09:00"), Timezone: "UTC", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	// The user asks something; the provider holds the answer until the gate.
	lead := agent.ID(ws, yesmen.LeadName)
	ctx := context.Background()
	human := fmt.Sprintf("%012x%036x", time.Now().UnixMilli(), 1)
	if d, err := m.Submit(ctx, lead, human, "What changed today?", nil, y.HiredAt); err != nil || d.State == "failed" {
		t.Fatalf("the user's message: %+v %v", d, err)
	}
	waitFor(t, "the lead answers", func() bool { i, _ := m.Get(lead); return i.Status == agent.Working })

	clock.Set(utc("2026-11-16T09:00:05Z"))
	svc.Tick(ctx)
	svc.Wait()
	id := "s1-20261116t0900"
	run := runOf(t, svc, id)
	if run.State != StateQueued || run.Attempts[0].State != "queued" {
		t.Fatalf("a run due while the lead answers: %+v", run)
	}
	if i, _ := m.Get(lead); i.Status != agent.Working {
		t.Fatalf("the lead's answer was interrupted: %s", i.Status)
	}

	if err := os.WriteFile(gate, []byte("release"), 0o644); err != nil {
		t.Fatal(err)
	}
	var users []json.RawMessage
	var last string
	defer func() {
		if t.Failed() {
			t.Logf("last seen: %s", last)
		}
	}()
	waitFor(t, "the lead reads the run", func() bool {
		conv, err := m.Messages(ctx, lead)
		if err != nil {
			return false
		}
		var messages []struct {
			Role string `json:"role"`
		}
		_ = json.Unmarshal(conv.Messages, &messages)
		var raw []json.RawMessage
		_ = json.Unmarshal(conv.Messages, &raw)
		users = nil
		for i, msg := range messages {
			if msg.Role == "user" {
				users = append(users, raw[i])
			}
		}
		info, _ := m.Get(lead)
		last = fmt.Sprintf("%d user messages, lead %s %s", len(users), info.Status, info.Detail)
		return len(users) >= 2 && info.Status == agent.Idle
	})
	// The user's question first, then the run; yesman may remind the lead of
	// the run's open board task after that.
	text := string(users[1])
	if !strings.Contains(text, "Scheduled task s1") || !strings.Contains(text, "Check the overnight builds.") || !strings.Contains(string(users[0]), "What changed today?") {
		t.Fatalf("transcript order or text: %s", users)
	}
	waitFor(t, "the run follows its receipt", func() bool { return runOf(t, svc, id).Attempts[0].State != "queued" })
	run = runOf(t, svc, id)
	auditData, _ := os.ReadFile(audit)
	var logs []byte
	sessions, _ := filepath.Glob(filepath.Join(y.SessionDir(yesmen.LeadName), "*.jsonl"))
	for _, s := range sessions {
		data, _ := os.ReadFile(s)
		logs = append(logs, data...)
	}
	for _, private := range []string{id, run.Attempts[0].ID, human, `"origin"`, "schedule\""} {
		for where, data := range map[string]string{"transcript": string(users[1]), "model context": string(auditData), "session log": string(logs)} {
			if strings.Contains(data, private) {
				t.Errorf("%s shows %q", where, private)
			}
		}
	}
	if !strings.Contains(string(auditData), "Check the overnight builds.") {
		t.Fatalf("the model never saw the run: %s", auditData)
	}
	// pi accepted it into its queue; that it was read cannot be told from a
	// receipt, so the run stays accepted and its board task says the rest.
	if run.State != StateQueued || run.Attempts[0].State != "unconfirmed" {
		t.Fatalf("after the lead read it: %+v", run)
	}
	if v := runView(run, Plan{}, boardTasks(store.Board(ws)), nil, false); v.Status != StateSent {
		t.Fatalf("shown as %s once pi no longer reports it queued", v.Status)
	}
	t.Logf("real pi: queued behind the user's answer, read after it; transcript %d user messages; audit %d bytes", len(users), len(auditData))
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func runOf(t *testing.T, svc *Service, id string) Run {
	t.Helper()
	v, err := svc.View(ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range v.Plans {
		for _, r := range p.Runs {
			if r.ID == id {
				return r.Run
			}
		}
	}
	t.Fatalf("no run %s", id)
	return Run{}
}
