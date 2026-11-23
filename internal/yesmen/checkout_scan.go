package yesmen

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/idy/yesman/internal/safefs"
)

// CheckoutEntry describes an on-disk Git marker, not a managed worktree or
// permission to remove one. The observation never crosses the desktop bridge.
type CheckoutEntry struct {
	Path string `json:"path"`
	Bare bool   `json:"bare,omitempty"`
}

type checkoutObservation struct {
	CheckoutEntry
	directory safefs.Identity
	markers   map[string]fs.FileInfo
	links     map[string]string // literal symlink destinations, never resolved
}

// CheckoutScan may have rows even when coverage is incomplete. Only a complete
// scan with an unchanged target can be used to prepare force deletion.
type CheckoutScan struct {
	Entries        []CheckoutEntry `json:"entries"`
	Paths          []string        `json:"paths,omitempty"`
	LinkMarkers    []string        `json:"linkMarkers,omitempty"`
	LocalGitStores []string        `json:"localGitStores,omitempty"`
	Problem        string          `json:"problem,omitempty"`
	observed       []checkoutObservation
}

func gitMarkers(rel string, entries []os.DirEntry) (CheckoutEntry, []string) {
	var dotGit, bare []string
	var head, objects, worktrees, private, config, refs bool
	for _, entry := range entries {
		name := entry.Name()
		switch strings.ToLower(name) {
		case ".git":
			dotGit = append(dotGit, name)
		case "head":
			head = true
			bare = append(bare, name)
		case "objects":
			objects = true
			bare = append(bare, name)
		case "config":
			config = true
		case "refs", "packed-refs":
			refs = true
		case "worktrees":
			worktrees = true
		case "commondir", "gitdir":
			private = true
			bare = append(bare, name)
		}
	}
	// Private Git data can be separated from its common directory. A .git
	// directory is already covered by the enclosing checkout's marker.
	if (objects && (head || worktrees) || head && private || config && (head || objects) && refs) && !strings.EqualFold(path.Base(rel), ".git") {
		return CheckoutEntry{Path: rel, Bare: true}, append(bare, dotGit...)
	}
	if len(dotGit) > 0 {
		return CheckoutEntry{Path: rel}, dotGit
	}
	return CheckoutEntry{}, nil
}

func deletionGitMarkers(dir *os.Root, rel string, entries []os.DirEntry, legacyStage bool) (CheckoutEntry, []string) {
	entry, markers := gitMarkers(rel, entries)
	if !legacyStage || len(markers) != 0 || strings.EqualFold(path.Base(rel), ".git") {
		return entry, markers
	}
	// Earlier purges had no approval inventory. Hold recognizable partial
	// admin data when their original marker set is no longer available.
	var config, refs, objects string
	for _, e := range entries {
		name := e.Name()
		switch strings.ToLower(name) {
		case "head":
			data, _, err := readRegularEntry(dir, name, gitDeletionFileLimit, nil, true)
			if err == nil && validLocalHEAD(data) {
				return CheckoutEntry{Path: rel, Bare: true}, []string{name}
			}
		case "commondir", "gitdir":
			return CheckoutEntry{Path: rel, Bare: true}, []string{name}
		case "config":
			config = name
		case "refs", "packed-refs":
			refs = name
		case "objects":
			objects = name
		}
	}
	if refs != "" && objects != "" {
		return CheckoutEntry{Path: rel, Bare: true}, []string{refs, objects}
	}
	if strings.HasSuffix(strings.ToLower(rel), ".git") {
		for _, name := range []string{config, refs, objects} {
			if name != "" {
				return CheckoutEntry{Path: rel, Bare: true}, []string{name}
			}
		}
	}
	return entry, markers
}

func isLegacyDeletionStage(ws *os.Root) bool {
	name := filepath.Base(ws.Name())
	return strings.HasPrefix(name, deletingPrefix) && !recoverableDeletionName(name)
}

func observeCheckout(dir *os.Root, entry CheckoutEntry, id safefs.Identity, names []string) (checkoutObservation, error) {
	o := checkoutObservation{CheckoutEntry: entry, directory: id, markers: make(map[string]fs.FileInfo, len(names))}
	for _, name := range names {
		st, err := dir.Lstat(name)
		if err != nil {
			return o, err
		}
		o.markers[name] = st
		if st.Mode().Type() == fs.ModeSymlink {
			target, err := dir.Readlink(name)
			if err != nil {
				return o, err
			}
			now, err := dir.Lstat(name)
			if err != nil || !sameDeletionEntry(st, now) {
				return o, errCheckoutPreviewChanged
			}
			if o.links == nil {
				o.links = make(map[string]string)
			}
			o.links[name] = target
		}
	}
	return o, nil
}

