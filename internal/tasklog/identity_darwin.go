package tasklog

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func fileIdentity(f *os.File) (Identity, error) {
	st, err := f.Stat()
	if err != nil {
		return Identity{}, err
	}
	s := st.Sys().(*syscall.Stat_t)
	identity := Identity{Platform: "darwin", Volume: fmt.Sprintf("%x", uint64(s.Dev)), ID: fmt.Sprintf("%x", s.Ino), Birth: fmt.Sprintf("%d:%d", s.Birthtimespec.Sec, s.Birthtimespec.Nsec)}
	if !identity.valid() {
		return Identity{}, errors.New("filesystem cannot prove stable Darwin source birth identity; archive explicitly instead")
	}
	return identity, nil
}
