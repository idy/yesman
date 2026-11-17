//go:build !unix && !windows

package yesmen

import "os"

// fileLinks: these systems have no hard links yesman can see.
func fileLinks(*os.File) (uint64, error) { return 1, nil }
