//go:build !windows

package safefs

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// Dir is an open directory. Names below it are opened with openat and
// O_NOFOLLOW, so the kernel refuses a link at that name.
type Dir struct {
	f *os.File
}

// Open opens the directory at path, a trusted location such as a workspace
// or the pool, which may itself be reached through a link (/var on macOS).
func Open(path string) (*Dir, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return &Dir{f: os.NewFile(uintptr(fd), path)}, nil
}

func (d *Dir) Close() error { return d.f.Close() }

// Stat describes the directory d is open on.
func (d *Dir) Stat() (fs.FileInfo, error) { return d.f.Stat() }

// Dup returns a second handle of the same directory.
func (d *Dir) Dup() (*Dir, error) {
	fd, err := unix.FcntlInt(d.f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &Dir{f: os.NewFile(uintptr(fd), d.f.Name())}, nil
}

// OpenDir opens the directory name inside d; a link there is refused.
func (d *Dir) OpenDir(name string) (*Dir, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(d.f.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, openError(name, err)
	}
	return &Dir{f: os.NewFile(uintptr(fd), name)}, nil
}

// Mkdir makes the directory name inside d unless the name exists.
func (d *Dir) Mkdir(name string, perm os.FileMode) error {
	if err := checkName(name); err != nil {
		return err
	}
	err := unix.Mkdirat(int(d.f.Fd()), name, uint32(perm.Perm()))
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return &fs.PathError{Op: "mkdir", Path: name, Err: err}
	}
	return nil
}

// OpenFile opens the file name inside d with flag (os.O_RDONLY, O_WRONLY or
// O_RDWR, with O_CREATE and O_EXCL), never following a link, never waiting
// on a FIFO and never truncating.
func (d *Dir) OpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	sys := unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	switch flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_WRONLY:
		sys |= unix.O_WRONLY
	case os.O_RDWR:
		sys |= unix.O_RDWR
	}
	if flag&os.O_CREATE != 0 {
		sys |= unix.O_CREAT
	}
	if flag&os.O_EXCL != 0 {
		sys |= unix.O_EXCL
	}
	fd, err := unix.Openat(int(d.f.Fd()), name, sys, uint32(perm.Perm()))
	if err != nil {
		return nil, openError(name, err)
	}
	f := os.NewFile(uintptr(fd), name)
	if info, err := f.Stat(); err != nil || info.IsDir() {
		f.Close()
		if err == nil {
			err = &fs.PathError{Op: "open", Path: name, Err: syscall.EISDIR}
		}
		return nil, err
	}
	return f, nil
}

// Identity identifies the directory d is open on.
func (d *Dir) Identity() (Identity, error) {
	return identity(d.f)
}

// O_NOFOLLOW reports a link as ELOOP (EMLINK on some BSDs); O_DIRECTORY on
// a link to a file as ENOTDIR.
func openError(name string, err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK) {
		return linkError(name)
	}
	return &fs.PathError{Op: "open", Path: name, Err: err}
}
