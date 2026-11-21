package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/restart"
	"github.com/idy/yesman/internal/yesmen"
)

func restartLead(y yesmen.Yesman) restart.Agent {
	return restart.Agent{Yesman: y.Name, Name: "lead", Role: RoleLead, WorkspaceHiredAt: y.HiredAt, AgentSince: y.HiredAt, Reason: restart.ReasonWorking}
}

func TestRestartCompositionColdPauses(t *testing.T) {
	for _, which := range []string{"lead-stop", "lead-abort", "worker-stop", "unknown-lead"} {
		t.Run(which, func(t *testing.T) {
			store, tmp, y := restartStore(t)
			old, _ := restartInstance(t, store, tmp)
			r := restartLead(y)
			if which == "worker-stop" {
				w := addWorker(t, store, "worker")
				r.Name, r.Role, r.AgentSince = w.Name, RoleWorker, w.StartedAt
			}
			id := ID(r.Yesman, r.Name)
			if which == "lead-abort" {
				if _, _, err := old.ensure(id); err != nil {
					t.Fatal(err)
				}
				if err := old.AbortByUser(context.Background(), id); err != nil {
					t.Fatal(err)
				}
			} else if which != "unknown-lead" {
				old.StopByUser(id)
			}
			old.StopAll()
			path := filepath.Join(y.Path, ".yesman", "continuation.json")
			bad := []byte("{invalid pause record\n")
			if which == "unknown-lead" {
				if err := os.WriteFile(path, bad, 0600); err != nil {
					t.Fatal(err)
				}
			}
			m, got := restartInstance(t, store, tmp)
			if o, err := m.ResumeAfterRestart(context.Background(), r, "restart note"); o != restart.Stopped {
				t.Fatalf("got %s %v", o, err)
			}
			if m.running(id) != nil || len(got.to(id)) != 0 {
				t.Fatal("cold pause woke a process")
			}
			if which == "unknown-lead" {
				if b, _ := os.ReadFile(path); !bytes.Equal(b, bad) {
					t.Fatal("unreadable pause was rewritten")
				}
			}
		})
	}
}

func TestRestartCompositionWaitSurvivesAnotherOpen(t *testing.T) {
	for _, role := range []string{RoleLead, RoleWorker} {
		t.Run(role, func(t *testing.T) {
			store, tmp, y := restartStore(t)
			r := restartLead(y)
			if role == RoleWorker {
				w := addWorker(t, store, "waiter")
				r.Name, r.Role, r.AgentSince = w.Name, role, w.StartedAt
			}
			r.Reason = restart.ReasonNeedsYou
			addTasks(t, store, yesmen.TaskEdit{Title: str("waiting task"), Status: str("doing"), Owner: str(r.Name)})
			m, got := restartInstance(t, store, tmp)
			finish := m.HoldContinuations()
			if o, err := m.ResumeAfterRestart(context.Background(), r, "must not send"); o != restart.Waiting || err != nil {
				t.Fatalf("got %s %v", o, err)
			}
			finish()
			quiet(t, got.to, "alice/lead", 0)
			m.StopAll()
			next, after := restartInstance(t, store, tmp)
			next.Resume()
			quiet(t, after.to, "alice/lead", 0)
			if next.running("alice/lead") != nil || next.running(ID(r.Yesman, r.Name)) != nil {
				t.Fatal("ordinary reopen lost human wait")
			}
			if err := next.Prompt(context.Background(), ID(r.Yesman, r.Name), "explicit answer"); err != nil {
				t.Fatal(err)
			}
			if after.notes(ID(r.Yesman, r.Name), "explicit answer") != 1 {
				t.Fatal("explicit answer did not release wait")
			}
		})
	}
}

func TestRestartCompositionOneTaskNotice(t *testing.T) {
	store, tmp, y := restartStore(t)
	addTasks(t, store, yesmen.TaskEdit{Title: str("interrupted goal")})
	m, got := restartInstance(t, store, tmp)
	finish := m.HoldContinuations()
	m.continueTasks("alice")
	if m.running("alice/lead") != nil {
		t.Fatal("startup evaluation passed recovery reservation")
	}
	if o, err := m.ResumeAfterRestart(context.Background(), restartLead(y), "restart note"); o != restart.Resumed || err != nil {
		t.Fatalf("got %s %v", o, err)
	}
	finish()
	finish() // the startup release itself is idempotent
	quiet(t, got.to, "alice/lead", 0)
	if got.notes("alice/lead", "restart note") != 1 {
		t.Fatal("wrong restart note count")
	}
	c, err := store.Continuation("alice")
	if err != nil || len(c.Attempts) != 1 || !c.Tasks["t1"].Delivered {
		t.Fatalf("restart did not cover durable task state: %+v %v", c, err)
	}
	m.StopAll()
	next, after := restartInstance(t, store, tmp)
	next.Resume()
	quiet(t, after.to, "alice/lead", 0)
	if next.running("alice/lead") != nil {
		t.Fatal("another open repeated covered work")
	}
	addTasks(t, store, yesmen.TaskEdit{Title: str("new authorized goal")})
	next.continueTasks("alice")
	waitNoticeListing(t, after.to, "alice/lead", "t2")
}

