package yesmen

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// NTFS persists the directory structure on a directory flush (MS-FSA 2.1.5.7).
// CreateFileW can obtain the required access on ordinary writable directories.
// Resolve the pathname from the pinned handle, then compare both live objects
// before flushing. OPEN_EXISTING never creates or truncates an entry.
func flushReceiptDir(dir *os.File) error {
	pinned := windows.Handle(dir.Fd())
	var fsName [32]uint16
	if err := windows.GetVolumeInformationByHandle(pinned, nil, 0, nil, nil, nil, &fsName[0], uint32(len(fsName))); err != nil {
		return os.NewSyscallError("GetVolumeInformationByHandleW(directory)", err)
	}
	if fs := windows.UTF16ToString(fsName[:]); !strings.EqualFold(fs, "NTFS") {
		return fmt.Errorf("worktree relocation receipts require NTFS directory flushes on Windows (filesystem %q)", fs)
	}
	// The extended-length Windows path limit includes the terminating NUL.
	path := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(pinned, &path[0], uint32(len(path)), 0)
	if err != nil {
		return os.NewSyscallError("GetFinalPathNameByHandleW(directory)", err)
	}
	if n == 0 || n >= uint32(len(path)) {
		return errors.New("the relocation directory path exceeds the Windows path limit")
	}
	return flushSameReceiptDir(pinned, &path[0])
}

func flushSameReceiptDir(pinned windows.Handle, path *uint16) (err error) {
	h, err := windows.CreateFile(path, windows.GENERIC_WRITE|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return os.NewSyscallError("CreateFileW(relocation directory)", err)
	}
	defer func() {
		err = errors.Join(err, os.NewSyscallError("CloseHandle(relocation directory)", windows.CloseHandle(h)))
	}()
	var before, opened windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(pinned, &before); err != nil {
		return os.NewSyscallError("GetFileInformationByHandle(pinned directory)", err)
	}
	if err := windows.GetFileInformationByHandle(h, &opened); err != nil {
		return os.NewSyscallError("GetFileInformationByHandle(relocation directory)", err)
	}
	const kinds = windows.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_REPARSE_POINT
	if opened.FileAttributes&kinds != windows.FILE_ATTRIBUTE_DIRECTORY || before.FileAttributes&kinds != windows.FILE_ATTRIBUTE_DIRECTORY ||
		opened.FileIndexHigh|opened.FileIndexLow == 0 || before.VolumeSerialNumber != opened.VolumeSerialNumber ||
		before.FileIndexHigh != opened.FileIndexHigh || before.FileIndexLow != opened.FileIndexLow || before.CreationTime != opened.CreationTime {
		return errors.New("the worktree relocation directory changed before synchronization")
	}
	return os.NewSyscallError("FlushFileBuffers(relocation directory)", windows.FlushFileBuffers(h))
}
