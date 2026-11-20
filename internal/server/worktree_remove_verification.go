package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// The remote check is bound to the exact record read and the completion in
// the request. It runs under a cancellable caller lease, outside the locks
// for local work. A new caller admission covers the subsequent local commit.
type removalVerification struct {
	record        *yesmen.WorktreeRecord
	missing       bool
	branchChecked bool
	branchErr     error
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
		if completionProblem(rec, *req.Completion) != "" || rec.Completion != nil && !sameWorktreeCompletion(*rec.Completion, *req.Completion) {
			return proof, nil
		}
		rec.Completion = req.Completion
	}
	if rec.Completion == nil {
		return proof, nil
	}
	proof.branchChecked = true
	proof.branchErr = branchTaskFinished(b, rec)
	if proof.branchErr == nil {
		proof.branchErr = s.verifyMerged(ctx, rec)
	}
	if proof.branchErr == nil && (!rec.BoardRound.IsZero() || publishedBranch(rec) != rec.Branch) {
		proof.branchErr = s.checkOpenPulls(ctx, rec)
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
