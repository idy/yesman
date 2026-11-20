package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/yesmen"
)

func TestFirstManualCompletionUsesCurrentTaskDecision(t *testing.T) {
	for _, mode := range []string{"keep", "keep-handoff", "reopen-keep", "already-kept", "note-only", "handoff-then-reopen"} {
		t.Run(mode, func(t *testing.T) {
			x, wt, result := returnFixture(t, "author", "")
			submitReturn(t, x, result)
			x.f.m.Stop(x.me.agent.ID)
			lead := x.f.start("alice/lead")
			post := func(edit map[string]any) {
				t.Helper()
				edit["id"] = "t1"
				res := managementPost(t, x, lead.Token, "/v1/board", map[string]any{"update": []any{edit}})
				if res.Code != 200 {
					t.Fatal(res.Body)
				}
			}
			s := x.f.srv
			request := removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID, Completion: x.cf.completion("done")}
			if strings.Contains(mode, "handoff") {
				request.Handoff = "delegate"
				if err := x.f.store.AddWorker(x.y.Name, yesmen.Worker{Name: request.Handoff, StartedAt: time.Now().UTC(), Title: "Cleanup"}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "already-kept" {
				post(map[string]any{"worktrees": []any{}})
			}
			read := s.ReadPullMerge
			s.ReadPullMerge = func(ctx context.Context, repo string, n int) (github.PullMerge, error) {
				switch mode {
				case "already-kept":
					t.Fatal("remote check for a checkout the Board keeps")
				case "note-only":
					post(map[string]any{"note": "Moved the external evidence pointer"})
				case "reopen-keep":
					post(map[string]any{"status": "doing"})
					post(map[string]any{"status": "done", "worktrees": []any{}})
				case "keep", "keep-handoff":
					post(map[string]any{"worktrees": []any{}})
				}
				return read(ctx, repo, n)
			}
			res := managementPost(t, x, lead.Token, "/v1/worktrees/remove", request)
			var out cleanupResult
			if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &out) != nil {
				t.Fatalf("remove: %d %s", res.Code, res.Body)
			}
			rec := recordOf(t, x, wt.Dir)
			switch mode {
			case "note-only":
				if !out.Removed || rec.BoardRound.IsZero() || rec.Completion.ReturnID == "" {
					t.Fatalf("current return did not remove: %+v", out)
				}
			case "handoff-then-reopen":
				if out.Removed || rec.BoardRound.IsZero() || rec.CleanupOwner != request.Handoff {
					t.Fatalf("missing current Board handoff: %+v", out)
				}
				post(map[string]any{"status": "doing"})
				if retry := savedRetry(t, x, lead, wt, ""); retry.Removed || !strings.Contains(retry.Reason, "no longer current") {
					t.Fatalf("used the old handoff after reopen: %+v", retry)
				}
				assertRetained(t, x, wt)
			default:
				if out.Removed || rec.Completion != nil || rec.CleanupOwner != "" {
					t.Fatalf("stale proposal changed the registry: %+v", out)
				}
				assertRetained(t, x, wt)
			}
		})
	}
}
