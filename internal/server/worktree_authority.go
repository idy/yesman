package server

import (
	"errors"
	"slices"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func sameCertifiedReturn(a, b yesmen.WorktreeCompletion) bool {
	return sameWorktreeCompletion(a, b) && a.ReturnID == b.ReturnID && a.CertifiedBy == b.CertifiedBy && a.CertifiedSince.Equal(b.CertifiedSince) && a.SavedAt.Equal(b.SavedAt)
}

func sameReturnedSource(a, b yesmen.WorktreeCompletion) bool {
	return a.Head == b.Head && a.Ref == b.Ref && a.SourceBranch == b.SourceBranch && a.Publication == b.Publication
}

func currentWorktreeUser(y yesmen.Yesman, workers []yesmen.Worker, name string, since time.Time) bool {
	if since.IsZero() {
		return false
	}
	if name == yesmen.LeadName {
		return y.HiredAt.Equal(since)
	}
	return slices.ContainsFunc(workers, func(w yesmen.Worker) bool { return w.Name == name && w.StartedAt.Equal(since) })
}

func returnedTask(y yesmen.Yesman, rec yesmen.WorktreeRecord, b yesmen.Board, workers []yesmen.Worker) (*yesmen.Task, bool) {
	if !currentWorktreeUser(y, workers, rec.Owner, rec.OwnerSince) || rec.CleanupOwner != "" && !currentWorktreeUser(y, workers, rec.CleanupOwner, rec.CleanupSince) {
		return nil, false
	}
	i := slices.IndexFunc(b.Tasks, func(t yesmen.Task) bool { return t.ID == rec.Task })
	if i < 0 {
		return nil, false
	}
	t := &b.Tasks[i]
	if t.Status != yesmen.TaskDone || t.Paused() || t.Owner != rec.Owner || t.DoneOwner != rec.Owner || t.DoneAt.IsZero() || !t.OwnerSince.IsZero() && !rec.OwnerSince.Equal(t.OwnerSince) {
		return nil, false
	}
	return t, true
}

func boardProof(y yesmen.Yesman, rec yesmen.WorktreeRecord, t *yesmen.Task) *yesmen.WorktreeCompletion {
	for _, v := range t.Returns {
		if v.Checkout != rec.ID {
			continue
		}
		if v.SavedAt.IsZero() || !(v.CertifiedBy == rec.Owner && v.CertifiedSince.Equal(rec.OwnerSince) || v.CertifiedBy == yesmen.LeadName && v.CertifiedSince.Equal(y.HiredAt)) {
			return nil
		}
		return &v.WorktreeCompletion
	}
	return nil
}

// CheckSavedWorktreeReturn validates the Board authority attached to an
// already-certified checkout. Bulk preview may call it on its snapshot. A
// consumer acting on saved evidence calls it again on fresh WorktreeState
// within TryWorktreeCommit, through removal or delegation. Standalone manual
// completions use their own existing admission rules.
func CheckSavedWorktreeReturn(y yesmen.Yesman, rec yesmen.WorktreeRecord, b yesmen.Board, workers []yesmen.Worker) error {
	if rec.CreatedFor != nil && !slices.ContainsFunc(b.Tasks, func(t yesmen.Task) bool { return boundCreatingTask(y, rec, t) }) {
		return errors.New("the creating task or workspace no longer matches this checkout")
	}
	if rec.BoardRound.IsZero() {
		d := WorktreeDecisionFor(b, rec)
		if d.KeepsCheckout() {
			return errors.New("the task explicitly keeps this checkout. Its owner or lead can update the Board disposition")
		}
		if d.explicit && rec.Completion != nil {
			return errors.New("the current Board return supersedes the standalone completion. Wait for the service to verify this return")
		}
		if rec.Completion != nil && rec.Completion.ReturnID != "" {
			return errors.New("Board certification is missing its task round")
		}
		return nil
	}
	t, ok := returnedTask(y, rec, b, workers)
	if ok && t.DoneAt.Equal(rec.BoardRound) && rec.Completion != nil {
		if v := boardProof(y, rec, t); v != nil && sameCertifiedReturn(*rec.Completion, *v) {
			return nil
		}
	}
	return errors.New("the saved Board return is no longer current. Keep this checkout until its owner or lead returns it again")
}
