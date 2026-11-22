package server

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/safefs"
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

// worktreeOperation admits the original caller only after the workspace wait.
// BeginOperation checks the exact hire, agent and process under a short Use
// lease, then holds the process change gate until fn returns. Stop cancels its
// context before draining that gate; Archive does the same before moving the
// workspace. Do not hold or reacquire Use during fn: Archive may hold it while
// draining this operation. Owner reservations and repository/worktree locks
// follow admission; Manager.mu is never held across their waits or I/O.
func (s *Server) worktreeOperation(ctx context.Context, c caller, fn func(context.Context, yesmen.Yesman) error) error {
	l := s.worktreeLock(c.yesman.Name)
	l.Lock()
	defer l.Unlock()
	ctx, finish, err := s.Agents.BeginOperation(ctx, c.token, c.agent, c.yesman.HiredAt)
	if err != nil {
		return err
	}
	defer finish()
	return fn(ctx, c.yesman)
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
	var admitted *yesmen.Task
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
		for _, t := range b.Tasks {
			if t.ID == req.Task && t.Owner == c.agent.Name && t.Open() && !t.Paused() && !t.CreatedAt.IsZero() &&
				(t.OwnerSince.IsZero() || t.OwnerSince.Equal(c.agent.Since)) {
				admitted = &t
				break
			}
		}
		if admitted == nil {
			return pool.Worktree{}, errors.New("temporary checkouts need an open board task owned by the caller")
		}
	default:
		return pool.Worktree{}, errors.New("purpose must be review, author, release or primary")
	}
	repo, err := s.Pool.Get(req.Repo)
	if err != nil {
		return pool.Worktree{}, err
	}
	req.Dir, err = checkoutDir(repo, req)
	if err != nil {
		return pool.Worktree{}, err
	}
	records, err := s.Yesmen.WorktreeRecords(y.Name)
	if err != nil {
		return pool.Worktree{}, err
	}
	if i := latestWorktree(records, req.Dir); i >= 0 && !records[i].Removed {
		if err := s.retireMissingWorktree(ctx, c, y, records, i); err != nil {
			return pool.Worktree{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return pool.Worktree{}, err
	}
	wt, err := Checkout(ctx, s.Pool, y, req)
	if err != nil {
		return wt, err
	}
	rec := yesmen.WorktreeRecord{ID: wt.CleanupID, Dir: wt.Dir, Repo: wt.Repo, Owner: c.agent.Name, OwnerSince: c.agent.Since, Purpose: purpose, Task: req.Task, Branch: wt.Branch, Head: wt.Head}
	if wt.Root != nil {
		// Measured by the pool at creation; bound here with the instance
		// and the owner's generation (files.go checkInstance).
		rec.Root = &yesmen.WorktreeRoot{Ino: wt.Root.Ino, Birth: wt.Root.Birth}
	}
	if admitted != nil {
		rec.CreatedFor = &yesmen.WorktreeCreation{Workspace: y.Name, HiredAt: y.HiredAt, TaskAt: admitted.CreatedAt}
	}
	// Once Git succeeds, record the checkout even if cancellation just arrived.
	// The operation lease still protects this final local bookkeeping.
	records = append(records, rec)
	if err := s.Yesmen.SaveCheckout(y, records, admitted); err != nil {
		// Do not delete the checkout on a failed metadata write. Unknown
		// resources are protected and can be reported for manual recovery.
		return wt, fmt.Errorf("checkout exists but lifecycle registration failed; retain it: %w", err)
	}
	return wt, nil
}

// An explicit checkout by the same owner may retire a record for an instance
// already removed manually. This changes only history, not any directory or Git
// registration, and never treats a missing unfinished task as completed.
func (s *Server) retireMissingWorktree(ctx context.Context, c caller, y yesmen.Yesman, records []yesmen.WorktreeRecord, i int) error {
	rec := &records[i]
	if rec.Owner != c.agent.Name || !rec.OwnerSince.Equal(c.agent.Since) {
		return errors.New("recorded checkout belongs to another owner; retain it")
	}
	return s.withWorktreeUsers(y.Name, *rec, c.agent.ID, func() error {
		path := filepath.Join(y.Path, rec.Dir)
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return errors.New("recorded checkout still exists or cannot be inspected; retain it")
		}
		registered, err := s.Pool.HasWorktreeRegistration(ctx, "", path)
		if err != nil {
			return err
		}
		if registered {
			return errors.New("missing checkout still has a Git registration; retain it")
		}
		if checkoutID.MatchString(rec.ID) {
			moved, err := s.Pool.HasWorktreeInstance(ctx, rec.Repo, rec.ID)
			if err != nil {
				return err
			}
			if moved {
				return errors.New("checkout is still registered at another path")
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rec.Removed = true
		rec.RetiredReason = "directory and Git registration already absent; explicit owner checkout starts a new instance"
		return s.Yesmen.SaveWorktreeRecords(y.Name, records)
	})
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

func (s *Server) finishRemoval(ctx context.Context, c caller, y yesmen.Yesman, req removeWorktreeRequest, proof removalVerification, records []yesmen.WorktreeRecord, i int, board yesmen.Board, workers []yesmen.Worker) (cleanupResult, error) {
	if err := ctx.Err(); err != nil {
		return cleanupResult{}, err
	}
	rec := &records[i]
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
	recoveryOnly := proof.missing && rec.Completion != nil && req.Handoff == ""
	if !rec.Removed && !recoveryOnly {
		if err := CheckSavedWorktreeReturn(y, *rec, board, workers); err != nil {
			return retain(err.Error())
		}
		// A first manual proposal has no saved certificate yet. The task can
		// be reopened, closed again or explicitly kept during a remote check.
		if !reflect.DeepEqual(proof.decision, WorktreeDecisionFor(board, *rec)) {
			return retain("the associated task changed during verification. Check its current disposition before trying again")
		}
		if !rec.BoardRound.IsZero() && heldByUsers(*rec, board, workers) {
			return retain("recorded user has open or paused work")
		}
	}
	reservationCaller := c.agent.ID
	if !rec.BoardRound.IsZero() {
		reservationCaller = ""
	}
	branchVerified := false
	if req.Completion != nil {
		if !owner && !lead {
			return retain("only the owner or lead may certify completion")
		}
		v := *req.Completion
		if reason := completionProblem(*rec, v); reason != "" {
			return retain(reason)
		}
		if !matchesSavedProposal(*rec, v) {
			return retain("completion already saved; reconstruct a new checkout for another round")
		}
		if rec.Completion == nil {
			v.SavedAt = time.Now().UTC()
			v.ReturnID = ""
			v.CertifiedBy, v.CertifiedSince = c.agent.Name, c.agent.Since
			candidate, err := proof.decision.bind(*rec, v)
			if err != nil {
				return retain(err.Error())
			}
			if err := CheckSavedWorktreeReturn(y, candidate, board, workers); err != nil {
				return retain(err.Error())
			}
			if !candidate.BoardRound.IsZero() && heldByUsers(candidate, board, workers) {
				return retain("recorded user has open or paused work")
			}
			if rec.Purpose != "review" {
				err := proof.branchProof(board, candidate)
				if err == nil {
					err = s.Pool.CheckTemporaryWorktree(ctx, y.Path, expectedWorktree(candidate))
				}
				if ctxErr := ctx.Err(); ctxErr != nil {
					return out, ctxErr
				}
				if err != nil {
					if len(rec.Attempts) == 0 || !sameWorktreeCompletion(rec.Attempts[len(rec.Attempts)-1].Completion, v) || rec.Attempts[len(rec.Attempts)-1].Reason != err.Error() {
						rec.Attempts = append(rec.Attempts, yesmen.WorktreeAttempt{Completion: v, Reason: err.Error()})
						if saveErr := s.Yesmen.SaveWorktreeRecords(y.Name, records); saveErr != nil {
							return out, saveErr
						}
					}
					return retain(err.Error())
				}
				branchVerified = true
			}
			if err := ctx.Err(); err != nil {
				return out, err
			}
			rec.Completion, rec.BoardRound = candidate.Completion, candidate.BoardRound
			if !rec.BoardRound.IsZero() {
				reservationCaller = ""
			}
			if err := s.Yesmen.SaveWorktreeRecords(y.Name, records); err != nil {
				return out, err
			}
		}
	}
	if rec.Completion == nil {
		return retain("no saved completion evidence; stopped/restarted is not completed")
	}
	if !rec.Removed && rec.Purpose != "review" && !branchVerified && !recoveryOnly {
		if err := proof.branchProof(board, *rec); err != nil {
			return retain(err.Error())
		}
	}
	if req.Handoff != "" {
		if !lead {
			return retain("only the lead delegates cleanup")
		}
		target, err := s.Agents.Get(agent.ID(y.Name, req.Handoff))
		if err != nil || target.Role != agent.RoleWorker {
			return retain("handoff needs a worker in this workspace")
		}
		err = s.withWorktreeUsers(y.Name, *rec, reservationCaller, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
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
	// Refuse a reused worker name. A removed worker's durable records still
	// exist, but only the lead can take responsibility for that evidence.
	if info, err := s.Agents.Get(agent.ID(y.Name, rec.Owner)); err == nil {
		if !info.Since.Equal(rec.OwnerSince) {
			return retain("owner identity changed")
		}
	} else if !lead && !delegate {
		return retain("owner cannot be verified")
	}
	err := s.withWorktreeUsers(y.Name, *rec, reservationCaller, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if recoveryOnly {
			if err := s.checkMissingInstance(ctx, *rec, path); err != nil {
				return err
			}
		} else if err := s.removeCompletedInstance(ctx, y, *rec); err != nil {
			return err
		}
		// Successful removal must be recorded even if cancellation just arrived.
		// The process gate still keeps Stop/Archive from finishing before save.
		err := s.recordWorktreeRemoval(y, *rec, rec.RetiredReason)
		if err == nil {
			rec.Removed = true
		}
		return err
	})
	if err != nil {
		return retain(err.Error())
	}
	out.Removed = true
	return out, nil
}

// recordWorktreeRemoval is the single post-Git write under TryWorktreeCommit.
// It reloads and compares the original record, preserving unrelated checkouts.
func (s *Server) recordWorktreeRemoval(y yesmen.Yesman, before yesmen.WorktreeRecord, reason string) error {
	after := before
	after.Removed, after.RetiredReason = true, reason
	return s.Yesmen.CommitWorktreeRecord(y, before, after)
}

// A Board certification keeps the task's evidence. Retrying the owner's
// original request can therefore supply a different external evidence note.
func matchesSavedProposal(rec yesmen.WorktreeRecord, v yesmen.WorktreeCompletion) bool {
	if rec.Completion == nil || sameWorktreeCompletion(*rec.Completion, v) {
		return true
	}
	return !rec.BoardRound.IsZero() && sameReturnedSource(*rec.Completion, v) && rec.Completion.Outcome == v.Outcome && rec.Completion.Unused == v.Unused
}

func sameWorktreeCompletion(a, b yesmen.WorktreeCompletion) bool {
	return a.Head == b.Head && a.Outcome == b.Outcome && a.Ref == b.Ref && a.Evidence == b.Evidence && a.Unused == b.Unused && a.SourceBranch == b.SourceBranch && a.Publication == b.Publication
}

func expectedWorktree(rec yesmen.WorktreeRecord) pool.Worktree {
	head := rec.Head
	if rec.Completion != nil {
		head = rec.Completion.Head
	}
	return pool.Worktree{Repo: rec.Repo, Dir: rec.Dir, Head: head, Branch: rec.Branch, Detached: rec.Purpose == "review", CleanupID: rec.ID, Root: recordedWorktreeRoot(rec)}
}

func recordedWorktreeRoot(rec yesmen.WorktreeRecord) *safefs.Identity {
	if rec.Root != nil {
		return &safefs.Identity{Ino: rec.Root.Ino, Birth: rec.Root.Birth, HasBirth: true}
	}
	return nil
}

func (s *Server) worktreeUsersPaused(y yesmen.Yesman, rec yesmen.WorktreeRecord) bool {
	return s.Agents.WorktreeUserPaused(y, rec.Owner, rec.OwnerSince) ||
		rec.CleanupOwner != "" && s.Agents.WorktreeUserPaused(y, rec.CleanupOwner, rec.CleanupSince)
}

func branchTaskFinished(b yesmen.Board, rec yesmen.WorktreeRecord) error {
	finished := false
	for _, t := range b.Tasks {
		terminal := !t.Open()
		if rec.Purpose == "release" {
			terminal = t.Status == yesmen.TaskDone
		}
		finished = finished || t.ID == rec.Task && t.Owner == rec.Owner && (t.OwnerSince.IsZero() || rec.OwnerSince.Equal(t.OwnerSince)) && terminal
	}
	if !finished {
		return errors.New("associated author/release task is unfinished")
	}
	return nil
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
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
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
	r, err := s.Pool.RecordedRepo(ctx, rec.Repo)
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
	if !pr.Merged || pr.State != "closed" || pr.MergedAt.IsZero() || !exactHeadRE.MatchString(pr.MergeCommit) || pr.Head.SHA != rec.Completion.Head || pr.Head.Ref != publishedBranch(rec) || !strings.EqualFold("github.com/"+pr.Head.Repo.FullName, rec.Repo) {
		return errors.New("PR is not merged at this exact source repository/branch/HEAD")
	}
	if rec.Purpose == "release" && rec.Completion.Publication != "" {
		base := "https://github.com/" + target + "/releases/tag/"
		tag, ok := strings.CutPrefix(rec.Completion.Publication, base)
		if !ok || tag == "" || strings.ContainsAny(tag, "?#%") {
			return errors.New("publication needs the published GitHub release URL in the PR's repository")
		}
		readTag := s.ReadPublishedTag
		if readTag == nil {
			readTag = github.ReadPublishedTag
		}
		head, err := readTag(ctx, target, tag)
		if err != nil || head != pr.MergeCommit && head != pr.Head.SHA {
			return errors.New("cannot verify publication of this release PR at its tag")
		}
	}
	return nil
}
