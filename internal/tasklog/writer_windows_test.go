package tasklog

import (
	"golang.org/x/sys/windows"
	"os"
)

// Hold a real Windows writer through source unlink. Ordinary os.OpenFile's
// sharing contract can prevent that mutation instead; this fixture explicitly
// requests delete sharing so the old-descriptor late-append assertion executes.
func openLateWriter(name string) (*os.File, error) {
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(path, windows.FILE_APPEND_DATA|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), name), nil
}
