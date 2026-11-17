package safefs

import (
	"errors"
	"os"
	"syscall"
)

func identity(f *os.File) (Identity, error) {
	info, err := f.Stat()
	if err != nil {
		return Identity{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, errors.New("cannot tell the file's identity")
	}
	return Identity{Dev: uint64(st.Dev), Ino: st.Ino, Birth: st.Birthtimespec.Nano(), HasBirth: true}, nil
}
