package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

type cleanupFixture struct {
	s                     *Server
	y                     yesmen.Yesman
	repo                  pool.Repo
	head, merge           string
	owner, lead, delegate caller
}

func cleanupGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newCleanupFixture(t *testing.T) *cleanupFixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin")
	if err := os.MkdirAll(origin, 0755); err != nil {
		t.Fatal(err)
	}
	cleanupGit(t, origin, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(origin, "README"), []byte("base\n"), 0644)
	os.WriteFile(filepath.Join(origin, ".gitignore"), []byte("ignored\n"), 0644)
	cleanupGit(t, origin, "add", ".")
	cleanupGit(t, origin, "commit", "-qm", "base")
	cleanupGit(t, origin, "checkout", "-qb", "feature")
	os.WriteFile(filepath.Join(origin, "feature"), []byte("one\n"), 0644)
	cleanupGit(t, origin, "add", ".")
	cleanupGit(t, origin, "commit", "-qm", "feature one")
	os.WriteFile(filepath.Join(origin, "feature"), []byte("two\n"), 0644)
	cleanupGit(t, origin, "commit", "-qam", "feature two")
	head := cleanupGit(t, origin, "rev-parse", "HEAD")
	cleanupGit(t, origin, "checkout", "-q", "main")
	cleanupGit(t, origin, "merge", "--squash", "feature")
	cleanupGit(t, origin, "commit", "-qm", "squash")
	merge := cleanupGit(t, origin, "rev-parse", "HEAD")
	cleanupGit(t, origin, "tag", "v-fixture")
	p := pool.New(filepath.Join(dir, "repos"))
	r, err := p.Add(context.Background(), origin, "")
	if err != nil {
		t.Fatal(err)
	}
	// Give the isolated local fixture a GitHub identity; fetches still use
	// its local origin. No network, real pool or external model is touched.
	path := filepath.Join(p.Root(), "github.com", "acme", "demo.git")
	os.MkdirAll(filepath.Dir(path), 0755)
	if err := os.Rename(r.Path, path); err != nil {
		t.Fatal(err)
	}
	r, err = p.Get("github.com/acme/demo")
	if err != nil {
		t.Fatal(err)
	}
	store := yesmen.New(filepath.Join(dir, "yesmen"), filepath.Join(dir, "archive"))
	y, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"owner", "cleanup"} {
		if err := store.AddWorker(y.Name, yesmen.Worker{Name: name, StartedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	str := func(s string) *string { return &s }
	_, err = store.ChangeBoard(y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: str("Fixture task"), Owner: str("owner"), Status: str("doing")}}})
	if err != nil {
		t.Fatal(err)
	}
	m := agent.New(agent.Config{Yesmen: store})
	s := &Server{Pool: p, Yesmen: store, Agents: m}
	t.Cleanup(m.StopAll)
	f := &cleanupFixture{s: s, y: y, repo: r, head: head, merge: merge}
	f.owner = f.caller(t, "owner")
	f.lead = f.caller(t, "lead")
	f.delegate = f.caller(t, "cleanup")
	s.ReadPullMerge = func(_ context.Context, repo string, number int) (github.PullMerge, error) {
		if repo != "acme/demo" || number != 7 {
			return github.PullMerge{}, fmt.Errorf("unexpected PR %s#%d", repo, number)
		}
		var pr github.PullMerge
		json.Unmarshal([]byte(fmt.Sprintf(`{"state":"closed","merged":true,"merged_at":"2026-01-02T00:00:00Z","merge_commit_sha":%q,"head":{"sha":%q,"ref":"feature","repo":{"full_name":"acme/demo"}}}`, merge, head)), &pr)
		return pr, nil
	}
	return f
}

func (f *cleanupFixture) caller(t *testing.T, name string) caller {
	t.Helper()
	i, err := f.s.Agents.Get(agent.ID(f.y.Name, name))
	if err != nil {
		t.Fatal(err)
	}
	return caller{agent: i, yesman: f.y}
}

