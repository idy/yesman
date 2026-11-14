//go:build windows

package usage

import (
	"os"
	"syscall"
)

const openFlags = os.O_RDONLY

// hardLinks counts the links to an open file.
func hardLinks(f *os.File, _ os.FileInfo) (uint64, bool) {
	var d syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &d); err != nil {
		return 0, false
	}
	return uint64(d.NumberOfLinks), true
}
