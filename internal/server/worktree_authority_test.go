package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

func retainedBoardReturn(t *testing.T, purpose string) (*worktreeFixture, pool.Worktree, yesmen.WorktreeReturn, yesmen.WorktreeRecord) {
	t.Helper()
	x, wt, v := returnFixture(t, purpose, "")
	submitReturn(t, x, v)
	x.f.m.Stop(x.me.agent.ID)
	ignored := filepath.Join(wt.Path, "ignored")
	if err := os.WriteFile(ignored, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if x.f.srv.reconcileCheckout(t.Context(), x.y, wt.CleanupID) != retryLater {
		t.Fatal("expected pending cleanup after certification")
	}
	rec := recordOf(t, x, wt.Dir)
	if rec.Removed || rec.Completion == nil || rec.BoardRound.IsZero() || rec.Completion.ReturnID == "" {
		t.Fatalf("expected a certified Board receipt, got %+v", rec)
	}
	if err := os.Remove(ignored); err != nil {
		t.Fatal(err)
	}
	return x, wt, v, rec
}

func savedRetry(t *testing.T, x *worktreeFixture, who childTrace, wt pool.Worktree, handoff string) cleanupResult {
	t.Helper()
	res := managementPost(t, x, who.Token, "/v1/worktrees/remove", removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID, Handoff: handoff})
	var out cleanupResult
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &out) != nil {
		t.Fatalf("retry: %d %s", res.Code, res.Body)
	}
	return out
}

func TestSavedBoardReturnRequiresCurrentAuthority(t *testing.T) {
	for _, kind := range []string{"author/keep", "author/reopen", "review/keep", "review/reopen", "author/current", "review/current"} {
		t.Run(kind, func(t *testing.T) {
			purpose, action, _ := strings.Cut(kind, "/")
			x, wt, _, cert := retainedBoardReturn(t, purpose)
			lead := x.f.start("alice/lead")
			if action != "current" {
				e := map[string]any{"id": "t1"}
				if action == "keep" {
					e["worktrees"] = []any{}
				} else {
					e["status"] = "doing"
				}
				if res := managementPost(t, x, lead.Token, "/v1/board", map[string]any{"update": []any{e}}); res.Code != 200 {
					t.Fatal(res.Body)
				}
				// Reopen the same hire with a fresh manager and process credential.
				x.f.m.StopAll()
				store := yesmen.New(filepath.Dir(x.y.Path), filepath.Join(filepath.Dir(filepath.Dir(x.y.Path)), "archive"))
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				m := agent.New(agent.Config{Yesmen: store, Pi: exe, Env: []string{"YESMAN_TEST_RPC_CHILD=" + x.f.traces, "GORACE=atexit_sleep_ms=0"}})
				t.Cleanup(m.StopAll)
				x.f.store, x.f.m = store, m
				x.f.srv = &Server{Yesmen: store, Agents: m, Pool: x.cf.s.Pool, ReadPullMerge: x.cf.s.ReadPullMerge}
				lead = x.f.start("alice/lead")
			}
			out := savedRetry(t, x, lead, wt, "")
			if action == "current" {
				if !out.Removed {
					t.Fatal(out.Reason)
				}
				if err := x.f.srv.Pool.CheckWorktreeCheckpoint(t.Context(), wt); err != nil {
					t.Fatal(err)
				}
				return
			}
			if out.Removed || !strings.Contains(out.Reason, "no longer current") {
				t.Fatalf("replayed receipt: %+v", out)
			}
			if out = savedRetry(t, x, lead, wt, "other"); out.Removed || !strings.Contains(out.Reason, "no longer current") {
				t.Fatalf("delegated stale receipt: %+v", out)
			}
			assertRetained(t, x, wt)
			if rec := recordOf(t, x, wt.Dir); !sameCertifiedReturn(*rec.Completion, *cert.Completion) {
				t.Fatal("historical certification changed")
			}
		})
	}
}

