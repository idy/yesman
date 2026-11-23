package yesmen

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// ForceDeletion binds the desktop confirmation to one workspace and the
// observed checkout and directory instances. A cold retry uses a fresh plan.
type ForceDeletion struct {
	target         DeletionTarget
	paths          map[string]checkoutObservation
	top            map[string]os.FileInfo
	scope          [32]byte
	approval       *deletionApproval
	approvalDigest [32]byte
	git            map[string]localGitBinding
	checks         LocalGitDeletionChecks
}

func (s *Store) PrepareForceDeletion(ctx context.Context, target DeletionTarget, registrations func(context.Context, string) error, relocated RelocatedPrimary, local ...LocalGitDeletionChecks) (CheckoutScan, *ForceDeletion, error) {
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
	if err := checkStagedGitHold(ws); err != nil {
		return CheckoutScan{}, nil, err
	}
	var checks LocalGitDeletionChecks
	validate := func(dir *os.Root, checkout checkoutObservation) error {
		return checkCheckoutGit(ws, dir, checkout, checkout.Path)
	}
	var git map[string]localGitBinding
	var receipt *os.File
	var approval *deletionApproval
	var approvalDigest [32]byte
	var cursor *deletionCursor
	if len(local) == 1 && local[0].Idle != nil {
		checks = local[0]
	}
	if target.Pending && recoverableDeletionName(target.Entry) {
		if checks.Idle == nil {
			return CheckoutScan{}, nil, errDeletionReceipt
		}
		var size int64
		receipt, approval, approvalDigest, size, err = readDeletionApproval(ctx, parent, target.Entry)
		if err != nil {
			return CheckoutScan{}, nil, err
		}
		defer receipt.Close()
		cursor, err = approvalCursor(receipt, size)
		if err != nil {
			return CheckoutScan{}, nil, err
		}
		validate = func(dir *os.Root, checkout checkoutObservation) error {
			if approval.Git[checkout.Path] != "" {
				return nil
			} // exact surviving scope checked below
			return checkCheckoutGit(ws, dir, checkout, checkout.Path)
		}
	} else if checks.Idle != nil {
		review, err := newLocalGitReview(ctx, ws)
		if err != nil {
			return CheckoutScan{}, nil, err
		}
		defer review.root.Close()
		validate, git = review.validate, review.paths
	}
	scan, hold := scanCheckouts(ctx, ws, validate)
	if hold != nil {
		return scan, nil, hold
	}
	if scan.Problem != "" {
		return scan, nil, nil
	}
	if !target.Pending && len(scan.Entries) == 0 {
		return scan, nil, nil
	}
	plan := &ForceDeletion{target: target, paths: make(map[string]checkoutObservation, len(scan.observed)), top: map[string]os.FileInfo{}, git: git, checks: checks, approval: approval, approvalDigest: approvalDigest}
	for _, o := range scan.observed {
		if !o.directory.Usable() {
			return scan, nil, deletionIssue("Cannot verify the Git checkout directory at %s.", o.Path)
		}
		plan.paths[o.Path] = o
		for name := range o.links {
			scan.LinkMarkers = append(scan.LinkMarkers, path.Join(o.Path, name))
		}
	}
	for _, binding := range git {
		if !binding.module {
			scan.LocalGitStores = append(scan.LocalGitStores, binding.admin)
		}
	}
	scan.Entries = slices.DeleteFunc(scan.Entries, func(e CheckoutEntry) bool { return git[e.Path].module })
	if approval != nil {
		for admin := range approval.admins() {
			// Include the originally approved stores even when the last marker
			// has gone. The user must still explicitly confirm the Git risk.
			if _, err := ws.Lstat(filepath.FromSlash(admin)); err == nil {
				scan.LocalGitStores = append(scan.LocalGitStores, admin)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return scan, nil, err
			}
		}
	}
	slices.Sort(scan.LocalGitStores)
	scan.LocalGitStores = slices.Compact(scan.LocalGitStores)
	slices.Sort(scan.LinkMarkers)
	if err := plan.observeTop(ctx, ws); err != nil {
		return scan, nil, err
	}
	if target.Pending && len(plan.top) == 0 && approval == nil {
		return scan, nil, nil // the existing empty-stage finish works
	}
	for name := range plan.top {
		scan.Paths = append(scan.Paths, name)
	}
	slices.Sort(scan.Paths)
	if err := registrations(ctx, ws.Name()); err != nil {
		return scan, nil, err
	}
	coverage := deletionCoverage{input: cursor, subset: approval != nil}
	if target.Pending {
		coverage.output = newDeletionInventory(nil)
	}
	if err := walkDeletion(ctx, ws, inspectDeletion, plan, coverage); err != nil {
		return scan, nil, err
	}
	if target.Pending {
		plan.scope = coverage.output.digest()
	}
	if err := plan.checkIdle(ctx, ws); err != nil {
		return scan, nil, err
	}
	if err := s.checkWorktreeRecords(ctx, ws, []RelocatedPrimary{relocated}, nil); err != nil {
		return scan, nil, err
	}
	if err := s.checkDeletionTarget(parent, target); err != nil {
		return scan, nil, err
	}
	return scan, plan, nil
}

