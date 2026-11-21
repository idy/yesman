package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/safefs"
	"github.com/idy/yesman/internal/safefs/linktest"
	"github.com/idy/yesman/internal/yesmen"
)

// A returned checkout may already have merged-source evidence and a recovery
// ref when the user archives it. Neither the present source before the move,
// nor the missing source afterwards, grants permission to retire the archive.
func TestWorktreeArchiveReservesReturnedCheckout(t *testing.T) {
	for _, phase := range []string{"pending", "archived"} {
		t.Run(phase, func(t *testing.T) {
			f, w, req := archiveFixture(t)
			proof := f.completion("done")
			handoff := f.remove(t, f.lead, removeWorktreeRequest{Dir: w.Dir, Completion: proof, Handoff: f.delegate.agent.Name})
			if handoff.Record == nil || handoff.Record.Completion == nil || handoff.Removed {
				t.Fatal(handoff)
			}
			cleanupGit(t, f.repo.Path, "update-ref", "refs/yesman/worktrees/"+w.CleanupID, w.Head)
			desktop := &DesktopWorktrees{Server: f.s}
			old := previewTree(t, desktop, f.y, &w, true)
			if phase == "pending" {
				f.s.Yesmen.ReplaceWorktreeJournal = journalFault(t, "archiving", true)
				if _, err := confirmMove(t, f, req); !errors.Is(err, failJournalSync) {
					t.Fatal(err)
				}
				f.s.Yesmen.ReplaceWorktreeJournal = nil
			} else {
				completeMove(t, f, req)
			}
			confirmTree(t, desktop, f.y, old.Token, false)
			if phase == "pending" {
				for _, p := range []WorktreeCleanupPreview{previewTree(t, desktop, f.y, &w, false), previewTree(t, desktop, f.y, nil, false)} {
					if p.Rows[0].Reason != archiveReserved {
						t.Fatal(p)
					}
				}
			}
			svc := NewWorktreeReclaimer(f.s)
			if err := svc.scan(f.y); err != nil || len(svc.jobs) != 0 {
				t.Fatalf("queued reserved checkout: %v, %v", svc.jobs, err)
			}
			if got := f.s.reconcileCheckout(t.Context(), f.y, w.CleanupID); got != collectedOrIneligible {
				t.Fatalf("reclaimer result %v", got)
			}
			if res := f.remove(t, f.owner, removeWorktreeRequest{Dir: w.Dir}); res.Removed || res.Record.Removed || res.Reason != "retain: "+archiveReserved {
				t.Fatal(res)
			}
			records, _, _, err := f.s.Yesmen.WorktreeState(f.y)
			if err != nil || len(records) != 1 || records[0].Removed || !records[0].ReservedForArchive() {
				t.Fatal(records, err)
			}
			if records[0].Completion.Head != proof.Head || records[0].CleanupOwner != f.delegate.agent.Name {
				t.Fatal("lost recorded completion or delegate")
			}
			if phase == "pending" {
				completeMove(t, f, req)
			}
			req.Action = "restore"
			completeMove(t, f, req)
			if got := f.s.reconcileCheckout(t.Context(), f.y, w.CleanupID); got != collectedOrIneligible {
				t.Fatal(got)
			}
			if preview := previewTree(t, desktop, f.y, nil, false); preview.Rows[0].Reason != "The restored checkout needs a fresh Board return for bulk cleanup." {
				t.Fatal(preview)
			}
			if got := cleanupGit(t, w.Path, "rev-parse", "HEAD"); got != w.Head {
				t.Fatal(got)
			}
		})
	}
}