func TestReopenedWorktreeReturnPreservesEachCertification(t *testing.T) {
	x, wt, v, original := retainedBoardReturn(t, "author")
	lead := x.f.start("alice/lead")
	post := func(update map[string]any) {
		t.Helper()
		if res := managementPost(t, x, lead.Token, "/v1/board", map[string]any{"update": []any{update}}); res.Code != 200 {
			t.Fatal(res.Body)
		}
	}
	post(map[string]any{"id": "t1", "status": "doing"})
	post(map[string]any{"id": "t1", "status": "done", "note": "Reviewed again for this release, with an updated external handoff", "worktrees": []yesmen.WorktreeReturn{v}})
	b, err := x.f.store.Board(x.y.Name)
	if err != nil {
		t.Fatal(err)
	}
	current := b.Tasks[0].Returns[0].WorktreeCompletion
	if current.CertifiedBy != "lead" || current.ReturnID == original.Completion.ReturnID {
		t.Fatal("new round did not get an independent return")
	}
	if out := savedRetry(t, x, lead, wt, ""); out.Removed {
		t.Fatal("manual retry reused a superseded return")
	}
	x.f.srv.reconcileCheckout(t.Context(), x.y, wt.CleanupID)
	rec := recordOf(t, x, wt.Dir)
	if !rec.Removed || !rec.BoardRound.Equal(b.Tasks[0].DoneAt) || !sameCertifiedReturn(*rec.Completion, current) || len(rec.PriorReturns) != 1 || !rec.PriorReturns[0].Round.Equal(original.BoardRound) || !sameCertifiedReturn(rec.PriorReturns[0].Completion, *original.Completion) {
		t.Fatalf("incorrect round history: %+v", rec)
	}
}

func TestManualVerificationAllowsBoardWithdrawal(t *testing.T) {
	x, wt, _, _ := retainedBoardReturn(t, "author")
	lead := x.f.start("alice/lead")
	s := x.f.srv
	read := s.ReadPullMerge
	entered, cut := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(cut) })
	defer unblock()
	s.ReadPullMerge = func(ctx context.Context, target string, n int) (github.PullMerge, error) {
		close(entered)
		select {
		case <-cut:
		case <-ctx.Done():
			return github.PullMerge{}, ctx.Err()
		}
		return read(ctx, target, n)
	}
	body, err := json.Marshal(removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/worktrees/remove", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+lead.Token)
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() { out := httptest.NewRecorder(); s.Handler().ServeHTTP(out, req); finished <- out }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("verification did not start")
	}
	changed := make(chan *httptest.ResponseRecorder, 1)
	go func() { changed <- x.f.post(lead.Token, `{"update":[{"id":"t1","worktrees":[]}]}`) }()
	select {
	case res := <-changed:
		if res.Code != 200 {
			t.Fatal(res.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("board blocked on remote verification")
	}
	unblock()
	select {
	case res := <-finished:
		var out cleanupResult
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &out) != nil || out.Removed || !strings.Contains(out.Reason, "no longer current") {
			t.Fatalf("withdrawal: %d %s", res.Code, res.Body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("retry did not finish")
	}
	assertRetained(t, x, wt)
}

func TestSavedReturnBindsCertifierAndGeneration(t *testing.T) {
	x, wt, v, cert := retainedBoardReturn(t, "review")
	lead := x.f.start("alice/lead")
	// The lead attests the same source, outcome and note within the same
	// task close. The previous worker certification is now historical.
	res := managementPost(t, x, lead.Token, "/v1/board", map[string]any{"update": []any{map[string]any{"id": "t1", "note": "Delivered tested source. The push to feature and publication receipt are saved in the task evidence.", "worktrees": []yesmen.WorktreeReturn{v}}}})
	if res.Code != 200 {
		t.Fatal(res.Body)
	}
	if out := savedRetry(t, x, lead, wt, ""); out.Removed || !strings.Contains(out.Reason, "no longer current") {
		t.Fatalf("stale worker certification: %+v", out)
	}
	_, b, workers, err := x.f.store.WorktreeState(x.y)
	if err != nil {
		t.Fatal(err)
	}
	replacement := b.Tasks[0].Returns[0].WorktreeCompletion
	if !sameWorktreeCompletion(*cert.Completion, replacement) {
		t.Fatal("fixture changed the source or evidence")
	}
	// Even equal wall clocks do not substitute one return ID for another.
	replacement.SavedAt = cert.Completion.SavedAt
	replacement.CertifiedBy, replacement.CertifiedSince = cert.Completion.CertifiedBy, cert.Completion.CertifiedSince
	b.Tasks[0].Returns[0].WorktreeCompletion = replacement
	if CheckSavedWorktreeReturn(x.y, cert, b, workers) == nil {
		t.Fatal("different return ID certified an old request")
	}
	b.Tasks[0].Returns[0].WorktreeCompletion = *cert.Completion
	if err := CheckSavedWorktreeReturn(x.y, cert, b, workers); err != nil {
		t.Fatal(err)
	}
	for i := range workers {
		if workers[i].Name == cert.Owner {
			workers[i].StartedAt = workers[i].StartedAt.Add(time.Second)
		}
	}
	if CheckSavedWorktreeReturn(x.y, cert, b, workers) == nil {
		t.Fatal("replacement owner certified an old return")
	}
	assertRetained(t, x, wt)
}

func TestClosingOtherTaskWakesLocalWorktreeWait(t *testing.T) {
	x, wt, v := returnFixture(t, "review", "")
	submitReturn(t, x, v)
	x.f.m.Stop(x.me.agent.ID)
	title, owner := "Using the returned checkout", x.me.agent.Name
	if _, err := x.f.store.ChangeBoard(x.y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: &title, Owner: &owner}}}); err != nil {
		t.Fatal(err)
	}
	s := x.f.srv
	if w := s.reconcileCheckout(t.Context(), x.y, wt.CleanupID); w != waitForOwner {
		t.Fatalf("open follow-up wait = %v", w)
	}
	r := NewWorktreeReclaimer(s)
	s.Reclaimer = r
	defer r.Close()
	key := checkoutKey{x.y.Name, x.y.HiredAt, wt.CleanupID}
	deadline := time.Now().Add(time.Hour)
	job := checkoutJob{y: x.y, due: deadline, delay: time.Hour, owner: owner, waitForIdle: true}
	r.jobs[key] = job
	lead := x.f.start("alice/lead")
	if res := x.f.post(lead.Token, `{"update":[{"id":"t2","status":"done"}]}`); res.Code != 200 {
		t.Fatal(res.Body)
	}
	if _, ok := r.jobs[checkoutKey{workspace: x.y.Name, hire: x.y.HiredAt}]; !ok {
		t.Fatal("missing Board discovery")
	}
	if err := r.scan(x.y); err != nil {
		t.Fatal(err)
	}
	if due := r.jobs[key].due; due.After(time.Now()) {
		t.Fatalf("still waiting on a completed follow-up until %s", due)
	}
	job.waitForIdle = false // a remote verification of this source is backing off
	r.jobs[key] = job
	if err := r.scan(x.y); err != nil {
		t.Fatal(err)
	}
	r.OwnerRetired(x.y.Name, owner)
	if due := r.jobs[key].due; !due.Equal(deadline) {
		t.Fatalf("local events moved the remote retry to %s", due)
	}
}

