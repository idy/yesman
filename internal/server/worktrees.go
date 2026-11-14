package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

var exactHeadRE = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func (s *Server) worktreeLock(workspace string) *sync.Mutex {
	s.worktreeMu.Lock()
	defer s.worktreeMu.Unlock()
	if s.worktreeLocks == nil {
		s.worktreeLocks = map[string]*sync.Mutex{}
	}
	if s.worktreeLocks[workspace] == nil {
		s.worktreeLocks[workspace] = &sync.Mutex{}
	}
	return s.worktreeLocks[workspace]
}

func latestWorktree(records []yesmen.WorktreeRecord, dir string) int {
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Dir == dir {
			return i
		}
	}
	return -1
}

func (s *Server) checkoutOwned(ctx context.Context, c caller, y yesmen.Yesman, req CheckoutRequest) (pool.Worktree, error) {
	purpose := req.Purpose
	if purpose == "" {
		purpose = "work"
	}
	switch purpose {
	case "work", "primary":
	case "review", "author", "release":
		if c.agent.Since.IsZero() {
			return pool.Worktree{}, errors.New("temporary checkouts need a known owner identity")
		}
		if !dirRE.MatchString(req.Dir) {
			return pool.Worktree{}, errors.New("temporary checkouts need an explicit dir")
		}
		if purpose == "review" && (req.Ref == "" || req.Branch != "") {
			return pool.Worktree{}, errors.New("review checkouts must be detached and read-only")
		}
		if purpose != "review" && req.Ref != "" {
			return pool.Worktree{}, errors.New("author/release checkouts need a branch")
		}
		b, err := s.Yesmen.Board(y.Name)
		if err != nil {
			return pool.Worktree{}, err
		}
		found := false
		for _, t := range b.Tasks {
			found = found || (t.ID == req.Task && t.Owner == c.agent.Name && t.Open())
		}
		if !found {
			return pool.Worktree{}, errors.New("temporary checkouts need an open board task owned by the caller")
		}
	default:
		return pool.Worktree{}, errors.New("purpose must be review, author, release or primary")
	}
	records, err := s.Yesmen.WorktreeRecords(y.Name)
	if err != nil {
		return pool.Worktree{}, err
	}
	if i := latestWorktree(records, req.Dir); i >= 0 && !records[i].Removed {
		// A successful remove followed by a crash before its final metadata
		// write is recoverable too. Never adopt a missing unfinished checkout.
		old := &records[i]
		if old.Completion == nil || old.Owner != c.agent.Name || !old.OwnerSince.Equal(c.agent.Since) || req.Ref != old.Completion.Head || purpose != "review" {
			return pool.Worktree{}, errors.New("checkout already has an unfinished lifecycle record; retain it or finish cleanup first")
		}
		if _, err := os.Lstat(filepath.Join(y.Path, old.Dir)); !errors.Is(err, os.ErrNotExist) {
			return pool.Worktree{}, errors.New("recorded checkout still exists")
		}
		old.Removed = true
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return pool.Worktree{}, err
	}
	wt, err := Checkout(ctx, s.Pool, y, req)
	if err != nil {
		return wt, err
	}
	wt.CleanupID = hex.EncodeToString(id[:])
	records = append(records, yesmen.WorktreeRecord{ID: wt.CleanupID, Dir: wt.Dir, Repo: wt.Repo, Owner: c.agent.Name, OwnerSince: c.agent.Since, Purpose: purpose, Task: req.Task, Branch: wt.Branch, Head: wt.Head})
	if err := s.Yesmen.SaveWorktreeRecords(y.Name, records); err != nil {
		// Do not delete the checkout on a failed metadata write. Unknown
		// resources are protected and can be reported for manual recovery.
		return wt, fmt.Errorf("checkout exists but lifecycle registration failed; retain it: %w", err)
	}
	return wt, nil
}

type removeWorktreeRequest struct {
	Checkout   string                     `json:"checkout"`
	Dir        string                     `json:"dir"`
	Force      bool                       `json:"force,omitempty"` // reject old callers requesting force
	Completion *yesmen.WorktreeCompletion `json:"completion,omitempty"`
	Handoff    string                     `json:"handoff,omitempty"` // lead-only cleanup delegation
}

