package yesmen

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
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
}

// CheckoutScan may have rows even when coverage is incomplete. Only a complete
// scan with an unchanged target can be used to prepare force deletion.
type CheckoutScan struct {
	Entries  []CheckoutEntry `json:"entries"`
	Problem  string          `json:"problem,omitempty"`
	observed []checkoutObservation
}

func gitMarkers(rel string, entries []os.DirEntry) (CheckoutEntry, []string) {
	var dotGit, bare []string
	var head, objects bool
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
		}
	}
	if len(dotGit) > 0 {
		return CheckoutEntry{Path: rel}, dotGit
	}
	// HEAD/objects immediately inside .git belong to the enclosing checkout.
	if head && objects && !strings.EqualFold(path.Base(rel), ".git") {
		return CheckoutEntry{Path: rel, Bare: true}, bare
	}
	return CheckoutEntry{}, nil
}

func observeCheckout(dir *os.Root, entry CheckoutEntry, id safefs.Identity, names []string) (checkoutObservation, error) {
	o := checkoutObservation{CheckoutEntry: entry, directory: id, markers: make(map[string]fs.FileInfo, len(names))}
	for _, name := range names {
		st, err := dir.Lstat(name)
		if err != nil {
			return o, err
		}
		o.markers[name] = st
	}
	return o, nil
}

func (a checkoutObservation) matches(b checkoutObservation) bool {
	if a.CheckoutEntry != b.CheckoutEntry || a.directory != b.directory || len(a.markers) != len(b.markers) {
		return false
	}
	for name, x := range a.markers {
		y := b.markers[name]
		if y == nil || !os.SameFile(x, y) || x.Mode().Type() != y.Mode().Type() {
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

var errCheckoutPreviewChanged = deletionIssue("The Git checkout paths changed. Review a fresh deletion confirmation.")

// InspectCheckouts is invoked only by the visible Worktrees panel and delete
// confirmation. It does not run Git or open the metadata a marker points to.
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
	scan := scanCheckouts(ctx, ws)
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
)

// The scan has independent entry, depth, result and deadline limits. When a
// subtree is unreadable, keep what was found in the rest of the workspace.
func scanCheckouts(ctx context.Context, ws *os.Root) CheckoutScan {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	scan := CheckoutScan{Entries: []CheckoutEntry{}}
	problem := func(err error) {
		if scan.Problem == "" {
			scan.Problem = err.Error()
		}
	}
	volume, err := directoryIdentity(ws)
	if err != nil {
		problem(err)
		return scan
	}
	remaining := checkoutEntryLimit
	var visit func(*os.Root, string, int)
	visit = func(dir *os.Root, rel string, depth int) {
		if ctx.Err() != nil || remaining <= 0 || depth > checkoutDepthLimit || len(scan.Entries) >= checkoutResultLimit {
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
		f, err := dir.Open(".")
		if err != nil {
			problem(err)
			return
		}
		// Check the budget between batches, even for one huge directory.
		var entries []os.DirEntry
		for {
			if ctx.Err() != nil || remaining <= 0 {
				problem(deletionIssue("The workspace Git scan reached its limit."))
				break
			}
			batch, err := f.ReadDir(min(remaining, 256))
			entries = append(entries, batch...)
			remaining -= len(batch)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					problem(err)
				}
				break
			}
		}
		f.Close()
		entry, markers := gitMarkers(rel, entries)
		if len(markers) > 0 {
			scan.Entries = append(scan.Entries, entry)
			o, err := observeCheckout(dir, entry, id, markers)
			if err != nil {
				problem(err)
			} else {
				scan.observed = append(scan.observed, o)
			}
		}
		for _, entry := range entries {
			if ctx.Err() != nil || remaining <= 0 || len(scan.Entries) >= checkoutResultLimit {
				problem(deletionIssue("The workspace Git scan reached its limit."))
				return
			}
			if !entry.IsDir() {
				continue
			}
			child, err := directDir(dir, entry.Name())
			if err != nil {
				problem(err)
				continue
			}
			visit(child, path.Join(rel, entry.Name()), depth+1)
			child.Close()
		}
	}
	visit(ws, ".", 0)
	slices.SortFunc(scan.Entries, func(a, b CheckoutEntry) int { return strings.Compare(a.Path, b.Path) })
	return scan
}