func (a checkoutObservation) matches(b checkoutObservation) bool {
	if a.CheckoutEntry != b.CheckoutEntry || a.directory != b.directory || len(a.markers) != len(b.markers) {
		return false
	}
	for name, x := range a.markers {
		y := b.markers[name]
		if y == nil || !os.SameFile(x, y) || x.Mode().Type() != y.Mode().Type() || a.links[name] != b.links[name] {
			return false
		}
		// A regular gitfile (including a linked-worktree pointer) can also
		// be rewritten in place. Directory contents are the user's work.
		if x.Mode().IsRegular() && (x.Size() != y.Size() || !x.ModTime().Equal(y.ModTime())) {
			return false
		}
	}
	return true
}

func matchDeletionTarget(want, now DeletionTarget) bool {
	return want.Entry == now.Entry && want.Archived == now.Archived && want.Instance != "" &&
		want.Instance == now.Instance && want.Label == now.Label && want.Pending == now.Pending &&
		want.Problem == "" && now.Problem == "" && want.HiredAt.Equal(now.HiredAt)
}

var errCheckoutPreviewChanged = deletionIssue("Workspace files or Git checkout paths changed. Review a fresh deletion confirmation.")

// InspectCheckouts is invoked only by the visible Worktrees panel. It does
// not run Git or open the metadata a marker points to.
func (s *Store) InspectCheckouts(ctx context.Context, target DeletionTarget) (CheckoutScan, error) {
	parent, err := s.deletionParent(target.Archived)
	if err != nil {
		return CheckoutScan{}, err
	}
	defer parent.Close()
	ws, err := openDeletionTarget(s, parent, target)
	if err != nil {
		return CheckoutScan{}, err
	}
	defer ws.Close()
	scan, _ := scanCheckouts(ctx, ws, nil)
	if err := s.checkDeletionTarget(parent, target); err != nil {
		return CheckoutScan{}, err
	}
	return scan, nil
}

func openDeletionTarget(s *Store, parent *os.Root, target DeletionTarget) (*os.Root, error) {
	original, name, staged, ok := deletionEntry(target.Entry, target.Archived)
	if !ok || target.Instance == "" || target.Problem != "" {
		return nil, errors.New("invalid workspace deletion confirmation")
	}
	ws, err := directDir(parent, target.Entry)
	if err != nil {
		return nil, err
	}
	now, err := s.deletionIdentity(parent, ws, DeletionTarget{Entry: target.Entry, Archived: target.Archived, Pending: staged != "", Label: name}, original, staged)
	if err == nil && !matchDeletionTarget(target, now) {
		err = errors.New("the workspace changed since confirmation. Reopen its menu")
	}
	if err != nil {
		ws.Close()
		return nil, err
	}
	return ws, nil
}

func (s *Store) checkDeletionTarget(parent *os.Root, target DeletionTarget) error {
	ws, err := openDeletionTarget(s, parent, target)
	if err == nil {
		ws.Close()
	}
	return err
}

const (
	checkoutEntryLimit  = 1_000_000
	checkoutDepthLimit  = 96
	checkoutResultLimit = 256
	deletionDepthLimit  = 256
)