func (f *cleanupFixture) checkout(t *testing.T, purpose, dir string) pool.Worktree {
	t.Helper()
	req := CheckoutRequest{Repo: f.repo.Name, Dir: dir, Task: "t1", Purpose: purpose}
	if purpose == "review" {
		req.Ref = f.head
	} else {
		req.Branch = "feature"
	}
	body, _ := json.Marshal(req)
	out, err := f.s.checkout(context.Background(), f.owner, httptest.NewRequest("POST", "/v1/checkout", bytes.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	return out.(pool.Worktree)
}

func (f *cleanupFixture) remove(t *testing.T, who caller, req removeWorktreeRequest) cleanupResult {
	t.Helper()
	if req.Checkout == "" {
		records, _ := f.s.Yesmen.WorktreeRecords(who.yesman.Name)
		if i := latestWorktree(records, req.Dir); i >= 0 {
			req.Checkout = records[i].ID
		}
	}
	body, _ := json.Marshal(req)
	out, err := f.s.removeWorktree(context.Background(), who, httptest.NewRequest("POST", "/v1/worktrees/remove", bytes.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	return out.(cleanupResult)
}

func (f *cleanupFixture) completion(outcome string) *yesmen.WorktreeCompletion {
	return &yesmen.WorktreeCompletion{Head: f.head, Outcome: outcome, Ref: "https://github.com/acme/demo/pull/7", Evidence: "Conclusion and test evidence saved in board t1/session owner. Rebuild with yesman_checkout repo github.com/acme/demo ref " + f.head + " purpose review task t1.", Unused: true}
}

func (f *cleanupFixture) finishTask(t *testing.T) {
	t.Helper()
	status := "done"
	if _, err := f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: &status}}}); err != nil {
		t.Fatal(err)
	}
}

func assertCleanupRefsPreserved(t *testing.T, f *cleanupFixture, before string, wt pool.Worktree) {
	t.Helper()
	after := cleanupGit(t, f.repo.Path, "for-each-ref", "--format=%(refname) %(objectname)")
	for _, ref := range strings.Split(before, "\n") {
		if !strings.Contains("\n"+after+"\n", "\n"+ref+"\n") {
			t.Fatalf("ref removed/changed: %s", ref)
		}
	}
	if got := cleanupGit(t, f.repo.Path, "rev-parse", "refs/yesman/worktrees/"+wt.CleanupID); got != wt.Head {
		t.Fatalf("recovery ref = %s, want %s", got, wt.Head)
	}
}

func TestReviewCleanupAndExactRecovery(t *testing.T) {
	for _, outcome := range []string{"pass", "waiting-release", "findings-handed-off"} {
		t.Run(outcome, func(t *testing.T) {
			f := newCleanupFixture(t)
			wt := f.checkout(t, "review", "review")
			refs := cleanupGit(t, wt.Path, "for-each-ref", "--format=%(refname) %(objectname)")
			// The PR deliberately remains OPEN for review cleanup. No GitHub
			// query is appropriate when only this review round is complete.
			f.s.ReadPullMerge = func(context.Context, string, int) (github.PullMerge, error) {
				t.Fatal("review queried merge status")
				return github.PullMerge{}, nil
			}
			out := f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir, Completion: f.completion(outcome)})
			if !out.Removed {
				t.Fatalf("cleanup: %+v", out)
			}
			if _, err := os.Lstat(wt.Path); !os.IsNotExist(err) {
				t.Fatal("tree still exists")
			}
			if wts, _ := f.s.Pool.Worktrees(context.Background(), f.y.Path); len(wts) != 0 {
				t.Fatalf("registration remains: %+v", wts)
			}
			assertCleanupRefsPreserved(t, f, refs, wt)
			if !f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir}).Removed {
				t.Fatal("repeated cleanup failed")
			}
			ctx := BuildContext(context.Background(), f.s.Pool, f.s.Agents, f.y, f.owner.agent, yesmen.Board{})
			if !strings.Contains(ctx, f.head) || !strings.Contains(ctx, "removed=true") {
				t.Fatal("missing recovery context")
			}
			// Restart the app, restore the exact reviewed source, and retain
			// the earlier completion after creating a new lifecycle record.
			f.s = &Server{Pool: f.s.Pool, Yesmen: yesmen.New(filepath.Dir(f.y.Path), filepath.Join(filepath.Dir(filepath.Dir(f.y.Path)), "archive"))}
			f.s.Agents = agent.New(agent.Config{Yesmen: f.s.Yesmen})
			t.Cleanup(f.s.Agents.StopAll)
			restored := f.checkout(t, "review", wt.Dir)
			if got := cleanupGit(t, restored.Path, "rev-parse", "HEAD"); got != f.head {
				t.Fatalf("restored HEAD %s", got)
			}
			records, _ := f.s.Yesmen.WorktreeRecords(f.y.Name)
			if len(records) != 2 || records[0].Completion.Outcome != outcome || !records[0].Removed || records[1].Completion != nil {
				t.Fatalf("history: %+v", records)
			}
			// A new round has no completion; an old repeated event must not
			// retire it, even at the same HEAD and directory.
			if out := f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID, Completion: f.completion(outcome)}); out.Removed || !strings.Contains(out.Reason, "ID changed") {
				t.Fatalf("old event removed new round: %+v", out)
			}
			if out := f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir}); out.Removed || !strings.Contains(out.Reason, "no saved completion") {
				t.Fatalf("new round removed: %+v", out)
			}
		})
	}
}

