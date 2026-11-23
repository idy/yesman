//go:build windows

package pool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func visitDesktopOpenFiles(ctx context.Context, _ func(string) bool) error { return ctx.Err() }

// Ask NTFS whether an existing handle prevents deletion, before unlinking
// any files. Use the open parent and open the entry itself, including links.
// The check is read-only and sharing is enforced again at actual removal.
func checkDiscardSharing(parent *os.File, name string) error {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory: windows.Handle(parent.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.DELETE|windows.SYNCHRONIZE, &attributes, &status, nil,
		windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN, windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err == nil {
		return windows.CloseHandle(handle)
	}
	var nt windows.NTStatus
	if errors.As(err, &nt) && nt.Errno() == windows.ERROR_SHARING_VIOLATION {
		return &RemovalHold{Reason: "A process is using this worktree.", Detail: filepath.Join(parent.Name(), name)}
	}
	return err
}
