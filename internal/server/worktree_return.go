package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

func temporaryPurpose(p string) bool { return p == "review" || p == "author" || p == "release" }

func completionProblem(rec yesmen.WorktreeRecord, v yesmen.WorktreeCompletion) string {
	if !exactHeadRE.MatchString(v.Head) || strings.TrimSpace(v.Ref) == "" || strings.TrimSpace(v.Evidence) == "" || len(v.Ref) > 2048 || len(v.Evidence) > 16000 || !v.Unused {
		return "completion needs exact HEAD, PR/ref, durable evidence and unused=true"
	}
	if strings.TrimSpace(v.SourceBranch) != v.SourceBranch || len(v.SourceBranch) > 255 || len(v.Publication) > 2048 {
		return "invalid published source branch or publication receipt"
	}
	if rec.Purpose == "review" {
		if v.Head != rec.Head || v.SourceBranch != "" || !slices.Contains([]string{"pass", "findings-handed-off", "waiting-release", "audit-complete"}, v.Outcome) {
			return "outcome or reviewed HEAD does not match this checkout"
		}
	} else if !temporaryPurpose(rec.Purpose) || v.Outcome != "done" {
		return "outcome or purpose does not match this checkout"
	}
	return ""
}

func publishedBranch(rec yesmen.WorktreeRecord) string {
	if rec.Completion.SourceBranch != "" {
		return rec.Completion.SourceBranch
	}
	return rec.Branch
}

// Called with the workspace worktree lock before the Board commit. The task
// update and its named checkout returns are one durable write. Taking the
// evidence from this update's own note keeps later note edits independent.
func (s *Server) prepareReturns(c caller, change *yesmen.BoardChange, requireDisposition bool) error {
	records, board, _, err := s.Yesmen.WorktreeState(c.yesman)
	if err != nil {
		// A metadata read error cannot authorize a return. It need not
		// prevent finishing unrelated work or explicitly keeping the trees.
		for _, e := range change.Update {
			if e.Worktrees != nil && len(*e.Worktrees) > 0 {
				return err
			}
		}
		return nil
	}
	byTask := map[string]map[string]yesmen.WorktreeRecord{}
	seen := map[string]bool{}
	for _, rec := range slices.Backward(records) {
		if !seen[rec.Dir] && !rec.Removed && temporaryPurpose(rec.Purpose) && (c.agent.Role == agent.RoleLead || rec.Owner == c.agent.Name && rec.OwnerSince.Equal(c.agent.Since)) {
			if byTask[rec.Task] == nil {
				byTask[rec.Task] = map[string]yesmen.WorktreeRecord{}
			}
			byTask[rec.Task][rec.ID] = rec
		}
		seen[rec.Dir] = true
	}
	for _, e := range change.Update {
		id := yesmen.TaskID(e.ID)
		i := slices.IndexFunc(board.Tasks, func(t yesmen.Task) bool { return t.ID == id })
		if i < 0 {
			if e.Worktrees != nil && len(*e.Worktrees) > 0 {
				return fmt.Errorf("no registered worktree result for task %s", id)
			}
			continue
		}
		t := board.Tasks[i]
		owned := byTask[id]
		if e.Worktrees == nil {
			if requireDisposition && t.Open() && e.Status != nil && strings.EqualFold(strings.TrimSpace(*e.Status), yesmen.TaskDone) {
				for _, rec := range owned {
					if rec.Owner == t.Owner && rec.Completion == nil && rec.Root != nil {
						return fmt.Errorf("task %s has temporary checkout %s (%s). Include its worktrees result with this update, or worktrees: [] to keep it", id, rec.Dir, rec.ID)
					}
				}
			}
			continue
		}
		if len(*e.Worktrees) > 32 || (c.agent.Role != agent.RoleLead && c.agent.Name != t.Owner) {
			return fmt.Errorf("only the task owner or lead may return its worktrees (up to 32)")
		}
		used := map[string]bool{}
		for j := range *e.Worktrees {
			v := &(*e.Worktrees)[j]
			rec, ok := owned[v.Checkout]
			if !ok || used[v.Checkout] || rec.Owner != t.Owner || !t.OwnerSince.IsZero() && !rec.OwnerSince.Equal(t.OwnerSince) {
				return fmt.Errorf("task %s: checkout or owner generation changed", id)
			}
			used[v.Checkout] = true
			if rec.Root == nil {
				return fmt.Errorf("%s has no recorded directory identity for automatic cleanup. Keep it with worktrees: [] or use the explicit management path after verification", rec.Dir)
			}
			if e.Note == nil || strings.TrimSpace(*e.Note) == "" {
				return errors.New("save the result and evidence outside the tree in this task update's note")
			}
			v.Evidence = fmt.Sprintf("Board %s: %s\nReconstruct %s from %s at %s with a new owned checkout.", id, strings.TrimSpace(*e.Note), rec.Dir, rec.Repo, v.Head)
			v.SavedAt = time.Now().UTC()
			v.CertifiedBy, v.CertifiedSince = c.agent.Name, c.agent.Since
			if reason := completionProblem(rec, v.WorktreeCompletion); reason != "" {
				return errors.New(reason)
			}
			if rec.Purpose == "release" && strings.TrimSpace(v.Publication) == "" {
				return errors.New("a release return needs the publisher's external publication and verification receipt")
			}
			if rec.Completion != nil && !sameWorktreeCompletion(*rec.Completion, v.WorktreeCompletion) {
				return errors.New("completion already saved for this checkout")
			}
		}
	}
	return nil
}

