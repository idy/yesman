package main

import "golang.org/x/sys/unix"

// diskSpace is the size of the disk that holds path and the space free to
// use there, as an unprivileged user may. On Linux statfs counts blocks in
// Frsize, the fragment size, which NFS, FUSE and others set apart from
// Bsize, the preferred transfer size; kernels that leave it unset count in
// Bsize.
func diskSpace(path string) (DiskSpace, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return DiskSpace{}, err
	}
	block := int64(st.Frsize)
	if block == 0 {
		block = int64(st.Bsize)
	}
	return DiskSpace{Total: int64(st.Blocks) * block, Free: int64(st.Bavail) * block}, nil
}