type cleanupResult struct {
	Removed bool                   `json:"removed"`
	Reason  string                 `json:"reason,omitempty"`
	Record  *yesmen.WorktreeRecord `json:"record,omitempty"`
}

func (s *Server) cleanupWorktree(ctx context.Context, c caller, y yesmen.Yesman, req removeWorktreeRequest) (cleanupResult, error) {
	if req.Force {
		return cleanupResult{}, errors.New("agent cleanup never uses force")
	}
	records, err := s.Yesmen.WorktreeRecords(y.Name)
	if err != nil {
		return cleanupResult{}, err
	}
	i := latestWorktree(records, req.Dir)
	if i < 0 {
		return cleanupResult{Reason: "retain: unknown ownership; no agent lifecycle record"}, nil
	}
	rec := &records[i]
	if req.Checkout == "" || req.Checkout != rec.ID {
		return cleanupResult{Reason: "retain: checkout ID changed or missing; old cleanup cannot affect a new round"}, nil
	}
	out := cleanupResult{Record: rec}
	retain := func(reason string) (cleanupResult, error) { out.Reason = "retain: " + reason; return out, nil }
	owner := rec.Owner == c.agent.Name && rec.OwnerSince.Equal(c.agent.Since)
	lead := c.agent.Role == agent.RoleLead
	delegate := rec.CleanupOwner == c.agent.Name && rec.CleanupSince.Equal(c.agent.Since)
	if !owner && !lead && !delegate {
		return retain("belongs to another agent; ask the lead for an explicit handoff")
	}
	if rec.Purpose != "review" && rec.Purpose != "author" && rec.Purpose != "release" {
		return retain("primary or unknown purpose")
	}
	if req.Completion != nil {
		if !owner && !lead {
			return retain("only the owner or lead may certify completion")
		}
		v := *req.Completion
		if !exactHeadRE.MatchString(v.Head) || strings.TrimSpace(v.Ref) == "" || strings.TrimSpace(v.Evidence) == "" || len(v.Ref) > 2048 || len(v.Evidence) > 16000 || !v.Unused {
			return retain("completion needs exact HEAD, PR/ref, durable evidence and unused=true")
		}
		valid := v.Outcome == "done" && rec.Purpose != "review"
		if rec.Purpose == "review" {
			valid = v.Outcome == "pass" || v.Outcome == "findings-handed-off" || v.Outcome == "waiting-release" || v.Outcome == "audit-complete"
		}
		if !valid || rec.Purpose == "review" && v.Head != rec.Head {
			return retain("outcome or reviewed HEAD does not match this checkout")
		}
		if rec.Completion != nil && (rec.Completion.Head != v.Head || rec.Completion.Outcome != v.Outcome || rec.Completion.Ref != v.Ref || rec.Completion.Evidence != v.Evidence) {
			return retain("completion already saved; reconstruct a new checkout for another round")
		}
		if rec.Completion == nil {
			v.SavedAt = time.Now().UTC()
			rec.Completion = &v
			if err := s.Yesmen.SaveWorktreeRecords(y.Name, records); err != nil {
				return out, err
			}
		}
	}
	if rec.Completion == nil {
		return retain("no saved completion evidence; stopped/restarted is not completed")
	}
	if req.Handoff != "" {
		if !lead {
			return retain("only the lead delegates cleanup")
		}
		target, err := s.Agents.Get(agent.ID(y.Name, req.Handoff))
		if err != nil || target.Role != agent.RoleWorker {
			return retain("handoff needs a worker in this workspace")
		}
		err = s.withWorktreeUsers(y.Name, *rec, c.agent.ID, func() error {
			rec.CleanupOwner, rec.CleanupSince = target.Name, target.Since
			return s.Yesmen.SaveWorktreeRecords(y.Name, records)
		})
		if err != nil {
			return retain(err.Error())
		}
		out.Reason = "cleanup handed off to " + target.Name
		return out, nil
	}
	path := filepath.Join(y.Path, rec.Dir)
	if rec.Removed {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return retain("removed directory was reused; do not remove its new contents")
		}
		out.Removed = true
		return out, nil
	}
	// Fresh merge evidence is required for branches, including squash merges.
	// Release tasks stay open through publication/verification, after PR merge.
	if rec.Purpose != "review" {
		b, err := s.Yesmen.Board(y.Name)
		if err != nil {
			return out, err
		}
		finished := false
		for _, t := range b.Tasks {
			terminal := !t.Open()
			if rec.Purpose == "release" {
				terminal = t.Status == yesmen.TaskDone
			}
			finished = finished || t.ID == rec.Task && t.Owner == rec.Owner && terminal
		}
		if !finished {
			return retain("associated author/release task is unfinished")
		}
		if err := s.verifyMerged(ctx, *rec); err != nil {
			return retain(err.Error())
		}
	}
	// Refuse a reused worker name. A removed worker's durable records still
	// exist, but only the lead can take responsibility for that evidence.
	if info, err := s.Agents.Get(agent.ID(y.Name, rec.Owner)); err == nil {
		if !info.Since.Equal(rec.OwnerSince) {
			return retain("owner identity changed")
		}
	} else if !lead && !delegate {
		return retain("owner cannot be verified")
	}
	err = s.withWorktreeUsers(y.Name, *rec, c.agent.ID, func() error {
		// Idempotent recovery after git removed the tree but the app exited
		// before the final metadata save. A replacement path is never touched.
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			registered, err := s.Pool.HasWorktreeRegistration(ctx, rec.Repo, path)
			if err != nil {
				return err
			}
			if registered {
				return errors.New("missing path is still registered; retain for recovery")
			}
		} else {
			if err := s.Pool.RemoveTemporaryWorktree(ctx, y.Path, pool.Worktree{Repo: rec.Repo, Dir: rec.Dir, Head: rec.Completion.Head, Branch: rec.Branch, Detached: rec.Purpose == "review", CleanupID: rec.ID}); err != nil {
				return err
			}
		}
		rec.Removed = true
		return s.Yesmen.SaveWorktreeRecords(y.Name, records)
	})
	if err != nil {
		return retain(err.Error())
	}
	out.Removed = true
	return out, nil
}

