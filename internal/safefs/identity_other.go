//go:build !windows && !darwin && !linux

package safefs

import (
	"errors"
	"os"
	"syscall"
)

// Elsewhere no creation time is read: HasBirth stays false.
func identity(f *os.File) (Identity, error) {
	info, err := f.Stat()
	if err != nil {
		return Identity{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, errors.New("cannot tell the file's identity")
	}
	return Identity{Dev: uint64(st.Dev), Ino: uint64(st.Ino)}, nil
}