func (s *Server) removeCompletedInstance(ctx context.Context, y yesmen.Yesman, rec yesmen.WorktreeRecord) error {
	path := filepath.Join(y.Path, rec.Dir)
	wt := expectedWorktree(rec)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return s.Pool.RemoveTemporaryWorktree(ctx, y.Path, wt)
	}
	return s.checkMissingInstance(ctx, rec, path)
}

func (s *Server) checkMissingInstance(ctx context.Context, rec yesmen.WorktreeRecord, path string) error {
	registered, err := s.Pool.HasWorktreeRegistration(ctx, "", path)
	if err != nil {
		return err
	}
	if registered {
		return errors.New("missing path is still registered; retain for recovery")
	}
	return s.Pool.CheckWorktreeCheckpoint(ctx, expectedWorktree(rec))
}

// Check each repository in which this pool branch can target a PR. An alias
// requires checking both the local and explicitly published head ref.
func (s *Server) checkOpenPulls(ctx context.Context, rec yesmen.WorktreeRecord) error {
	r, err := s.Pool.RecordedRepo(ctx, rec.Repo)
	if err != nil {
		return err
	}
	targets := []string{strings.TrimPrefix(rec.Repo, "github.com/")}
	if r.Upstream != "" {
		name, err := pool.NameFromURL(r.Upstream)
		if err != nil || !strings.HasPrefix(name, "github.com/") {
			return errors.New("cannot identify GitHub upstream for this checkout")
		}
		name = strings.TrimPrefix(name, "github.com/")
		if !strings.EqualFold(name, targets[0]) {
			targets = append(targets, name)
		}
	}
	owner, _, _ := strings.Cut(targets[0], "/")
	read := s.HasOpenPull
	if read == nil {
		read = github.HasOpenPullFrom
	}
	for _, target := range targets {
		for _, branch := range slices.Compact([]string{rec.Branch, publishedBranch(rec)}) {
			open, err := read(ctx, target, owner, branch)
			if err != nil {
				return fmt.Errorf("cannot verify open PRs: %w", err)
			}
			if open {
				return fmt.Errorf("source branch %s still has an open PR in %s", branch, target)
			}
		}
	}
	return nil
}
