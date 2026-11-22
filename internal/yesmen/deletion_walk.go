package yesmen

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
)

type deletionWalk uint8

const (
	inspectDeletion deletionWalk = iota
	prepareDeletion
	purgeDeletion
)

// Walk one verified directory handle at a time. A file or link is unlinked as
// an entry. Only an empty directory is ever removed.
func walkWorkspaceDeletion(ctx context.Context, ws *os.Root, phase deletionWalk, force *ForceDeletion) error {
	return walkDeletion(ctx, ws, phase, force, false, nil)
}

func walkDeletion(ctx context.Context, ws *os.Root, phase deletionWalk, force *ForceDeletion, snapshot bool, receipt *deletionGitReceipt) error {
	volume, err := directoryIdentity(ws)
	if err != nil {
		return err
	}
	seen, seenTop, seenRemaining := 0, 0, 0
	var visit func(*os.Root, string) error
	visit = func(dir *os.Root, rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		id, err := directoryIdentity(dir)
		if err != nil {
			return err
		}
		if !sameMount(id, volume) {
			return deletionIssue("Unmount %s before deleting this workspace.", rel)
		}
		limit := -1
		entries, err := readDeletionEntries(ctx, dir, &limit)
		if err != nil {
			return err
		}
		slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		entry, markers := gitMarkers(rel, entries)
		var verified map[string]fs.FileInfo
		if len(markers) > 0 {
			if force == nil {
				if entry.Bare {
					return deletionIssue("%s contains Git repository data. Move the repository out of the workspace first.", rel)
				}
				return deletionIssue("%s contains a Git checkout. Move it with Git or finish its managed cleanup first.", rel)
			}
			current, err := observeCheckout(dir, entry, id, markers)
			if err != nil {
				return err
			}
			if !force.paths[rel].matches(current) {
				return errCheckoutPreviewChanged
			}
			verified = current.markers
			seen++
			if phase == purgeDeletion {
				// Keep the marker until the ordinary files have been removed.
				// An interrupted purge can then show it again on retry.
				other := make([]os.DirEntry, 0, len(entries))
				for _, e := range entries {
					if verified[e.Name()] == nil {
						other = append(other, e)
					}
				}
				for _, e := range entries {
					if verified[e.Name()] != nil {
						other = append(other, e)
					}
				}
				entries = other
			}
		} else if force != nil {
			if _, approved := force.paths[rel]; approved {
				return errCheckoutPreviewChanged
			}
		}
		if len(entries) > 0 && rel == "." && phase == purgeDeletion {
			// The Git receipt remains available until the staged data is gone.
			for i, e := range entries {
				if e.Name() == stagedGitReceipt {
					copy(entries[i:], entries[i+1:])
					entries[len(entries)-1] = e
					break
				}
			}
		}
		if len(entries) > 0 {
			if err := deletionDirectoryAccess(dir, rel, phase != inspectDeletion); err != nil {
				return err
			}
		}
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := e.Name()
			child := path.Join(rel, name)
			st, err := dir.Lstat(name)
			if err != nil {
				return err
			}
			if expected := verified[name]; expected != nil && !sameDeletionEntry(expected, st) {
				return errCheckoutPreviewChanged
			}
			if force != nil && rel == "." && force.top[name] != nil {
				if !sameDeletionEntry(force.top[name], st) {
					return errCheckoutPreviewChanged
				}
				seenTop++
			}
			if force != nil && force.remaining != nil {
				if snapshot {
					if err := force.observeRemaining(child, st); err != nil {
						return err
					}
				} else if !sameDeletionEntry(force.remaining[child], st) {
					return errCheckoutPreviewChanged
				}
				seenRemaining++
			}
			switch {
			case st.Mode().Type() == fs.ModeDir:
				if rel == "." && name != ".yesman" && (force == nil || force.top[name] == nil) {
					return deletionIssue("Move %q out of the workspace first. For a worktree, use Git or its managed cleanup.", name)
				}
				d, err := directDir(dir, name)
				if err != nil {
					return err
				}
				opened, err := d.Stat(".")
				if err != nil || !os.SameFile(st, opened) {
					d.Close()
					return fmt.Errorf("%s changed during deletion", child)
				}
				err = visit(d, child)
				d.Close()
				if err != nil {
					return err
				}
			case child == ".yesman":
				return errors.New(".yesman must be the workspace's own directory")
			case strings.HasPrefix(child, ".yesman/sessions/") || rel == ".yesman":
				// An active agent drain reads/writes here. Do not let it follow a
				// linked session path, wait on a pipe or truncate a linked temp.
				if !st.Mode().IsRegular() {
					return deletionIssue("Move the linked or special metadata entry %s out of the workspace first.", child)
				}
				if strings.HasSuffix(name, ".tmp") {
					if err := singleDeletionFile(dir, name, st); err != nil {
						return err
					}
				}
			}
			if phase == purgeDeletion {
				if receipt != nil && verified[name] != nil && st.Mode().IsRegular() && name == ".git" {
					if err := receipt.beginGitfileRemoval(ws, dir, child, st); err != nil {
						return err
					}
				}
				now, err := dir.Lstat(name)
				if err != nil || !os.SameFile(st, now) {
					return fmt.Errorf("%s changed during deletion", child)
				}
				if err := dir.Remove(name); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(ws, "."); err != nil {
		return err
	}
	if force != nil && (seen != len(force.paths) || seenTop != len(force.top) || seenRemaining != len(force.remaining)) {
		return errCheckoutPreviewChanged
	}
	return nil
}

func singleDeletionFile(dir *os.Root, name string, st fs.FileInfo) error {
	f, err := dir.OpenFile(name, os.O_RDONLY|openNoBlock, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(st, opened) || !opened.Mode().IsRegular() {
		return fmt.Errorf("%s changed during deletion", name)
	}
	n, err := linkCount(f, opened)
	if err != nil || n != 1 {
		return deletionIssue("Move the linked metadata file %s out of the workspace first.", name)
	}
	return nil
}
