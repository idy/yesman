package tasklog

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// File pins the exact regular file validated by Open. Validate checks the
// actual descriptor and the original parent identities; it never reopens it.
type File struct {
	*os.File
	root     *os.Root
	path     string
	parents  []parentIdentity
	maxLinks uint64
}
type parentIdentity struct {
	path string
	info os.FileInfo
}

func pathParts(name string) ([]string, error) {
	parts := strings.Split(name, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, "\\\x00") {
			return nil, errors.New("invalid working log relative path")
		}
	}
	return parts, nil
}

// Open traverses ordinary parents through directory handles. A replaced
// parent cannot redirect the leaf open through a symlink or another directory.
// The workspace root itself may be a legitimate alias, as with os.Root.
func Open(root *os.Root, name string) (*File, error) { return openChecked(root, name, 1) }

func openChecked(root *os.Root, name string, maxLinks uint64) (*File, error) {
	parts, err := pathParts(name)
	if err != nil {
		return nil, err
	}
	dir := root
	var opened []*os.Root
	defer func() {
		for i := len(opened) - 1; i >= 0; i-- {
			_ = opened[i].Close()
		}
	}()
	var parents []parentIdentity
	for i, part := range parts[:len(parts)-1] {
		before, e := dir.Lstat(part)
		if e != nil {
			return nil, e
		}
		if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("linked or invalid working log parent: %s", part)
		}
		child, e := dir.OpenRoot(part)
		if e != nil {
			return nil, e
		}
		opened = append(opened, child)
		actual, e := child.Stat(".")
		if e != nil {
			return nil, e
		}
		current, e := dir.Lstat(part)
		if e != nil {
			return nil, e
		}
		if current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(before, actual) || !os.SameFile(current, actual) {
			return nil, errors.New("working log parent changed while opening")
		}
		parents = append(parents, parentIdentity{strings.Join(parts[:i+1], "/"), actual})
		dir = child
	}
	leaf := parts[len(parts)-1]
	before, err := dir.Lstat(leaf)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("not a regular working log file: %s", name)
	}
	raw, err := openRead(dir, leaf)
	if err != nil {
		return nil, err
	}
	f := &File{File: raw, root: root, path: name, parents: parents, maxLinks: maxLinks}
	actual, err := raw.Stat()
	if err == nil && !os.SameFile(before, actual) {
		err = errors.New("working log file changed while opening")
	}
	if err == nil {
		err = f.Validate()
	}
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return f, nil
}

func (f *File) Validate() error {
	actual, err := f.File.Stat()
	if err != nil {
		return err
	}
	n, err := links(f.File)
	if err != nil {
		return err
	}
	if !actual.Mode().IsRegular() || n < 1 || n > f.maxLinks {
		return errors.New("working log descriptor is not an unaliased regular file")
	}
	for _, parent := range f.parents {
		current, e := f.root.Lstat(parent.path)
		if e != nil {
			return e
		}
		if !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(parent.info, current) {
			return errors.New("working log parent changed during read")
		}
	}
	current, err := f.root.Lstat(f.path)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(current, actual) {
		return errors.New("working log path changed during read")
	}
	return nil
}
