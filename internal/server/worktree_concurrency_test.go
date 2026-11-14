package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConcurrentCleanupUsesOneCompletionAndCheckpoint(t *testing.T) {
	f := newCleanupFixture(t)
	wt := f.checkout(t, "review", "review")
	// An owner reserved by another lifecycle operation cannot be removed.
	err := f.s.Agents.WithWorktreeOwner(f.owner.agent.ID, f.delegate.agent.ID, func() error {
		out := f.remove(t, f.lead, removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID, Completion: f.completion("pass")})
		if out.Removed || !strings.Contains(out.Reason, "in progress") {
			return fmt.Errorf("reserved owner: %+v", out)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out := f.remove(t, f.lead, removeWorktreeRequest{Dir: wt.Dir, Handoff: "cleanup"}); out.Removed || !strings.Contains(out.Reason, "handed off") {
		t.Fatalf("handoff: %+v", out)
	}
	err = f.s.Agents.WithWorktreeOwner(f.delegate.agent.ID, f.lead.agent.ID, func() error {
		if out := f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir}); out.Removed || !strings.Contains(out.Reason, "in progress") {
			return fmt.Errorf("active delegate: %+v", out)
		}
		if out := f.remove(t, f.lead, removeWorktreeRequest{Dir: wt.Dir, Handoff: "owner"}); out.Removed || !strings.Contains(out.Reason, "in progress") {
			return fmt.Errorf("reassigned active delegate: %+v", out)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID})
	results := make(chan error, 8)
	for range 8 {
		go func() {
			out, err := f.s.removeWorktree(context.Background(), f.lead, httptest.NewRequest("POST", "/v1/worktrees/remove", bytes.NewReader(body)))
			if err == nil && !out.(cleanupResult).Removed {
				err = fmt.Errorf("cleanup: %+v", out)
			}
			results <- err
		}()
	}
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	records, err := f.s.Yesmen.WorktreeRecords(f.y.Name)
	if err != nil || len(records) != 1 || !records[0].Removed || records[0].Completion == nil {
		t.Fatalf("records: %+v %v", records, err)
	}
	if got := cleanupGit(t, f.repo.Path, "for-each-ref", "--format=%(refname)", "refs/yesman/worktrees/"); got != "refs/yesman/worktrees/"+wt.CleanupID {
		t.Fatalf("duplicate checkpoints: %s", got)
	}
}
