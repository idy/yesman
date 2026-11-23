package yesmen

import (
	"io/fs"
	"os"
	"syscall"

	"github.com/idy/yesman/internal/safefs"
)

func deletionEntryID(_ *os.File, _ string, st fs.FileInfo, volume safefs.Identity) (safefs.Identity, error) {
	raw, ok := st.Sys().(*syscall.Stat_t)
	// Match the signed-to-uint64 conversion in Darwin safefs.FileIdentity.
	if !ok || uint64(raw.Dev) != volume.Dev || raw.Ino == 0 {
		return safefs.Identity{}, errCheckoutPreviewChanged
	}
	return safefs.Identity{Ino: raw.Ino, Birth: raw.Birthtimespec.Sec*1e9 + raw.Birthtimespec.Nsec}, nil
}
