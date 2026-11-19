//go:build !windows

package yesmen

import "os"

// Sync directory entries through the opened directory itself.
func flushReceiptDir(dir *os.File) error { return dir.Sync() }