func TestWithdrawnReturnCanRecoverCompletedRemoval(t *testing.T) {
	x, wt, _, _ := retainedBoardReturn(t, "review")
	s := x.f.srv
	s.reconcileCheckout(t.Context(), x.y, wt.CleanupID)
	records, err := x.f.store.WorktreeRecords(x.y.Name)
	if err != nil || !records[0].Removed {
		t.Fatal("initial certified removal", err)
	}
	records[0].Removed = false
	if err := x.f.store.SaveWorktreeRecords(x.y.Name, records); err != nil {
		t.Fatal(err)
	}
	lead := x.f.start("alice/lead")
	if res := x.f.post(lead.Token, `{"update":[{"id":"t1","worktrees":[]}]}`); res.Code != 200 {
		t.Fatal(res.Body)
	}
	if err := os.Mkdir(wt.Path, 0700); err != nil {
		t.Fatal(err)
	}
	s.reconcileCheckout(t.Context(), x.y, wt.CleanupID)
	assertRetained(t, x, wt)
	if err := os.Remove(wt.Path); err != nil {
		t.Fatal(err)
	}
	s.reconcileCheckout(t.Context(), x.y, wt.CleanupID)
	if _, err := os.Lstat(wt.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if rec := recordOf(t, x, wt.Dir); !rec.Removed || !strings.Contains(rec.RetiredReason, "checkpoint") || !sameCertifiedReturn(*rec.Completion, *records[0].Completion) {
		t.Fatalf("recovery result: %+v", rec)
	}
}