func TestWorktreeArchiveThroughConfiguredRoot(t *testing.T) {
	f, w, req := archiveFixture(t)
	alias := filepath.Join(t.TempDir(), "settings-root")
	root := filepath.Dir(filepath.Dir(f.y.Path))
	if runtime.GOOS == "windows" {
		linktest.Junction(t, root, alias)
	} else {
		linktest.Symlink(t, root, alias)
	}
	store := yesmen.New(filepath.Join(alias, "yesmen"), filepath.Join(alias, "archive"))
	f.s = &Server{Yesmen: store, Pool: pool.New(filepath.Join(alias, "repos")), Agents: agent.New(agent.Config{Yesmen: store})}
	t.Cleanup(f.s.Agents.StopAll)
	moved := completeMove(t, f, req)
	expectedPath, err := safefs.PhysicalPath(w.Path)
	// The source is now absent. Its verified physical parent still exists.
	if !os.IsNotExist(err) {
		t.Fatalf("old path still resolves to %s: %v", expectedPath, err)
	}
	parent, err := safefs.PhysicalPath(filepath.Dir(w.Path))
	if err != nil {
		t.Fatal(err)
	}
	if moved.From != filepath.Join(parent, w.Dir) {
		t.Fatalf("unexpected source %s", moved.From)
	}
	req.Action = "restore"
	completeMove(t, f, req)
	if got := cleanupGit(t, w.Path, "rev-parse", "HEAD"); got != w.Head {
		t.Fatal(got)
	}
}

func TestWorktreeArchiveKeepsWorkspaceAndRestoreRecord(t *testing.T) {
	f, w, req := archiveFixture(t)
	target, err := f.s.Yesmen.PrepareDeletion(f.y.Name, f.y.HiredAt)
	if err != nil {
		t.Fatal(err)
	}
	moved := completeMove(t, f, req)
	// Settings lists legacy workspace archives. A linked checkout lives
	// outside the workspace, but still belongs to its original hire.
	if entries, err := f.s.Yesmen.StoredWorkspaces(); err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
	detached, err := f.s.Yesmen.Delete(t.Context(), target, f.s.Pool.CheckWorkspaceDeletion, func(yesmen.Yesman) error { t.Fatal("stopped a protected workspace"); return nil }, f.s.Pool.RelocatedPrimary)
	if detached || err == nil || !strings.Contains(err.Error(), "saved archive") {
		t.Fatal(detached, err)
	}
	if got := cleanupGit(t, moved.To, "rev-parse", "HEAD"); got != w.Head {
		t.Fatal(got)
	}
	req.Action = "restore"
	completeMove(t, f, req)
}

func restoreForAgentCleanup(t *testing.T, x *worktreeFixture, w pool.Worktree) {
	t.Helper()
	if err := os.MkdirAll(x.f.store.WorktreeArchiveDir(), 0700); err != nil {
		t.Fatal(err)
	}
	f := &cleanupFixture{s: x.f.srv, y: x.y}
	req := WorktreeMove{Workspace: x.y.Name, Since: x.y.HiredAt, Dir: w.Dir, ID: w.CleanupID, Action: "archive"}
	completeMove(t, f, req)
	req.Action = "restore"
	completeMove(t, f, req)
}

