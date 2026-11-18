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

// These endpoint controls use real current process credentials, synthetic Git
// and a local merge-response fixture. The older process-free cleanupFixture
// cases exercise resource policy below admission.
func managementPost(t *testing.T, x *worktreeFixture, token, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	x.f.srv.Handler().ServeHTTP(res, r)
	return res
}

func managedCheckout(t *testing.T, x *worktreeFixture, purpose string) pool.Worktree {
	t.Helper()
	req := CheckoutRequest{Repo: x.cf.repo.Name, Dir: "managed", Purpose: purpose, Task: "t1"}
	if purpose == "review" || purpose == "work" {
		req.Ref = x.cf.head
	} else {
		req.Branch = "feature"
	}
	res := managementPost(t, x, x.w.Token, "/v1/checkout", req)
	var wt pool.Worktree
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &wt) != nil || wt.CleanupID == "" {
		t.Fatalf("current checkout: %d %s", res.Code, res.Body)
	}
	return wt
}

func TestWorktreeManagementCurrentCaller(t *testing.T) {
	for _, purpose := range []string{"work", "review", "author", "release"} {
		t.Run(purpose, func(t *testing.T) {
			x := newWorktreeFixture(t)
			x.f.srv.ReadPullMerge = x.cf.s.ReadPullMerge
			wt := managedCheckout(t, x, purpose)
			if got := cleanupGit(t, wt.Path, "rev-parse", "HEAD"); got != x.cf.head {
				t.Fatalf("checkout head %s", got)
			}
			rec := recordOf(t, x, wt.Dir)
			if rec.Owner != x.me.agent.Name || !rec.OwnerSince.Equal(x.me.agent.Since) || rec.ID != wt.CleanupID {
				t.Fatalf("checkout ownership: %+v", rec)
			}
			if purpose == "work" {
				return // primary/unspecified lifecycle work is kept
			}
			outcome := "pass"
			if purpose != "review" {
				outcome = "done"
				status := "done"
				if _, err := x.f.store.ChangeBoard("alice", yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: &status}}}); err != nil {
					t.Fatal(err)
				}
			}
			refs := cleanupGit(t, wt.Path, "for-each-ref", "--format=%(refname) %(objectname)")
			req := removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID, Completion: x.cf.completion(outcome)}
			res := managementPost(t, x, x.w.Token, "/v1/worktrees/remove", req)
			var result cleanupResult
			if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &result) != nil || !result.Removed {
				t.Fatalf("current self-cleanup: %d %s", res.Code, res.Body)
			}
			if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
				t.Fatalf("removed worktree still present: %v", err)
			}
			assertCleanupRefsPreserved(t, x.cf, refs, wt)
			if rec := recordOf(t, x, wt.Dir); !rec.Removed || rec.Completion == nil {
				t.Fatalf("removal not recorded: %+v", rec)
			}
		})
	}
}

func TestWorktreeManagementDelegatedCleanup(t *testing.T) {
	x := newWorktreeFixture(t)
	wt := managedCheckout(t, x, "review")
	x.f.m.Stop("alice/helper")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := x.f.m.StartWorker(ctx, "alice", "cleanup", "Cleanup", "clean the completed review"); err != nil {
		t.Fatal(err)
	}
	delegate := x.f.start("alice/cleanup")
	lead := x.f.start("alice/lead")
	req := removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID, Completion: x.cf.completion("pass"), Handoff: "cleanup"}
	res := managementPost(t, x, lead.Token, "/v1/worktrees/remove", req)
	var handoff cleanupResult
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &handoff) != nil || handoff.Removed || !strings.Contains(handoff.Reason, "handed off") {
		t.Fatalf("lawful handoff: %d %s", res.Code, res.Body)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatal("handoff removed worktree", err)
	}
	res = managementPost(t, x, delegate.Token, "/v1/worktrees/remove", removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID})
	var result cleanupResult
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &result) != nil || !result.Removed {
		t.Fatalf("current delegated cleanup: %d %s", res.Code, res.Body)
	}
}