// Only sidebar discovery has the short deadline and depth/entry budget.
// Deletion review checks every subtree before authorizing deletion. A
// definitive Git hold can end the review as soon as it is encountered.
func scanCheckouts(ctx context.Context, ws *os.Root, validate func(*os.Root, checkoutObservation) error) (CheckoutScan, error) {
	review := validate != nil
	remaining := -1
	if !review {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		remaining = checkoutEntryLimit
	}
	scan := CheckoutScan{Entries: []CheckoutEntry{}}
	problem := func(err error) {
		if scan.Problem == "" {
			scan.Problem = err.Error()
		}
	}
	volume, err := directoryIdentity(ws)
	if err != nil {
		problem(err)
		return scan, nil
	}
	var hold error
	legacyStage := review && isLegacyDeletionStage(ws)
	var visit func(*os.Root, string, int, bool)
	visit = func(dir *os.Root, rel string, depth int, gitDir bool) {
		if err := ctx.Err(); err != nil {
			problem(err)
			return
		}
		if remaining == 0 || depth > deletionDepthLimit || (!review && depth > checkoutDepthLimit) || len(scan.Entries) >= checkoutResultLimit {
			problem(deletionIssue("The workspace Git scan reached its limit."))
			return
		}
		id, err := directoryIdentity(dir)
		if err != nil {
			problem(err)
			return
		}
		if !sameMount(id, volume) {
			problem(deletionIssue("Unmount %s before deleting this workspace.", rel))
			return
		}
		var listing deletionDirectory
		if review {
			listing, err = readDeletionDirectory(ctx, dir, nil)
		} else {
			listing.entries, err = readDeletionEntries(ctx, dir, &remaining)
			listing.markers = listing.entries
		}
		if err != nil {
			problem(err)
			if review {
				return
			}
		}
		entry, markers := deletionGitMarkers(dir, rel, listing.markers, legacyStage)
		objects := !review && (entry.Bare || gitDir) && gitObjectStore(ctx, dir, volume)
		if len(markers) > 0 {
			scan.Entries = append(scan.Entries, entry)
			o, err := observeCheckout(dir, entry, id, markers)
			if err != nil {
				problem(err)
			} else {
				scan.observed = append(scan.observed, o)
				if review {
					hold = validate(dir, o)
					if hold != nil {
						return
					}
				}
			}
		}
		for entry, err := range listing.sorted(ctx) {
			if err != nil {
				problem(err)
				return
			}
			if err := ctx.Err(); err != nil {
				problem(err)
				return
			}
			if remaining == 0 || len(scan.Entries) >= checkoutResultLimit {
				problem(deletionIssue("The workspace Git scan reached its limit."))
				return
			}
			name := entry.Name()
			childPath := path.Join(rel, name)
			// Agent build outputs are not sidebar discoveries. Deletion review
			// still has to account for every entry, including this subtree.
			if !entry.IsDir() || (!review && childPath == ".yesman/tmp") || (objects && name == "objects") {
				continue
			}
			child, err := directDir(dir, name)
			if err != nil {
				problem(err)
				continue
			}
			visit(child, childPath, depth+1, slices.Contains(markers, name) && strings.EqualFold(name, ".git"))
			child.Close()
			if hold != nil {
				return
			}
		}
	}
	visit(ws, ".", 0, false)
	slices.SortFunc(scan.Entries, func(a, b CheckoutEntry) int { return strings.Compare(a.Path, b.Path) })
	return scan, hold
}

func readDeletionEntries(ctx context.Context, dir *os.Root, remaining *int) ([]os.DirEntry, error) {
	file, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var entries []os.DirEntry
	for {
		if err := ctx.Err(); err != nil {
			return entries, err
		}
		if *remaining == 0 {
			return entries, deletionIssue("The workspace Git scan reached its limit.")
		}
		size := 256
		if *remaining > 0 {
			size = min(size, *remaining)
		}
		batch, err := file.ReadDir(size)
		entries = append(entries, batch...)
		if *remaining > 0 {
			*remaining -= len(batch)
		}
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return entries, err
		}
	}
}

// Git's object fanout is not a checkout. Skip it for discovery only after
// checking the directory and its layout. Deletion review and purge still walk
// its contents, so a nonstandard nested checkout cannot go unreviewed.
func gitObjectStore(ctx context.Context, dir *os.Root, volume safefs.Identity) bool {
	head, err := dir.Lstat("HEAD")
	if err != nil || !head.Mode().IsRegular() {
		return false
	}
	objects, err := directDir(dir, "objects")
	if err != nil {
		return false
	}
	defer objects.Close()
	id, err := directoryIdentity(objects)
	if err != nil || !sameMount(id, volume) {
		return false
	}
	limit := 260 // 256 loose-object buckets, pack and info
	entries, err := readDeletionEntries(ctx, objects, &limit)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || (name != "pack" && name != "info" && (len(name) != 2 || strings.Trim(name, "0123456789abcdef") != "")) {
			return false
		}
	}
	return true
}

// Open each workspace-relative component without following a directory link.
func openDeletionChild(root *os.Root, rel string) (*os.Root, error) {
	dir := root
	for name := range strings.SplitSeq(filepath.ToSlash(rel), "/") {
		next, err := directDir(dir, name)
		if dir != root {
			dir.Close()
		}
		if err != nil {
			return nil, err
		}
		dir = next
	}
	return dir, nil
}
