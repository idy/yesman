package server

import (
	"slices"
	"strings"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

const (
	unprovenCreation = "Yes Man could not verify which task created this checkout. Review it individually."
	canceledTask     = "The creating task was dropped. Review this checkout individually."
	unfinishedTask   = "The creating task is unfinished."
	missingTask      = "The checkout has no matching task on the board."
)

func boundCreatingTask(y yesmen.Yesman, rec yesmen.WorktreeRecord, t yesmen.Task) bool {
	p := rec.CreatedFor
	return p != nil && p.Workspace == y.Name && !y.HiredAt.IsZero() && p.HiredAt.Equal(y.HiredAt) &&
		t.ID == rec.Task && !t.CreatedAt.IsZero() && p.TaskAt.Equal(t.CreatedAt)
}

// The task's creation time and its non-reusable Board ID identify the task
// across assignment changes. WorktreeGeneration identifies the current
// authorization decision, which must survive the round trip to GitHub.
func creationMatches(y yesmen.Yesman, rec yesmen.WorktreeRecord, t yesmen.Task, workers []yesmen.Worker) bool {
	if rec.Root == nil || rec.Root.Ino == 0 || rec.Root.Birth == 0 || t.CreatedAt.IsZero() || y.HiredAt.IsZero() {
		return false
	}
	if rec.CreatedFor != nil {
		return boundCreatingTask(y, rec, t)
	}
	// Before the explicit binding, only authenticated checkout wrote this
	// temporary-purpose/owner/task tuple and creation-time root. Require
	// the original scoped owner generation to match the Board assignment
	// and the task and hire to already have existed when the root was born.
	// Never infer a legacy association across a handoff or backfill a record.
	birth := time.Unix(0, rec.Root.Birth)
	ownerBound := rec.OwnerSince.Equal(t.OwnerSince) || rec.Owner == yesmen.LeadName && rec.OwnerSince.Equal(y.HiredAt)
	return t.Owner == rec.Owner && ownerBound && currentWorktreeUser(y, workers, rec.Owner, rec.OwnerSince) &&
		!birth.Before(t.CreatedAt) && !birth.Before(y.HiredAt) && !birth.Before(rec.OwnerSince)
}

// checkoutAuthority is shared by automatic discovery and the desktop's bulk
// preview and confirmation. Explicit returns keep their stricter API and
// evidence checks. The ordinary task path observes Git's actual HEAD and never
// synthesizes a return, a merge, or an unused attestation.
type checkoutAuthority struct {
	decision   WorktreeTaskDecision
	completion *yesmen.WorktreeCompletion
	fromBoard  bool
}

func bulkAuthority(y yesmen.Yesman, rec yesmen.WorktreeRecord, b yesmen.Board, workers []yesmen.Worker) (checkoutAuthority, string) {
	d := WorktreeDecisionFor(b, rec)
	a := checkoutAuthority{decision: d}
	if d.status == "" {
		return a, missingTask
	}
	if d.KeepsCheckout() {
		return a, boardKeep
	}
	if d.status == yesmen.TaskDropped {
		return a, canceledTask
	}
	if d.paused {
		return a, "The creating task is paused."
	}
	if d.status != yesmen.TaskDone || d.round.IsZero() {
		return a, unfinishedTask
	}
	if !currentWorktreeUser(y, workers, rec.Owner, rec.OwnerSince) || rec.CleanupOwner != "" && !currentWorktreeUser(y, workers, rec.CleanupOwner, rec.CleanupSince) {
		return a, "The recorded owner can no longer be verified."
	}

	if d.explicit {
		t, ok := returnedTask(y, rec, b, workers)
		if !ok {
			return a, staleReturn
		}
		v := boardProof(y, rec, t)
		if v == nil || rec.Completion != nil && !sameReturnedSource(*rec.Completion, *v) {
			return a, staleReturn
		}
		a.completion, a.fromBoard = v, true
	} else if rec.Completion != nil && rec.BoardRound.IsZero() && !rec.Completion.SavedAt.Before(d.round) {
		// Existing accepted standalone completions remain retryable with their
		// original proof, including those predating the creation binding.
		if _, ok := returnedTask(y, rec, b, workers); !ok {
			return a, staleReturn
		}
		a.completion = rec.Completion
	}
	if a.completion != nil {
		if completionProblem(rec, *a.completion) != "" {
			return a, "The saved completion does not cover this checkout and commit."
		}
		if rec.Purpose == "release" && strings.TrimSpace(a.completion.Publication) == "" {
			return a, "The release return has no record of completed publication."
		}
	}
	if rec.CreatedFor != nil || a.completion == nil {
		i := slices.IndexFunc(b.Tasks, func(t yesmen.Task) bool { return t.ID == rec.Task })
		if i < 0 || !creationMatches(y, rec, b.Tasks[i], workers) {
			return a, unprovenCreation
		}
	}
	return a, ""
}

func (s *Server) withTaskWorktreeUsers(y yesmen.Yesman, rec yesmen.WorktreeRecord, d WorktreeTaskDecision, fn func() error) error {
	return s.withWorktreeUsers(y.Name, rec, "", func() error {
		// A reassignment does not change the creator. The current assignee
		// can still be using this resource while delivering the same task.
		if d.owner != "" && d.owner != rec.Owner && d.owner != rec.CleanupOwner {
			return s.Agents.WithWorktreeOwner(agent.ID(y.Name, d.owner), "", fn)
		}
		return fn()
	})
}
