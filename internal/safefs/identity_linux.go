package safefs

import (
	"os"

	"golang.org/x/sys/unix"
)

// The creation time comes from statx, which reports it where the file system
// keeps one (ext4, XFS, Btrfs and others).
func identity(f *os.File) (Identity, error) {
	var st unix.Statx_t
	if err := unix.Statx(int(f.Fd()), "", unix.AT_EMPTY_PATH, unix.STATX_INO|unix.STATX_BTIME, &st); err != nil {
		return Identity{}, err
	}
	id := Identity{Dev: uint64(unix.Mkdev(st.Dev_major, st.Dev_minor)), Ino: st.Ino}
	if st.Mask&unix.STATX_BTIME != 0 {
		id.Birth, id.HasBirth = st.Btime.Sec*1e9+int64(st.Btime.Nsec), true
	}
	return id, nil
}