// For a root checkout list its direct directories. A retry explicitly lists
// every remaining direct entry. Otherwise only checkout ancestors can relax
// the ordinary top-level directory guard.
func (f *ForceDeletion) observeTop(ctx context.Context, ws *os.Root) error {
	_, rootCheckout := f.paths["."]
	listing, err := readDeletionDirectory(ctx, ws, nil)
	if err != nil {
		return err
	}
	for e, err := range listing.sorted(ctx) {
		if err != nil {
			return err
		}
		name := e.Name()
		covered := f.target.Pending || rootCheckout && e.IsDir() && name != ".yesman"
		if !covered && name != ".yesman" && e.IsDir() {
			for check := range f.paths {
				if check == name || strings.HasPrefix(check, name+"/") {
					covered = true
					break
				}
			}
		}
		if !covered {
			continue
		}
		st, err := ws.Lstat(name)
		if err != nil || e.IsDir() != st.IsDir() {
			return errCheckoutPreviewChanged
		}
		f.top[name] = st
		if len(f.top) > checkoutResultLimit {
			return deletionIssue("Too many paths to show in a deletion confirmation. Move some files or directories out before trying again.")
		}
	}
	return nil
}

func sameDeletionEntry(want, got fs.FileInfo) bool {
	return want != nil && got != nil && want.Mode().Type() == got.Mode().Type() && os.SameFile(want, got) &&
		(!(want.Mode().IsRegular() || want.Mode().Type() == fs.ModeSymlink) || want.Size() == got.Size() && want.ModTime().Equal(got.ModTime()))
}

func (f *ForceDeletion) approvedGit() map[string]string {
	if f.approval != nil {
		return f.approval.Git
	}
	out := make(map[string]string, len(f.git))
	for rel, binding := range f.git {
		out[rel] = binding.admin
	}
	return out
}

func (f *ForceDeletion) approvedGitConfigs() map[string][32]byte {
	if f.approval != nil {
		return f.approval.Configs
	}
	configs := make(map[string][32]byte, len(f.git))
	for _, binding := range f.git {
		configs[binding.admin] = binding.config
	}
	return configs
}

func (f *ForceDeletion) approvedModulePointers() map[string][32]byte {
	if f.approval != nil {
		return f.approval.Pointers
	}
	pointers := map[string][32]byte{}
	for rel, binding := range f.git {
		if binding.module && rel != binding.admin {
			pointers[rel] = binding.pointer
		}
	}
	return pointers
}

func (f *ForceDeletion) checkIdle(ctx context.Context, ws *os.Root) error {
	if f == nil || (len(f.git) == 0 && f.approval == nil) {
		return nil
	}
	if err := f.checks.Idle(ctx, ws.Name()); err != nil {
		return &DeletionIssue{Message: "Could not verify that %s is idle.", Args: []any{ws.Name()}, Cause: err}
	}
	return nil
}

func (s *Store) DeleteWithCheckoutConfirmation(ctx context.Context, target DeletionTarget, force *ForceDeletion, registrations func(context.Context, string) error, stop func(Yesman) error, relocated RelocatedPrimary) (bool, error) {
	if force == nil || !matchDeletionTarget(force.target, target) || target.Archived || (!target.Pending && len(force.paths) == 0) {
		return false, errCheckoutPreviewChanged
	}
	return s.deleteWorkspace(ctx, target, registrations, stop, []RelocatedPrimary{relocated}, force)
}
