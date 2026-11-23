package yesmen

import (
	"io/fs"
	"os"
	"syscall"

	"github.com/idy/yesman/internal/safefs"
	"golang.org/x/sys/unix"
)

func deletionEntryID(dir *os.File, name string, st fs.FileInfo, volume safefs.Identity) (safefs.Identity, error) {
	raw, ok := st.Sys().(*syscall.Stat_t)
	if !ok || raw.Ino == 0 || raw.Dev != volume.Dev {
		return safefs.Identity{}, errCheckoutPreviewChanged
	}
	var now unix.Statx_t
	const flags = unix.STATX_INO | unix.STATX_BTIME | unix.STATX_MNT_ID
	if err := unix.Statx(int(dir.Fd()), name, unix.AT_SYMLINK_NOFOLLOW|unix.AT_STATX_DONT_SYNC, flags, &now); err != nil || now.Mask&flags != flags || now.Ino != raw.Ino || now.Mnt_id != volume.Mount {
		return safefs.Identity{}, errCheckoutPreviewChanged
	}
	return safefs.Identity{Ino: now.Ino, Birth: now.Btime.Sec*1e9 + int64(now.Btime.Nsec)}, nil
}
