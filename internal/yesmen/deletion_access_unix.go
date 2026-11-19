//go:build darwin || linux

package yesmen

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Go removes owner-write on downloaded module directories. After the checks,
// restore that one bit on the verified directory handle in the app's cache.
// The surrounding walk already checked its mount and repository markers.
func deletionDirectoryAccess(dir *os.Root, rel string, adjust bool) error {
	f, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Faccessat(int(f.Fd()), ".", unix.W_OK|unix.X_OK, 0); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	const cache = ".yesman/tmp/_cache/go-mod/"
	st, err := f.Stat()
	if err != nil {
		return err
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !strings.HasPrefix(rel, cache) || !st.IsDir() || !ok || uint32(os.Geteuid()) != stat.Uid || st.Mode().Perm()&0700 != 0500 {
		return deletionIssue("Grant write access to directory %s or move its contents to safety before deleting the workspace.", rel)
	}
	if adjust {
		err := f.Chmod(st.Mode() | 0200)
		if err == nil {
			err = unix.Faccessat(int(f.Fd()), ".", unix.W_OK|unix.X_OK, 0)
		}
		if err != nil {
			return &DeletionIssue{Message: "Grant write access to directory %s or move its contents to safety before deleting the workspace.", Args: []any{rel}, Cause: err}
		}
	}
	return nil
}
