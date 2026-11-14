//go:build !windows

package tasklog

import (
	"os"
	"syscall"
)

func openRead(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}

func lockFile(f *os.File) (func(), error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, err
}

func links(f *os.File) (uint64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return uint64(st.Sys().(*syscall.Stat_t).Nlink), nil
}
