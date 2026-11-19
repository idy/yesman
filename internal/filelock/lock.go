// Package filelock provides nonblocking lifetime locks shared by Yes Man processes.
package filelock

import (
	"errors"
	"os"
	"path/filepath"
)

var ErrLocked = errors.New("another Yes Man process holds the lock")

// Try takes an exclusive OS lock. Close the file to release it. The kernel
// also releases it on process exit. Keep the file in place across owners.
func Try(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lock(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
