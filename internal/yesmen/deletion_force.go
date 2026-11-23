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
	if err := checkStagedGitHold(ws); err != nil {
		return CheckoutScan{}, nil, err
	}
	scan, hold := scanCheckouts(ctx, ws, func(dir *os.Root, checkout checkoutObservation) error {
		return checkCheckoutGit(ws, dir, checkout, checkout.Path)
	})
	if hold != nil {
		return scan, nil, hold
	}
	if scan.Problem != "" {
		return scan, nil, nil
	}
	if !target.Pending && len(scan.Entries) == 0 {
		return scan, nil, nil
	}
	plan := &ForceDeletion{target: target, paths: make(map[string]checkoutObservation, len(scan.observed)), top: map[string]os.FileInfo{}}
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

func (s *Store) DeleteWithCheckoutConfirmation(ctx context.Context, target DeletionTarget, force *ForceDeletion, registrations func(context.Context, string) error, stop func(Yesman) error, relocated RelocatedPrimary) (bool, error) {
	if force == nil || !matchDeletionTarget(force.target, target) || target.Archived || (!target.Pending && len(force.paths) == 0) {
		return false, errCheckoutPreviewChanged
	}
	return s.deleteWorkspace(ctx, target, registrations, stop, []RelocatedPrimary{relocated}, force)
}