func TestWorktreeManagementCanceledVerification(t *testing.T) {
	x := newWorktreeFixture(t)
	wt := managedCheckout(t, x, "author")
	status := "done"
	if _, err := x.f.store.ChangeBoard("alice", yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: &status}}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	read := x.cf.s.ReadPullMerge
	x.f.srv.ReadPullMerge = func(ctx context.Context, repo string, n int) (github.PullMerge, error) {
		pr, err := read(ctx, repo, n)
		cancel() // the current caller cancels while ordinary verification finishes
		return pr, err
	}
	req := removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID, Completion: x.cf.completion("done")}
	raw, _ := json.Marshal(req)
	_, err := x.f.srv.removeWorktree(ctx, x.me, httptest.NewRequest("POST", "/v1/worktrees/remove", bytes.NewReader(raw)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled verification: %v", err)
	}
	if rec := recordOf(t, x, wt.Dir); rec.Removed || rec.Completion != nil || len(rec.Attempts) != 0 {
		t.Fatalf("canceled verification published evidence: %+v", rec)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "feature")); err != nil {
		t.Fatal("canceled verification removed checkout", err)
	}
	x.f.srv.ReadPullMerge = read
	res := managementPost(t, x, x.w.Token, "/v1/worktrees/remove", req)
	var result cleanupResult
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &result) != nil || !result.Removed {
		t.Fatalf("normal retry after cancellation: %d %s", res.Code, res.Body)
	}
}

// Admission checks stay at the helper boundary: the callback is just a counter,
// never an unauthorized checkout/removal against either an old or new handler.
func TestWorktreeOperationAdmissionAndErrorRelease(t *testing.T) {
	f := newBoardFixture(t)
	y := f.hire("alice")
	w := startHelper(t, f)
	info, err := f.m.Authenticate(w.Token)
	if err != nil {
		t.Fatal(err)
	}
	c := caller{agent: info, yesman: y, token: w.Token}
	called := 0
	fn := func(context.Context, yesmen.Yesman) error { called++; return nil }
	wrong := c
	wrong.yesman.HiredAt = y.HiredAt.Add(time.Second)
	if err := f.srv.worktreeOperation(context.Background(), wrong, fn); !errors.Is(err, agent.ErrRetired) || called != 0 {
		t.Fatalf("hire admission: %v, calls %d", err, called)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.srv.worktreeOperation(ctx, c, fn); !errors.Is(err, context.Canceled) || called != 0 {
		t.Fatalf("canceled admission: %v, calls %d", err, called)
	}
	want := errors.New("ordinary callback error")
	if err := f.srv.worktreeOperation(context.Background(), c, func(context.Context, yesmen.Yesman) error { return want }); !errors.Is(err, want) {
		t.Fatalf("callback error: %v", err)
	}
	if err := f.srv.worktreeOperation(context.Background(), c, fn); err != nil || called != 1 {
		t.Fatalf("current admission after error: %v, calls %d", err, called)
	}
	f.m.Stop("alice/helper")
	if err := f.srv.worktreeOperation(context.Background(), c, fn); !errors.Is(err, agent.ErrRetired) || called != 1 {
		t.Fatalf("ended process admission: %v, calls %d", err, called)
	}
}

func TestWorktreeOperationCancellationAndDrain(t *testing.T) {
	for _, end := range []string{"stop", "archive", "shutdown"} {
		t.Run(end, func(t *testing.T) {
			f := newBoardFixture(t)
			y := f.hire("alice")
			w := startHelper(t, f)
			info, err := f.m.Authenticate(w.Token)
			if err != nil {
				t.Fatal(err)
			}
			c := caller{agent: info, yesman: y, token: w.Token}
			entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			done := make(chan error, 1)
			go func() {
				done <- f.srv.worktreeOperation(context.Background(), c, func(ctx context.Context, current yesmen.Yesman) error {
					close(entered)
					<-ctx.Done()
					close(canceled)
					<-release
					return ctx.Err()
				})
			}()
			waitSignal := func(ch <-chan struct{}) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(10 * time.Second):
					t.Fatal("operation lifecycle signal timed out")
				}
			}
			waitSignal(entered)
			ended := make(chan error, 1)
			go func() {
				switch end {
				case "stop":
					f.m.Stop("alice/helper")
					ended <- nil
				case "archive":
					_, err := f.store.Archive("alice", func(y yesmen.Yesman) error { return f.m.StopYesman(y.Name) })
					ended <- err
				case "shutdown":
					f.m.StopAll()
					ended <- nil
				}
			}()
			waitSignal(canceled)
			select {
			case err := <-ended:
				t.Fatalf("%s returned before operation drained: %v", end, err)
			default:
			}
			readable := make(chan struct{})
			go func() { f.m.List(); close(readable) }()
			waitSignal(readable)
			unblock()
			for _, ch := range []<-chan error{done, ended} {
				select {
				case err := <-ch:
					if err != nil && !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("operation/retirement did not join")
				}
			}
		})
	}
}
