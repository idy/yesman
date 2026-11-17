//go:build !windows

package server

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// dirHandle is an open directory. Every name below it is opened relative to
// it with O_NOFOLLOW, one component at a time, so neither a link nor a
// component swapped for one is ever traversed: the kernel refuses it.
type dirHandle struct {
	f *os.File
}

// openWorkspace opens the workspace directory itself, which is trusted and
// may be spelled through an alias such as /var.
func openWorkspace(path string) (*dirHandle, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return &dirHandle{f: os.NewFile(uintptr(fd), path)}, nil
}

func (d *dirHandle) close() error { return d.f.Close() }

func (d *dirHandle) stat() (fs.FileInfo, error) { return d.f.Stat() }

// openDir opens the plain directory name inside d.
func (d *dirHandle) openDir(name string) (*dirHandle, error) {
	fd, err := unix.Openat(int(d.f.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, linkError(name, err)
	}
	return &dirHandle{f: os.NewFile(uintptr(fd), name)}, nil
}

// mkdir makes the directory name inside d unless it exists.
func (d *dirHandle) mkdir(name string, perm fs.FileMode) error {
	err := unix.Mkdirat(int(d.f.Fd()), name, uint32(perm.Perm()))
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return &fs.PathError{Op: "mkdir", Path: name, Err: err}
	}
	return nil
}

// openFile opens name inside d without following a link and without
// waiting on a FIFO.
func (d *dirHandle) openFile(name string, flag int) (*os.File, error) {
	fd, err := unix.Openat(int(d.f.Fd()), name, flag|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o666)
	if err != nil {
		return nil, linkError(name, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

// linkError names the refusal of a link: O_NOFOLLOW reports it as ELOOP
// (EMLINK on some BSDs), O_DIRECTORY on a link to a file as ENOTDIR.
func linkError(name string, err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK) {
		return fmt.Errorf("%s is a link; write and edit do not follow links", name)
	}
	return &fs.PathError{Op: "open", Path: name, Err: err}
}

func linkCount(_ *os.File, info fs.FileInfo) (uint64, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("cannot tell the file's link count")
	}
	return uint64(st.Nlink), nil
}
