//go:build windows

package server

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// dirHandle is an open directory. Windows has no openat with a no-follow
// flag that Go exposes, so each name below it is first looked at without
// following it (a link, junction or any other reparse point is refused),
// then opened through the os.Root of its parent, and the opened object must
// be the very one looked at. A link put in place after the look opens
// something else and is refused; what remains open to a program of the same
// user is moving the checked object itself between the two steps.
type dirHandle struct {
	r *os.Root
}

func openWorkspace(path string) (*dirHandle, error) {
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &dirHandle{r: r}, nil
}

func (d *dirHandle) close() error { return d.r.Close() }

func (d *dirHandle) stat() (fs.FileInfo, error) { return d.r.Stat(".") }

// plainEntry looks at name without following it.
func (d *dirHandle) plainEntry(name string) (fs.FileInfo, error) {
	info, err := d.r.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return nil, fmt.Errorf("%s is a link; write and edit do not follow links", name)
	}
	if a, ok := info.Sys().(*syscall.Win32FileAttributeData); ok && a.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, fmt.Errorf("%s is a link; write and edit do not follow links", name)
	}
	return info, nil
}

func (d *dirHandle) openDir(name string) (*dirHandle, error) {
	info, err := d.plainEntry(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", name)
	}
	r, err := d.r.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	if now, err := r.Stat("."); err != nil || !os.SameFile(now, info) {
		r.Close()
		return nil, fmt.Errorf("%s changed while it was opened", name)
	}
	return &dirHandle{r: r}, nil
}

func (d *dirHandle) mkdir(name string, perm fs.FileMode) error {
	if _, err := d.r.Lstat(name); err == nil {
		return nil
	}
	if err := d.r.Mkdir(name, perm); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

func (d *dirHandle) openFile(name string, flag int) (*os.File, error) {
	info, err := d.plainEntry(name)
	if err != nil && !(os.IsNotExist(err) && flag&os.O_CREATE != 0) {
		return nil, err
	}
	f, err := d.r.OpenFile(name, flag, 0o666)
	if err != nil {
		return nil, err
	}
	now, err := f.Stat()
	if err == nil && info != nil && !os.SameFile(now, info) {
		err = fmt.Errorf("%s changed while it was opened", name)
	}
	if err == nil && info == nil {
		// Created now: the name must not be a link put there meanwhile.
		var created fs.FileInfo
		if created, err = d.plainEntry(name); err == nil && !os.SameFile(now, created) {
			err = fmt.Errorf("%s changed while it was opened", name)
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func linkCount(f *os.File, _ fs.FileInfo) (uint64, error) {
	var d syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &d); err != nil {
		return 0, err
	}
	return uint64(d.NumberOfLinks), nil
}
