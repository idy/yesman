//go:build !windows

package schedule

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var errLocked = errors.New("another yesman app holds the scheduled tasks")

// lockFile takes an exclusive advisory lock on a file without waiting. The
// system drops it when the process ends, so a crash leaves no stale lock.
func lockFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return f, nil
}
