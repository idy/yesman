//go:build !windows

package server

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// nonblockFlag keeps opening a FIFO from waiting for its other end.
const nonblockFlag = syscall.O_NONBLOCK

func linkCount(_ *os.File, info fs.FileInfo) (uint64, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("cannot tell the file's link count")
	}
	return uint64(st.Nlink), nil
}
