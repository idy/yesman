package yesmen

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
)

// Walk one verified directory handle at a time. A file or link is unlinked as
// an entry; only an empty directory is ever removed. This deliberately avoids
// recursive RemoveAll, which would also erase a nested checkout or mounted disk.
func walkDeletion(ctx context.Context, ws *os.Root, remove bool) error {
	volume, err := directoryIdentity(ws)
	if err != nil {
		return err
	}
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
		entries, err := fs.ReadDir(dir.FS(), ".")
		if err != nil {
			return err
		}
		var head, objects bool
		for _, e := range entries {
			switch strings.ToLower(e.Name()) {
			case ".git":
				return deletionIssue("%s contains a Git checkout. Move it with Git or finish its managed cleanup first.", rel)
			case "objects":
				objects = true
			case "head":
				head = true
			}
		}
		if head && objects {
			return deletionIssue("%s contains Git repository data. Move the repository out of the workspace first.", rel)
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
			switch {
			case st.Mode().Type() == fs.ModeDir:
				if rel == "." && name != ".yesman" {
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
			if remove {
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
	return visit(ws, ".")
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
