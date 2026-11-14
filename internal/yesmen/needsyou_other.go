//go:build !unix && !windows

package yesmen

import (
	"errors"
	"os"
)

const openNoBlock = 0

// linkCount cannot be checked here, so the file is not trusted.
func linkCount(*os.File, os.FileInfo) (uint64, error) {
	return 0, errors.New("its links cannot be counted on this system")
}
