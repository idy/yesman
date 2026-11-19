package yesmen

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

var reopenReceiptDir = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

// FlushFileBuffers needs a write-capable handle. ReOpenFile upgrades access
// on this directory object without looking up its path again. Filesystems or
// ACLs that refuse the flush leave the archive in place.
func flushReceiptDir(dir *os.File) error {
	h, _, err := reopenReceiptDir.Call(dir.Fd(), windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_FLAG_BACKUP_SEMANTICS)
	if windows.Handle(h) == windows.InvalidHandle {
		return os.NewSyscallError("ReOpenFile(directory)", err)
	}
	handle := windows.Handle(h)
	return errors.Join(os.NewSyscallError("FlushFileBuffers(directory)", windows.FlushFileBuffers(handle)),
		os.NewSyscallError("CloseHandle(directory)", windows.CloseHandle(handle)))
}
