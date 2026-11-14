//go:build unix

package yesmen

import (
	"errors"
	"os"
	"syscall"
)

// fileLinks is the number of names an open file has.
func fileLinks(f *os.File) (uint64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("the file's link count is unknown")
	}
	return uint64(st.Nlink), nil
}
