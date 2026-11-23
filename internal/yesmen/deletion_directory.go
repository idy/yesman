package yesmen

import (
	"context"
	"errors"
	"io"
	"iter"
	"os"
	"slices"
	"strings"
)

// A page bounds the memory used to order even very wide directories. Narrow
// directories are read once. Wide ones are reread to choose the next page
// without retaining every entry or relying on filesystem enumeration order.
const deletionDirectoryPage = 64 << 10

type deletionDirectory struct {
	root    *os.Root
	entries []os.DirEntry
	markers []os.DirEntry
	wide    bool
}

func readDeletionDirectory(ctx context.Context, dir *os.Root, check func(string) error) (deletionDirectory, error) {
	out := deletionDirectory{root: dir}
	err := eachDeletionEntry(ctx, dir, func(e os.DirEntry) error {
		if check != nil {
			if err := check(e.Name()); err != nil {
				return err
			}
		}
		if !out.wide {
			out.entries = append(out.entries, e)
			if len(out.entries) > deletionDirectoryPage {
				out.wide = true
				for _, prior := range out.entries {
					out.addMarker(prior)
				}
				out.entries = nil
			}
		} else {
			out.addMarker(e)
		}
		if len(out.markers) > checkoutResultLimit {
			return deletionIssue("The workspace Git scan reached its limit.")
		}
		return nil
	})
	if !out.wide {
		out.markers = out.entries
	}
	return out, err
}

func (d *deletionDirectory) addMarker(e os.DirEntry) {
	switch strings.ToLower(e.Name()) {
	case ".git", "head", "objects", "config", "refs", "packed-refs", "worktrees", "commondir", "gitdir":
		d.markers = append(d.markers, e)
	}
}

func (d deletionDirectory) empty() bool { return !d.wide && len(d.entries) == 0 }

func (d deletionDirectory) sorted(ctx context.Context) iter.Seq2[os.DirEntry, error] {
	order := func(a, b os.DirEntry) int { return compareDeletionName(a.Name(), b.Name()) }
	return func(emit func(os.DirEntry, error) bool) {
		if !d.wide {
			slices.SortFunc(d.entries, order)
			for _, e := range d.entries {
				if !emit(e, nil) {
					return
				}
			}
			return
		}
		var page []os.DirEntry
		var after string
		for {
			page = page[:0]
			more := false
			err := eachDeletionEntry(ctx, d.root, func(e os.DirEntry) error {
				if after != "" && compareDeletionName(after, e.Name()) >= 0 {
					return nil
				}
				page = append(page, e)
				if len(page) == 2*deletionDirectoryPage {
					slices.SortFunc(page, order)
					clear(page[deletionDirectoryPage:])
					page = page[:deletionDirectoryPage]
					more = true
				}
				return nil
			})
			if err != nil {
				emit(nil, err)
				return
			}
			slices.SortFunc(page, order)
			if len(page) > deletionDirectoryPage {
				clear(page[deletionDirectoryPage:])
				page = page[:deletionDirectoryPage]
				more = true
			}
			for _, e := range page {
				if !emit(e, nil) {
					return
				}
			}
			if !more {
				return
			}
			after = page[len(page)-1].Name()
		}
	}
}

func eachDeletionEntry(ctx context.Context, dir *os.Root, visit func(os.DirEntry) error) error {
	file, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, err := file.ReadDir(256)
		for _, entry := range batch {
			if err := visit(entry); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
