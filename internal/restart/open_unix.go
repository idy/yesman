//go:build unix

package restart

import (
	"errors"
	"os"
	"syscall"
)

// openNoBlock keeps a pipe swapped in for the file from blocking its open.
const openNoBlock = syscall.O_NONBLOCK

// linkCount is the number of hard links of an open file.
func linkCount(fi os.FileInfo) (uint64, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("its links cannot be counted")
	}
	return uint64(st.Nlink), nil
}

// syncDir makes a rename in the directory durable.
func syncDir(r *os.Root) {
	if d, err := r.Open("."); err == nil {
		_ = d.Sync()
		d.Close()
	}
}