func TestRestartCompositionRefusedNoteRestoresLedger(t *testing.T) {
	store, tmp, y := restartStore(t)
	addTasks(t, store, yesmen.TaskEdit{Title: str("interrupted goal")})
	m, got := restartInstanceEnv(t, store, tmp, "FAKE_PI_FAIL_PROMPTS=1")
	m.HoldContinuations()
	if o, _ := m.ResumeAfterRestart(context.Background(), restartLead(y), "refused"); o != restart.Failed {
		t.Fatalf("got %s", o)
	}
	c, err := store.Continuation("alice")
	if err != nil || len(c.Attempts) != 0 || len(c.Tasks) != 0 {
		t.Fatalf("definite refusal consumed task state: %+v %v", c, err)
	}
	if got.notes("alice/lead", "refused") != 0 {
		t.Fatal("fixture accepted refused prompt")
	}
}

func TestRestartCompositionPauseAtPipeAdmission(t *testing.T) {
	for _, role := range []string{RoleLead, RoleWorker} {
		t.Run(role, func(t *testing.T) {
			store, tmp, y := restartStore(t)
			r := restartLead(y)
			if role == RoleWorker {
				w := addWorker(t, store, "worker")
				r.Name, r.Role, r.AgentSince = w.Name, role, w.StartedAt
			}
			m, got := restartInstance(t, store, tmp)
			id := ID(r.Yesman, r.Name)
			m.mu.Lock()
			a, err := m.workspaceAgentLocked(y, Info{ID: id, Yesman: r.Yesman, Name: r.Name, Role: r.Role, Since: r.AgentSince})
			m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			a.modelMu.Lock()
			done := make(chan string, 1)
			go func() { o, _ := m.ResumeAfterRestart(context.Background(), r, "late note"); done <- o }()
			eventually(t, "started process", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return a.proc != nil })
			m.mu.Lock()
			p := a.proc
			m.mu.Unlock()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			p.reviewBeforeWrite = func(v any) {
				if cmd, ok := v.(map[string]any); ok && cmd["type"] == "prompt" {
					once.Do(func() { close(entered); <-release })
				}
			}
			a.modelMu.Unlock()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("no pipe admission")
			}
			persist := m.pauseByUser(id) // pause linearizes before the pending write
			close(release)
			select {
			case o := <-done:
				if o != restart.Stopped {
					t.Fatalf("got %s", o)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("resume did not return")
			}
			if err := persist(); err != nil {
				t.Fatal(err)
			}
			if got.notes(id, "late note") != 0 {
				t.Fatal("pause admitted late note")
			}
		})
	}
}

func TestRestartCompositionStaleGenerationLeavesReplacement(t *testing.T) {
	for _, replacement := range []string{"workspace", "deleted workspace", "worker"} {
		t.Run(replacement, func(t *testing.T) {
			store, tmp, y := restartStore(t)
			w := addWorker(t, store, "worker")
			r := restart.Agent{Yesman: y.Name, Name: w.Name, Role: RoleWorker, WorkspaceHiredAt: y.HiredAt, AgentSince: w.StartedAt, Reason: restart.ReasonReport, Report: &restart.Report{State: restart.ReportAccepted}}
			if replacement != "worker" {
				if replacement == "deleted workspace" {
					target, err := store.PrepareDeletion(y.Name, y.HiredAt)
					if err != nil {
						t.Fatal(err)
					}
					unused := New(Config{Yesmen: store})
					defer unused.StopAll()
					_, err = store.Delete(t.Context(), target, func(context.Context, string) error { return nil }, func(y yesmen.Yesman) error { return unused.StopYesman(y.Name) })
					if err != nil {
						t.Fatal(err)
					}
				} else if _, err := store.Archive(y.Name, nil); err != nil {
					t.Fatal(err)
				}
				var err error
				y, err = store.Hire(y.Name)
				if err != nil {
					t.Fatal(err)
				}
				addWorker(t, store, "worker")
			} else {
				if err := store.RemoveWorker(y.Name, w.Name); err != nil {
					t.Fatal(err)
				}
				w.StartedAt = w.StartedAt.Add(time.Second)
				if err := store.AddWorker(y.Name, w); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(y.Path, ".yesman", "workers.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			m, got := restartInstance(t, store, tmp)
			if o, _ := m.ResumeAfterRestart(context.Background(), r, "no replay"); o != restart.Gone {
				t.Fatalf("got %s", o)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) || len(got.to("alice/lead")) != 0 || len(got.to("alice/worker")) != 0 {
				t.Fatal("stale accepted report changed replacement")
			}
		})
	}
}
