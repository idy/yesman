package yesmen

import (
	"context"
	"strings"
)

// ForceDeletion is an in-memory observation issued to the desktop, bound to
// the exact workspace and Git marker instances the confirmation showed.
// It cannot clear a pool registration or a saved lifecycle record.
type ForceDeletion struct {
	target DeletionTarget
	paths  map[string]checkoutObservation
}

func (s *Store) PrepareForceDeletion(ctx context.Context, target DeletionTarget, registrations func(context.Context, string) error, relocated RelocatedPrimary) (CheckoutScan, *ForceDeletion, error) {
	if target.Archived || registrations == nil {
		return CheckoutScan{}, nil, deletionIssue("Force Delete is available only for live workspaces and their interrupted deletions.")
	}
	parent, err := s.deletionParent(false)
	if err != nil {
		return CheckoutScan{}, nil, err
	}
	defer parent.Close()
	ws, err := openDeletionTarget(s, parent, target)
	if err != nil {
		return CheckoutScan{}, nil, err
	}
	defer ws.Close()
	scan := scanCheckouts(ctx, ws)
	if err := s.checkDeletionTarget(parent, target); err != nil {
		return CheckoutScan{}, nil, err
	}
	if scan.Problem != "" || len(scan.Entries) == 0 {
		return scan, nil, nil
	}
	plan := &ForceDeletion{target: target, paths: make(map[string]checkoutObservation, len(scan.observed))}
	for _, o := range scan.observed {
		if !o.directory.Usable() {
			return scan, nil, deletionIssue("Cannot verify the Git checkout directory at %s.", o.Path)
		}
		plan.paths[o.Path] = o
	}
	if err := registrations(ctx, ws.Name()); err != nil {
		return scan, nil, err
	}
	if err := walkWorkspaceDeletion(ctx, ws, inspectDeletion, plan); err != nil {
		return scan, nil, err
	}
	if err := s.checkWorktreeRecords(ctx, parent, ws, target, []RelocatedPrimary{relocated}, false); err != nil {
		return scan, nil, err
	}
	return scan, plan, nil
}

func (f *ForceDeletion) permitsTopLevel(dir string) bool {
	if f != nil {
		for rel := range f.paths {
			if rel == dir || strings.HasPrefix(rel, dir+"/") {
				return true
			}
		}
	}
	return false
}

func (s *Store) DeleteWithCheckoutConfirmation(ctx context.Context, target DeletionTarget, force *ForceDeletion, registrations func(context.Context, string) error, stop func(Yesman) error, relocated RelocatedPrimary) (bool, error) {
	if force == nil || !matchDeletionTarget(force.target, target) || target.Archived || len(force.paths) == 0 {
		return false, errCheckoutPreviewChanged
	}
	return s.deleteWorkspace(ctx, target, registrations, stop, []RelocatedPrimary{relocated}, force)
}
