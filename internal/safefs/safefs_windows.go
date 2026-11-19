//go:build windows

package safefs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Dir is an open directory. Names below it are opened with NtCreateFile
// relative to its handle (OBJECT_ATTRIBUTES.RootDirectory), with
// OBJ_DONT_REPARSE, which fails the open if the name is a reparse point, and
// FILE_OPEN_REPARSE_POINT, which would open a reparse point itself rather
// than its target. The handle that comes back is then checked: it must not
// carry FILE_ATTRIBUTE_REPARSE_POINT and must be a directory or a file as
// asked. Symbolic links, junctions and every other reparse point are refused
// by that open, not by a separate look at the name.
type Dir struct {
	h    windows.Handle
	name string
}

const dirAccess = windows.FILE_LIST_DIRECTORY | windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE

// Open opens the directory at path, a trusted location such as a workspace
// or the pool, which may itself be reached through a link.
func Open(path string) (*Dir, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, dirAccess, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		windows.CloseHandle(h)
		if err == nil {
			err = fmt.Errorf("%s is not a directory", path)
		}
		return nil, err
	}
	return &Dir{h: h, name: path}, nil
}

func (d *Dir) Close() error { return windows.CloseHandle(d.h) }

// Stat describes the directory d is open on.
func (d *Dir) Stat() (fs.FileInfo, error) {
	dup, err := d.Dup()
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dup.h), d.name)
	defer f.Close()
	return f.Stat()
}

// Dup returns a second handle of the same directory.
func (d *Dir) Dup() (*Dir, error) {
	p := windows.CurrentProcess()
	var h windows.Handle
	if err := windows.DuplicateHandle(p, d.h, p, &h, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, err
	}
	return &Dir{h: h, name: d.name}, nil
}

// open opens name relative to d.
func (d *Dir) open(name string, access, disposition, options uint32) (windows.Handle, error) {
	if err := checkName(name); err != nil {
		return 0, err
	}
	objName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	oa := windows.OBJECT_ATTRIBUTES{
		RootDirectory: d.h,
		ObjectName:    objName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, access|windows.SYNCHRONIZE, &oa, &iosb, nil, windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, disposition,
		options|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		return 0, statusError(name, err)
	}
	return h, nil
}

// check refuses a handle that is a reparse point or not of the kind asked.
func check(h windows.Handle, name string, dir bool) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return linkError(name)
	}
	if isDir := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0; isDir != dir {
		if dir {
			return fmt.Errorf("%s is not a directory", name)
		}
		return fmt.Errorf("%s is a directory", name)
	}
	return nil
}

// OpenDir opens the directory name inside d; a reparse point there is refused.
func (d *Dir) OpenDir(name string) (*Dir, error) {
	h, err := d.open(name, dirAccess, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE)
	if err != nil {
		return nil, err
	}
	if err := check(h, name, true); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return &Dir{h: h, name: name}, nil
}

// Mkdir makes the directory name inside d unless the name exists. perm is
// not used on Windows.
func (d *Dir) Mkdir(name string, _ os.FileMode) error {
	h, err := d.open(name, dirAccess, windows.FILE_CREATE, windows.FILE_DIRECTORY_FILE)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return windows.CloseHandle(h)
}

// OpenFile opens the file name inside d with flag (os.O_RDONLY, O_WRONLY or
// O_RDWR, with O_CREATE and O_EXCL), never following a reparse point and
// never truncating. perm is not used on Windows.
func (d *Dir) OpenFile(name string, flag int, _ os.FileMode) (*os.File, error) {
	access := uint32(windows.FILE_GENERIC_READ)
	switch flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_WRONLY:
		access = windows.FILE_GENERIC_WRITE
	case os.O_RDWR:
		access = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE
	}
	disposition := uint32(windows.FILE_OPEN)
	switch {
	case flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0:
		disposition = windows.FILE_CREATE
	case flag&os.O_CREATE != 0:
		disposition = windows.FILE_OPEN_IF
	}
	// Reading attributes is needed for the check below and for File.Stat,
	// and is not part of FILE_GENERIC_WRITE.
	access |= windows.FILE_READ_ATTRIBUTES
	h, err := d.open(name, access, disposition, windows.FILE_NON_DIRECTORY_FILE)
	if err != nil {
		return nil, err
	}
	if err := check(h, name, false); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), name), nil
}

// Identity identifies the directory d is open on: its volume, its NTFS file
// index (which carries a sequence number) and its creation time, if the
// file system keeps one.
func (d *Dir) Identity() (Identity, error) {
	return handleIdentity(d.h)
}

func identity(f *os.File) (Identity, error) { return handleIdentity(windows.Handle(f.Fd())) }

func handleIdentity(h windows.Handle) (Identity, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return Identity{}, err
	}
	birth, ok := birthFromFiletime(info.CreationTime.LowDateTime, info.CreationTime.HighDateTime)
	return Identity{
		Dev:      uint64(info.VolumeSerialNumber),
		Ino:      uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
		Birth:    birth,
		HasBirth: ok,
	}, nil
}

func statusError(name string, err error) error {
	st, ok := err.(windows.NTStatus)
	if !ok {
		return &fs.PathError{Op: "open", Path: name, Err: err}
	}
	switch st {
	case windows.STATUS_REPARSE_POINT_ENCOUNTERED, windows.STATUS_STOPPED_ON_SYMLINK:
		return linkError(name)
	case windows.STATUS_OBJECT_NAME_COLLISION:
		return &fs.PathError{Op: "open", Path: name, Err: fs.ErrExist}
	}
	return &fs.PathError{Op: "open", Path: name, Err: st.Errno()}
}
