package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// The remote check is bound to the exact record read and the completion in
// the request. It runs under a cancellable caller lease, outside the locks
// for local work. A new caller admission covers the subsequent local commit.
type removalVerification struct {
	record        *yesmen.WorktreeRecord
	decision      WorktreeTaskDecision
	missing       bool
	branchChecked bool
	branchErr     error
}

// WorktreeTaskDecision captures the authorization-relevant task state for one
// checkout. Compare snapshots before and after remote verification.
// A note edit, Board reorder or change to another task leaves it equal.
// Its fields come from the same durable Board snapshot as the registry read.
// The zero value describes a task with no recorded disposition.
type WorktreeTaskDecision struct {
	status, owner, doneOwner   string
	generation                 string
	created, ownerSince, round time.Time
	paused, explicit, kept     bool
	returned                   yesmen.WorktreeCompletion
	sourceBranches             []string // protection hints only
}

// KeepsCheckout reports an explicit Board choice to retain this instance.
func (d WorktreeTaskDecision) KeepsCheckout() bool {
	return d.kept || d.explicit && d.returned.Head == ""
}

// WorktreeDecisionFor returns the decision for this instance in b.
func WorktreeDecisionFor(b yesmen.Board, rec yesmen.WorktreeRecord) WorktreeTaskDecision {
	for _, task := range b.Tasks {
		if task.ID != rec.Task {
			continue
		}
		d := WorktreeTaskDecision{status: task.Status, owner: task.Owner, doneOwner: task.DoneOwner, generation: task.WorktreeGeneration,
			created: task.CreatedAt, ownerSince: task.OwnerSince, round: task.DoneAt,
			paused: task.Paused(), explicit: task.Returns != nil,
			kept:           task.KeepWorktreesExcept != nil && !slices.Contains(task.KeepWorktreesExcept, rec.ID),
			sourceBranches: task.WorktreeSourceBranches[rec.ID]}
		for _, ret := range task.Returns {
			if ret.Checkout == rec.ID {
				d.returned = ret.WorktreeCompletion
				break
			}
		}
		return d
	}
	return WorktreeTaskDecision{}
}

// A proposal made while the Board has explicitly disposed of this instance
// uses that return and its authority through later retries and delegation.
func (d WorktreeTaskDecision) bind(rec yesmen.WorktreeRecord, proposed yesmen.WorktreeCompletion) (yesmen.WorktreeRecord, error) {
	if d.explicit {
		if d.KeepsCheckout() || !sameReturnedSource(proposed, d.returned) || proposed.Outcome != d.returned.Outcome {
			return rec, errors.New("the manual proposal does not match the current Board disposition")
		}
		rec.BoardRound, proposed = d.round, d.returned
	}
	rec.Completion = &proposed
	return rec, nil
}

func (s *Server) verifyRemoval(ctx context.Context, c caller, y yesmen.Yesman, req removeWorktreeRequest) (removalVerification, error) {
	var proof removalVerification
	if req.Force {
		return proof, errors.New("agent cleanup never uses force")
	}
	records, b, users, err := s.Yesmen.WorktreeState(y)
	if err != nil {
		return proof, err
	}
	i := latestWorktree(records, req.Dir)
	if i < 0 || records[i].ID != req.Checkout || req.Checkout == "" {
		return proof, nil
	}
	rec := records[i]
	proof.record = &records[i]
	proof.decision = WorktreeDecisionFor(b, rec)
	_, err = os.Lstat(filepath.Join(y.Path, rec.Dir))
	proof.missing = errors.Is(err, os.ErrNotExist)
	if rec.Removed || rec.Purpose == "review" || !temporaryPurpose(rec.Purpose) || proof.missing && rec.Completion != nil || CheckSavedWorktreeReturn(y, rec, b, users) != nil {
		return proof, nil
	}
	owner := rec.Owner == c.agent.Name && rec.OwnerSince.Equal(c.agent.Since)
	lead := c.agent.Role == agent.RoleLead
	delegate := rec.CleanupOwner == c.agent.Name && rec.CleanupSince.Equal(c.agent.Since)
	if !lead && !owner && (!delegate || req.Completion != nil) {
		return proof, nil
	}
	if req.Completion != nil {
		if completionProblem(rec, *req.Completion) != "" || !matchesSavedProposal(rec, *req.Completion) {
			return proof, nil
		}
		if rec.Completion == nil {
			rec, err = proof.decision.bind(rec, *req.Completion)
			if err != nil || CheckSavedWorktreeReturn(y, rec, b, users) != nil {
				return proof, nil
			}
		}
	}
	if rec.Completion == nil {
		return proof, nil
	}
	proof.branchChecked = true
	proof.branchErr = branchTaskFinished(b, rec)
	if proof.branchErr == nil {
		proof.branchErr = s.verifyMerged(ctx, rec)
	}
	if proof.branchErr == nil && (!rec.BoardRound.IsZero() || publishedBranch(rec) != rec.Branch || len(proof.decision.sourceBranches) != 0) {
		proof.branchErr = s.checkOpenPulls(ctx, rec, proof.decision.sourceBranches...)
	}
	return proof, ctx.Err()
}

func (p removalVerification) branchProof(b yesmen.Board, rec yesmen.WorktreeRecord) error {
	if !p.branchChecked {
		return errors.New("branch completion requires fresh verification")
	}
	if p.branchErr != nil {
		return p.branchErr
	}
	return branchTaskFinished(b, rec)
}

func (s *Server) commitRemoval(ctx context.Context, c caller, y yesmen.Yesman, req removeWorktreeRequest, proof removalVerification) (cleanupResult, error) {
	var out cleanupResult
	var policyErr error
	err := s.Yesmen.TryWorktreeCommit(y, func() error {
		records, b, workers, err := s.Yesmen.WorktreeState(y)
		if err != nil {
			return err
		}
		i := latestWorktree(records, req.Dir)
		if i < 0 {
			out.Reason = "retain: unknown ownership; no agent lifecycle record"
			return nil
		}
		rec := &records[i]
		if req.Checkout == "" || req.Checkout != rec.ID {
			out.Reason = "retain: checkout ID changed or missing; old cleanup cannot affect a new round"
			return nil
		}
		if !rec.Removed && (proof.record == nil || !reflect.DeepEqual(*proof.record, *rec)) {
			out.Reason = "retain: checkout or saved evidence changed during verification. Retry with the current record"
			return nil
		}
		out, policyErr = s.finishRemoval(ctx, c, y, req, proof, records, i, b, workers)
		return nil
	})
	if err != nil {
		return cleanupResult{Reason: "retain: " + err.Error()}, nil
	}
	return out, policyErr
}
