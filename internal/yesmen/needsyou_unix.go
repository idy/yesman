//go:build unix

package yesmen

import (
	"errors"
	"os"
	"syscall"
)

// openNoBlock keeps a pipe swapped in for the file from blocking its open.
const openNoBlock = syscall.O_NONBLOCK

// linkCount is the number of hard links of an open file.
func linkCount(_ *os.File, fi os.FileInfo) (uint64, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("its links cannot be counted")
	}
	return uint64(st.Nlink), nil
}
