//go:build !windows

package usage

import (
	"os"
	"syscall"
)

// openFlags open a log for reading without waiting, should a named pipe
// have taken its place.
const openFlags = os.O_RDONLY | syscall.O_NONBLOCK

// hardLinks counts the links to an open file.
func hardLinks(_ *os.File, info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true
}