func TestCleanupRetainsUnknownDirtyAndChangedTrees(t *testing.T) {
	for _, kind := range []string{"dirty", "staged", "untracked", "ignored", "new-commit", "unknown", "non-owner", "primary", "hidden-index"} {
		t.Run(kind, func(t *testing.T) {
			f := newCleanupFixture(t)
			wt := f.checkout(t, "review", "review")
			who := f.owner
			switch kind {
			case "dirty", "staged":
				os.WriteFile(filepath.Join(wt.Path, "README"), []byte("new work"), 0644)
				if kind == "staged" {
					cleanupGit(t, wt.Path, "add", "README")
				}
			case "untracked", "ignored":
				os.WriteFile(filepath.Join(wt.Path, kind), []byte("keep"), 0644)
			case "new-commit":
				cleanupGit(t, wt.Path, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "--allow-empty", "-qm", "new work")
			case "unknown":
				if err := f.s.Yesmen.SaveWorktreeRecords(f.y.Name, nil); err != nil {
					t.Fatal(err)
				}
			case "non-owner":
				who = f.delegate
			case "primary":
				records, _ := f.s.Yesmen.WorktreeRecords(f.y.Name)
				records[0].Purpose = "primary"
				f.s.Yesmen.SaveWorktreeRecords(f.y.Name, records)
			case "hidden-index":
				cleanupGit(t, wt.Path, "update-index", "--assume-unchanged", "README")
				os.WriteFile(filepath.Join(wt.Path, "README"), []byte("hidden work"), 0644)
			}
			out := f.remove(t, who, removeWorktreeRequest{Dir: wt.Dir, Completion: f.completion("pass")})
			if out.Removed || out.Reason == "" {
				t.Fatalf("unsafe removal: %+v", out)
			}
			if _, err := os.Lstat(wt.Path); err != nil {
				t.Fatal("protected path missing", err)
			}
			if kind == "ignored" {
				records, _ := f.s.Yesmen.WorktreeRecords(f.y.Name)
				if records[0].Completion == nil {
					t.Fatal("evidence not saved before failed removal")
				}
			}
		})
	}
}

func TestStoppedRestartAndFallbackHandoff(t *testing.T) {
	f := newCleanupFixture(t)
	wt := f.checkout(t, "review", "review")
	// A reported/stopped worker and blocked task still lack completion.
	f.s.Yesmen.FinishWorker(f.y.Name, "owner", time.Now())
	status := "blocked"
	f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: &status}}})
	if out := f.remove(t, f.lead, removeWorktreeRequest{Dir: wt.Dir, Handoff: "cleanup"}); out.Removed || !strings.Contains(out.Reason, "no saved completion") {
		t.Fatalf("stopped cleanup: %+v", out)
	}
	// Lead verifies the completed report after an exit before cleanup.
	out := f.remove(t, f.lead, removeWorktreeRequest{Dir: wt.Dir, Completion: f.completion("findings-handed-off"), Handoff: "cleanup"})
	if out.Removed || !strings.Contains(out.Reason, "handed off") {
		t.Fatalf("handoff: %+v", out)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatal("handoff removed the tree")
	}
	if out := f.remove(t, f.delegate, removeWorktreeRequest{Dir: wt.Dir}); !out.Removed {
		t.Fatalf("delegate: %+v", out)
	}
	if !f.remove(t, f.delegate, removeWorktreeRequest{Dir: wt.Dir}).Removed {
		t.Fatal("repeated delegated cleanup failed")
	}
	os.MkdirAll(wt.Path, 0755)
	os.WriteFile(filepath.Join(wt.Path, "user-data"), []byte("keep"), 0644)
	if out := f.remove(t, f.delegate, removeWorktreeRequest{Dir: wt.Dir}); out.Removed || !strings.Contains(out.Reason, "reused") {
		t.Fatalf("reused path: %+v", out)
	}
}

