package tasklog

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"syscall"
)

func fileIdentity(f *os.File) (Identity, error) {
	st, err := f.Stat()
	if err != nil {
		return Identity{}, err
	}
	s := st.Sys().(*syscall.Stat_t)
	var x unix.Statx_t
	if err = unix.Statx(int(f.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME|unix.STATX_INO, &x); err != nil {
		return Identity{}, fmt.Errorf("stable source birth identity unavailable: %w", err)
	}
	if x.Mask&unix.STATX_BTIME == 0 || x.Ino != s.Ino {
		return Identity{}, errors.New("filesystem cannot prove stable source birth identity; archive explicitly instead")
	}
	return Identity{Platform: "linux", Volume: fmt.Sprintf("%x", uint64(s.Dev)), ID: fmt.Sprintf("%x", s.Ino), Birth: fmt.Sprintf("%d:%d", x.Btime.Sec, x.Btime.Nsec)}, nil
}
