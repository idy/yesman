//go:build windows

package schedule

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

var errLocked = errors.New("another yesman app holds the scheduled tasks")

// lockFile takes an exclusive lock on a file without waiting. The system
// drops it when the process ends, so a crash leaves no stale lock.
func lockFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	var ol windows.Overlapped
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, errLocked
		}
		return nil, err
	}
	return f, nil
}
