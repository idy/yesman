package yesmen

import (
	"context"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
)

// ForceDeletion binds the desktop confirmation to one workspace and the
// observed checkout and directory instances. A cold retry uses a fresh plan.
type ForceDeletion struct {
	target    DeletionTarget
	paths     map[string]checkoutObservation
	top       map[string]os.FileInfo
	remaining map[string]os.FileInfo // complete snapshot for a staged retry
	git       map[string]localGitBinding
	checks    LocalGitDeletionChecks
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
	if len(local) == 1 && local[0].Idle != nil {
		checks = local[0]
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
	plan := &ForceDeletion{target: target, paths: make(map[string]checkoutObservation, len(scan.observed)), top: map[string]os.FileInfo{}, git: git, checks: checks}
	if target.Pending {
		plan.remaining = map[string]os.FileInfo{}
	}
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
	slices.Sort(scan.LocalGitStores)
	slices.Sort(scan.LinkMarkers)
	if err := plan.observeTop(ctx, ws); err != nil {
		return scan, nil, err
	}
	if target.Pending && len(plan.top) == 0 {
		return scan, nil, nil // the existing empty-stage finish works
	}
	for name := range plan.top {
		scan.Paths = append(scan.Paths, name)
	}
	slices.Sort(scan.Paths)
	if err := registrations(ctx, ws.Name()); err != nil {
		return scan, nil, err
	}
	if err := walkDeletion(ctx, ws, inspectDeletion, plan, true); err != nil {
		return scan, nil, err
	}
	if err := plan.checkIdle(ctx, ws); err != nil {
		return scan, nil, err
	}
	if err := s.checkWorktreeRecords(ctx, parent, ws, target, []RelocatedPrimary{relocated}, false); err != nil {
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
	limit := -1
	entries, err := readDeletionEntries(ctx, ws, &limit)
	if err != nil {
		return err
	}
	for _, e := range entries {
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

func (f *ForceDeletion) observeRemaining(rel string, st fs.FileInfo) error {
	if len(f.remaining) >= checkoutEntryLimit {
		return deletionIssue("The workspace Git scan reached its limit.")
	}
	f.remaining[rel] = st
	return nil
}

func (f *ForceDeletion) checkIdle(ctx context.Context, ws *os.Root) error {
	if f == nil || len(f.git) == 0 {
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