func TestMergedBranchCleanupProtectsNewWorkAndReleases(t *testing.T) {
	for _, kind := range []string{"squash", "release-finished", "release-active", "release-dropped", "author-unfinished", "post-merge-commit", "open-pr", "wrong-repo", "wrong-branch", "wrong-head", "unverified"} {
		t.Run(kind, func(t *testing.T) {
			f := newCleanupFixture(t)
			purpose := "author"
			if strings.HasPrefix(kind, "release") {
				purpose = "release"
			}
			wt := f.checkout(t, purpose, "work")
			refs := cleanupGit(t, wt.Path, "for-each-ref", "--format=%(refname) %(objectname)")
			// Squash source is deliberately not an ancestor of merged main.
			cmd := exec.Command("git", "-C", wt.Path, "merge-base", "--is-ancestor", f.head, "origin/main")
			if err := cmd.Run(); err == nil {
				t.Fatal("fixture is not a squash merge")
			}
			if kind != "release-active" && kind != "author-unfinished" {
				f.finishTask(t)
			}
			if kind == "release-dropped" {
				status := "dropped"
				if _, err := f.s.Yesmen.ChangeBoard(f.y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Status: &status}}}); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "post-merge-commit" {
				cleanupGit(t, wt.Path, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "--allow-empty", "-qm", "post merge work")
			}
			read := f.s.ReadPullMerge
			f.s.ReadPullMerge = func(ctx context.Context, repo string, n int) (github.PullMerge, error) {
				pr, err := read(ctx, repo, n)
				switch kind {
				case "open-pr":
					pr.State, pr.Merged = "open", false
				case "wrong-repo":
					pr.Head.Repo.FullName = "someone/demo"
				case "wrong-branch":
					pr.Head.Ref = "other"
				case "wrong-head":
					pr.Head.SHA = f.merge
				case "unverified":
					return pr, fmt.Errorf("offline")
				}
				return pr, err
			}
			out := f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir, Completion: f.completion("done")})
			want := kind == "squash" || kind == "release-finished"
			if out.Removed != want {
				t.Fatalf("removed=%t want %t: %+v", out.Removed, want, out)
			}
			if want {
				assertCleanupRefsPreserved(t, f, refs, wt)
				if !f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir}).Removed {
					t.Fatal("repeat failed")
				}
			} else if _, err := os.Stat(wt.Path); err != nil {
				t.Fatal("protected worktree removed")
			}
		})
	}
}

func TestCleanupCannotCrossWorkspaceOrForce(t *testing.T) {
	f := newCleanupFixture(t)
	wt := f.checkout(t, "review", "review")
	other, err := f.s.Yesmen.Hire("bob")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := f.s.Agents.Get("bob/lead")
	out := f.remove(t, caller{agent: info, yesman: other}, removeWorktreeRequest{Dir: wt.Dir, Completion: f.completion("pass")})
	if out.Removed || !strings.Contains(out.Reason, "unknown ownership") {
		t.Fatalf("cross-workspace: %+v", out)
	}
	for _, req := range []removeWorktreeRequest{{Dir: "../review"}, {Dir: wt.Dir, Force: true}} {
		body, _ := json.Marshal(req)
		if _, err := f.s.removeWorktree(context.Background(), f.owner, httptest.NewRequest("POST", "/v1/worktrees/remove", bytes.NewReader(body))); err == nil {
			t.Fatalf("accepted unsafe request %+v", req)
		}
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatal("other workspace affected")
	}
}
