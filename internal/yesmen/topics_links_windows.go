//go:build windows

package yesmen

import (
	"os"
	"syscall"
)

// fileLinks is the number of names an open file has.
func fileLinks(f *os.File) (uint64, error) {
	var d syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &d); err != nil {
		return 0, err
	}
	return uint64(d.NumberOfLinks), nil
}
