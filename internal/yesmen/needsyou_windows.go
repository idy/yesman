package yesmen

import (
	"os"
	"syscall"
)

// openNoBlock is not needed: a pipe cannot take a file's name on Windows.
const openNoBlock = 0

// linkCount is the number of hard links of an open file.
func linkCount(f *os.File, _ os.FileInfo) (uint64, error) {
	var d syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &d); err != nil {
		return 0, err
	}
	return uint64(d.NumberOfLinks), nil
}