// A pre-archive certification and a saved delegate refer to the same checkout
// ID after restore. Exercise the authenticated retries with a present tree and
// with an absent source whose old checkpoint would otherwise permit retirement.
func TestRestoredWorktreeRejectsSavedAgentCleanup(t *testing.T) {
	for _, sourceState := range []string{"present", "missing"} {
		t.Run(sourceState, func(t *testing.T) {
			x, w, _, cert := retainedBoardReturn(t, "author")
			const delegate = "cleanup"
			if err := x.f.store.AddWorker(x.y.Name, yesmen.Worker{Name: delegate, StartedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			lead := x.f.start("alice/lead")
			if out := savedRetry(t, x, lead, w, delegate); out.Removed || out.Record == nil || out.Record.CleanupOwner != delegate {
				t.Fatalf("initial handoff: %+v", out)
			}
			restoreForAgentCleanup(t, x, w)
			before := recordOf(t, x, w.Dir)
			if before.Archive.PreviousReturnID != cert.Completion.ReturnID {
				t.Fatal("wrong pre-restore return")
			}
			source := w.Path
			if sourceState == "missing" {
				cleanupGit(t, x.cf.repo.Path, "update-ref", "refs/yesman/worktrees/"+w.CleanupID, w.Head)
				source = filepath.Join(t.TempDir(), "kept")
				cleanupGit(t, x.cf.repo.Path, "worktree", "move", w.Path, source)
			}
			check := func(out cleanupResult) {
				t.Helper()
				if out.Removed || !strings.Contains(out.Reason, "fresh Board return") {
					t.Fatalf("stale authority: %+v", out)
				}
				if !reflect.DeepEqual(before, recordOf(t, x, w.Dir)) {
					t.Fatal("stale retry changed the lifecycle record")
				}
				if got := cleanupGit(t, source, "rev-parse", "HEAD"); got != w.Head {
					t.Fatal(got)
				}
			}
			check(savedRetry(t, x, lead, w, ""))
			check(savedRetry(t, x, lead, w, x.me.agent.Name))
			who := x.f.start(agent.ID(x.y.Name, delegate))
			check(savedRetry(t, x, who, w, ""))
			if sourceState == "present" {
				x.f.m.Stop(agent.ID(x.y.Name, delegate))
				desktop := &DesktopWorktrees{Server: x.f.srv}
				preview := previewTree(t, desktop, x.y, &w, true)
				confirmTree(t, desktop, x.y, preview.Token, true)
			}
		})
	}
}

func TestRestoredWorktreeAgentUsesFreshBoardReturn(t *testing.T) {
	x, w, v := returnFixture(t, "author", "")
	submitReturn(t, x, v)
	x.f.m.Stop(x.me.agent.ID)
	restoreForAgentCleanup(t, x, w)
	lead := x.f.start("alice/lead")
	req := removeWorktreeRequest{Checkout: w.CleanupID, Dir: w.Dir, Completion: &v.WorktreeCompletion}
	post := func() cleanupResult {
		t.Helper()
		res := managementPost(t, x, lead.Token, "/v1/worktrees/remove", req)
		var out cleanupResult
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &out) != nil {
			t.Fatal(res.Body)
		}
		return out
	}
	if out := post(); out.Removed || out.Record == nil || out.Record.Completion != nil || !strings.Contains(out.Reason, "fresh Board return") {
		t.Fatalf("first proposal used old Board disposition: %+v", out)
	}
	x.w = x.f.start(x.me.agent.ID)
	submitReturn(t, x, v)
	x.f.m.Stop(x.me.agent.ID)
	req.Handoff = x.me.agent.Name
	if out := post(); out.Removed || out.Record == nil || out.Record.CleanupOwner != req.Handoff || out.Record.Completion == nil || !out.Record.AllowsCollection(out.Record.Completion.ReturnID) {
		t.Fatalf("fresh Board return did not authorize handoff: %+v", out)
	}
	if out := savedRetry(t, x, lead, w, ""); !out.Removed {
		t.Fatal(out.Reason)
	}
	if err := x.f.srv.Pool.CheckWorktreeCheckpoint(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	// Git succeeded with the fresh return. A retry can recover a lost final
	// registry write on the same post-restore certification.
	records, err := x.f.store.WorktreeRecords(x.y.Name)
	if err != nil {
		t.Fatal(err)
	}
	records[0].Removed = false
	if err := x.f.store.SaveWorktreeRecords(x.y.Name, records); err != nil {
		t.Fatal(err)
	}
	if out := savedRetry(t, x, lead, w, ""); !out.Removed {
		t.Fatal(out.Reason)
	}
}

func TestRestoredWorktreeMissingRecoveryRechecksBoard(t *testing.T) {
	x, w, v, _ := retainedBoardReturn(t, "author")
	restoreForAgentCleanup(t, x, w)
	s := x.f.srv

	// Certify a fresh return while an ignored file keeps Git's directory in place.
	x.w = x.f.start(x.me.agent.ID)
	submitReturn(t, x, v)
	x.f.m.Stop(x.me.agent.ID)
	writeFixture(t, filepath.Join(w.Path, "ignored"), "local data")
	if got := s.reconcileCheckout(t.Context(), x.y, w.CleanupID); got != retryLater {
		t.Fatal(got)
	}
	before := recordOf(t, x, w.Dir)
	if before.Completion == nil || !before.AllowsCollection(before.Completion.ReturnID) {
		t.Fatal("expected a certified post-restore return")
	}
	cleanupGit(t, x.cf.repo.Path, "update-ref", "refs/yesman/worktrees/"+w.CleanupID, w.Head)
	kept := filepath.Join(t.TempDir(), "kept")
	cleanupGit(t, x.cf.repo.Path, "worktree", "move", w.Path, kept)

	lead := x.f.start("alice/lead")
	info, err := x.f.m.Authenticate(lead.Token)
	if err != nil {
		t.Fatal(err)
	}
	c := caller{agent: info, yesman: x.y, token: lead.Token}
	req := removeWorktreeRequest{Dir: w.Dir, Checkout: w.CleanupID}
	proof, err := s.verifyRemoval(t.Context(), c, x.y, req)
	if err != nil || !proof.missing || proof.decision.returned.ReturnID != before.Completion.ReturnID {
		t.Fatal("missing-source verification", err)
	}
	// Withdraw permission between the two production phases. The registry,
	// original HEAD checkpoint and absent source all still match.
	if res := x.f.post(lead.Token, `{"update":[{"id":"t1","worktrees":[]}]}`); res.Code != 200 {
		t.Fatal(res.Body)
	}
	out, err := s.commitRemoval(t.Context(), c, x.y, req, proof)
	if err != nil || out.Removed || !strings.Contains(out.Reason, "fresh Board return") {
		t.Fatalf("stale missing-source commit: %+v, %v", out, err)
	}
	if !reflect.DeepEqual(before, recordOf(t, x, w.Dir)) {
		t.Fatal("withdrawing the return still retired the registry record")
	}
	// The remove handler schedules reconciliation when it retains a checkout
	// with saved evidence. Follow that same missing-source path as well.
	s.reconcileCheckout(t.Context(), x.y, w.CleanupID)
	if !reflect.DeepEqual(before, recordOf(t, x, w.Dir)) {
		t.Fatal("missing-source reconciliation retired the restored record after withdrawal")
	}
	if b, err := os.ReadFile(filepath.Join(kept, "ignored")); err != nil || string(b) != "local data" {
		t.Fatal("fixture local data changed", err)
	}
}

func TestWorktreeArchiveRestoreRequiresFreshBoardReturn(t *testing.T) {
	x, w, receipt := returnFixture(t, "author", "")
	if err := os.MkdirAll(x.f.store.WorktreeArchiveDir(), 0700); err != nil {
		t.Fatal(err)
	}
	submitReturn(t, x, receipt)
	x.f.m.Stop(x.me.agent.ID)
	move := WorktreeMove{Workspace: x.y.Name, Since: x.y.HiredAt, Dir: w.Dir, ID: w.CleanupID, Action: "archive"}
	f := &cleanupFixture{s: x.f.srv, y: x.y}
	completeMove(t, f, move)
	move.Action = "restore"
	completeMove(t, f, move)
	if reason := f.s.reconcileCheckout(context.Background(), x.y, w.CleanupID); reason != collectedOrIneligible {
		t.Fatal(reason)
	}
	assertRetained(t, x, w)
	// Repeat the actual Board return using the current owner's process. A
	// newly authenticated return may reclaim the checkout as usual.
	x.w = x.f.start(x.me.agent.ID)
	submitReturn(t, x, receipt)
	x.f.m.Stop(x.me.agent.ID)
	f.s.reconcileCheckout(context.Background(), x.y, w.CleanupID)
	if !recordOf(t, x, w.Dir).Removed {
		t.Fatal("new explicit return did not reclaim the restored checkout")
	}
	if _, err := os.Lstat(filepath.Join(x.y.Path, w.Dir)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
