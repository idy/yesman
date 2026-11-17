package safefs

import (
	"errors"
	"os"
	"syscall"
)

// The creation time is st_birthtimespec; where the file system keeps none it
// is {0, 0}, which reports no creation time (HasBirth false).
func identity(f *os.File) (Identity, error) {
	info, err := f.Stat()
	if err != nil {
		return Identity{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, errors.New("cannot tell the file's identity")
	}
	birth, ok := birthFromTimespec(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
	return Identity{Dev: uint64(st.Dev), Ino: st.Ino, Birth: birth, HasBirth: ok}, nil
}