// Reserve every recorded user, including a prior cleanup delegate, so a lead
// cannot reassign/delete a resource while that delegate is still using it.
func (s *Server) withWorktreeUsers(workspace string, rec yesmen.WorktreeRecord, callerID string, fn func() error) error {
	return s.Agents.WithWorktreeOwner(agent.ID(workspace, rec.Owner), callerID, func() error {
		if rec.CleanupOwner == "" || rec.CleanupOwner == rec.Owner {
			return fn()
		}
		return s.Agents.WithWorktreeOwner(agent.ID(workspace, rec.CleanupOwner), callerID, fn)
	})
}

func (s *Server) verifyMerged(ctx context.Context, rec yesmen.WorktreeRecord) error {
	u, err := url.Parse(rec.Completion.Ref)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" {
		return errors.New("branch completion needs a GitHub PR URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return errors.New("branch completion needs a GitHub PR URL")
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n < 1 {
		return errors.New("invalid PR number")
	}
	target := parts[0] + "/" + parts[1]
	r, err := s.Pool.Get(rec.Repo)
	if err != nil {
		return err
	}
	upstream, _ := pool.NameFromURL(r.Upstream)
	if !strings.EqualFold("github.com/"+target, rec.Repo) && !strings.EqualFold("github.com/"+target, upstream) {
		return errors.New("PR is outside the checkout's repository/upstream")
	}
	read := s.ReadPullMerge
	if read == nil {
		read = github.ReadPullMerge
	}
	pr, err := read(ctx, target, n)
	if err != nil {
		return fmt.Errorf("cannot verify merged PR: %w", err)
	}
	if !pr.Merged || pr.State != "closed" || pr.MergedAt.IsZero() || !exactHeadRE.MatchString(pr.MergeCommit) || pr.Head.SHA != rec.Completion.Head || pr.Head.Ref != rec.Branch || !strings.EqualFold("github.com/"+pr.Head.Repo.FullName, rec.Repo) {
		return errors.New("PR is not merged at this exact source repository/branch/HEAD")
	}
	return nil
}
