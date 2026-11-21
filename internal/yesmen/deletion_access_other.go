//go:build !darwin && !linux

package yesmen

import "os"

// Windows removes read-only entries through the rooted unlink operation.
func deletionDirectoryAccess(*os.Root, string, bool) error { return nil }
