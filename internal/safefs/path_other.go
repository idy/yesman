//go:build !windows

package safefs

import "path/filepath"

// PhysicalPath resolves an existing storage path using the filesystem's identity.
func PhysicalPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
