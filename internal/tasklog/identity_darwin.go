package tasklog

import (
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
	return Identity{Platform: "darwin", Volume: fmt.Sprintf("%x", uint64(s.Dev)), ID: fmt.Sprintf("%x", s.Ino), Birth: fmt.Sprintf("%d:%d", s.Birthtimespec.Sec, s.Birthtimespec.Nsec)}, nil
}
