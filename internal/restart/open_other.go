//go:build !unix

package restart

import "os"

// Windows has no pipes in a file's place to block an open.
const openNoBlock = 0

// linkCount is not checked where links cannot be counted from a FileInfo.
func linkCount(os.FileInfo) (uint64, error) { return 1, nil }

// syncDir: Windows makes a rename durable with the file.
func syncDir(*os.Root) {}
