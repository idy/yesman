//go:build !darwin && !linux && !windows

package main

import "errors"

// diskSpace is not asked of this system; Storage leaves the disk out.
func diskSpace(string) (DiskSpace, error) {
	return DiskSpace{}, errors.ErrUnsupported
}
