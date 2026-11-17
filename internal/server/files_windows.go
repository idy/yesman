//go:build windows

package server

import (
	"io/fs"
	"os"
	"syscall"
)

func linkCount(f *os.File, _ fs.FileInfo) (uint64, error) {
	var d syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &d); err != nil {
		return 0, err
	}
	return uint64(d.NumberOfLinks), nil
}
