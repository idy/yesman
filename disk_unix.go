//go:build darwin || linux

package main

import "golang.org/x/sys/unix"

// diskSpace is the size of the disk that holds path and the space free to
// use there, as an unprivileged user may.
func diskSpace(path string) (DiskSpace, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return DiskSpace{}, err
	}
	block := int64(st.Bsize)
	return DiskSpace{Total: int64(st.Blocks) * block, Free: int64(st.Bavail) * block}, nil
}
