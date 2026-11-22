package server

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/yesmen"
)

func TestCreatingTaskRecloseChecksPublishedAlias(t *testing.T) {
	for _, mode := range []string{"automatic", "bulk"} {
		t.Run(mode, func(t *testing.T) {
			x, w, offered := returnFixture(t, "author", "integration/local")
			s := x.f.srv
			s.ReadPullMerge = func(context.Context, string, int) (github.PullMerge, error) {
				return github.PullMerge{State: "open"}, nil
			}
			submitReturn(t, x, offered)
			x.f.m.Stop(x.me.agent.ID)
			if got := s.reconcileCheckout(t.Context(), x.y, w.CleanupID); got != retryLater || recordOf(t, x, w.Dir).Completion != nil {
				t.Fatal("the open return was certified", got)
			}
			lead := x.f.start("alice/lead")
			ordinaryTaskEdit(t, x, lead.Token, yesmen.TaskDoing, false)
			ordinaryTaskEdit(t, x, lead.Token, yesmen.TaskDone, false)

			// A fresh Store and Manager have only the durable task and checkout.
			store := yesmen.New(filepath.Dir(x.y.Path), filepath.Join(filepath.Dir(filepath.Dir(x.y.Path)), "archive"))
			m := agent.New(agent.Config{Yesmen: store})
			defer m.StopAll()
			cold := &Server{Pool: s.Pool, Yesmen: store, Agents: m}
			cold.ReadPullMerge = func(context.Context, string, int) (github.PullMerge, error) {
				t.Error("the previous return was resurrected as cleanup authority")
				return github.PullMerge{}, errors.New("stale return")
			}
			refs := cleanupGit(t, w.Path, "for-each-ref", "--format=%(refname) %(objectname)")
			var aliasReads int
			state := "open"
			cold.HasOpenPull = func(_ context.Context, target, owner, branch string) (bool, error) {
				if target != "acme/demo" || owner != "acme" || (branch != w.Branch && branch != offered.SourceBranch) {
					t.Errorf("unexpected open-PR scope %s %s %s", target, owner, branch)
				}
				if branch != offered.SourceBranch {
					return false, nil
				}
				aliasReads++
				if state == "offline" {
					return false, errors.New("offline")
				}
				return state == "open", nil
			}
			d := &DesktopWorktrees{Server: cold}
			for _, status := range []string{"open", "offline"} {
				state = status
				before := aliasReads
				if mode == "automatic" {
					if got := cold.reconcileCheckout(t.Context(), x.y, w.CleanupID); got != retryLater {
						t.Fatalf("%s dependency should keep checkout, got %d", status, got)
					}
				} else {
					p := previewTree(t, d, x.y, nil, false)
					want := "This branch has an open pull request."
					if status == "offline" {
						want = "Open pull requests could not be checked. Try again when GitHub is available."
					}
					if len(p.Rows) != 1 || p.Rows[0].Reason != want {
						t.Fatal(p)
					}
				}
				if aliasReads <= before {
					t.Fatal("the published alias was not checked")
				}
				assertRetained(t, x, w)
			}
			state = "closed"
			if mode == "automatic" {
				if got := cold.reconcileCheckout(t.Context(), x.y, w.CleanupID); got != collectedOrIneligible {
					t.Fatal(got)
				}
			} else {
				p := previewTree(t, d, x.y, nil, true)
				state = "open"
				if row := confirmTree(t, d, x.y, p.Token, false); !strings.Contains(row.Detail, offered.SourceBranch) {
					t.Fatal(row)
				}
				assertRetained(t, x, w)
				state = "closed"
				p = previewTree(t, d, x.y, nil, true)
				confirmTree(t, d, x.y, p.Token, true)
			}
			if rec := recordOf(t, x, w.Dir); !rec.Removed || rec.Completion != nil || rec.CleanupHead != w.Head {
				t.Fatalf("ordinary cleanup receipt: %+v", rec)
			}
			assertCleanupRefsPreserved(t, x.cf, refs, w)
		})
	}
}

func TestWorktreeReclaimerWakesCurrentAssignee(t *testing.T) {
	x, w, _ := returnFixture(t, "review", "")
	s := x.f.srv
	ordinaryTaskEdit(t, x, x.w.Token, yesmen.TaskDone, false)
	x.f.m.Stop(x.me.agent.ID)
	next := "handoff"
	if err := x.f.store.AddWorker(x.y.Name, yesmen.Worker{Name: next, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	nextAgent := agent.ID(x.y.Name, next)
	x.f.start(nextAgent)
	r := NewWorktreeReclaimer(s)
	defer r.Close()
	if err := r.scan(x.y); err != nil {
		t.Fatal(err)
	}
	key := checkoutKey{x.y.Name, x.y.HiredAt, w.CleanupID}
	job, queued := r.jobs[key]
	if !queued || job.assignee != x.me.agent.Name {
		t.Fatal("checkout was not queued for its initial assignee", job)
	}
	// Model the retry state left by a busy owner at the service's backoff cap.
	job.waitForIdle, job.delay, job.due = true, time.Hour, time.Now().Add(time.Hour)
	r.jobs[key] = job
	remote := checkoutKey{x.y.Name, x.y.HiredAt, "remote-retry"}
	other := checkoutKey{"other-workspace", x.y.HiredAt, "other-review"}
	r.jobs[remote] = checkoutJob{assignee: next, due: job.due, delay: job.delay}
	r.jobs[other] = checkoutJob{assignee: next, due: job.due, delay: job.delay, waitForIdle: true}
	doing, done := yesmen.TaskDoing, yesmen.TaskDone
	changeDesktopTask(t, x.f.store, x.y, yesmen.TaskEdit{ID: "t1", Status: &doing, Owner: &next}, yesmen.TaskEdit{ID: "t1", Status: &done})
	if got := s.reconcileCheckout(t.Context(), x.y, w.CleanupID); got != waitForOwner {
		t.Fatal("running assignee did not hold cleanup", got)
	}
	assertRetained(t, x, w)
	if err := r.scan(x.y); err != nil {
		t.Fatal(err)
	}
	if after := r.jobs[key]; after.assignee != next || after.owner != job.owner || after.delay != job.delay || !after.due.Equal(job.due) || !after.waitForIdle {
		t.Fatalf("discovery did not refresh routing while preserving backoff: %+v", after)
	}
	r.OwnerRetired(x.y.Name, "unrelated")
	if !r.jobs[key].due.Equal(job.due) {
		t.Fatal("unrelated retirement advanced cleanup")
	}
	x.f.m.Stop(nextAgent)
	r.OwnerRetired(x.y.Name, next)
	if r.jobs[key].due.After(time.Now()) || !r.jobs[remote].due.Equal(job.due) || !r.jobs[other].due.Equal(job.due) {
		t.Fatal("retirement did not wake only the matching idle wait")
	}
	removed := make(chan struct{}, 1)
	s.Changed = func() { removed <- struct{}{} }
	r.Start()
	select {
	case <-removed:
	case <-time.After(15 * time.Second):
		t.Fatal("retirement did not prompt a fresh cleanup attempt")
	}
	r.Close()
	if !recordOf(t, x, w.Dir).Removed {
		t.Fatal("reclamation did not record the removal")
	}
	if err := s.Pool.CheckWorktreeCheckpoint(t.Context(), w); err != nil {
		t.Fatal(err)
	}
}
