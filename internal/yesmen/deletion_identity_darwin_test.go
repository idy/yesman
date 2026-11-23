package yesmen

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"syscall"
	"testing"

	"github.com/idy/yesman/internal/safefs"
)

type darwinDeletionFileInfo struct {
	fs.FileInfo
	stat syscall.Stat_t
}

func (info darwinDeletionFileInfo) Sys() any { return &info.stat }

func TestDarwinDeletionEntryDeviceID(t *testing.T) {
	st, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("Darwin stat metadata unavailable")
	}
	raw := *stat
	const highBitDevice int32 = -2147483637
	// A directory fingerprint encodes the inode, birth time and entry type.
	var encoded [20]byte
	binary.LittleEndian.PutUint64(encoded[0:8], raw.Ino)
	binary.LittleEndian.PutUint64(encoded[8:16], uint64(raw.Birthtimespec.Sec*1e9+raw.Birthtimespec.Nsec))
	binary.LittleEndian.PutUint32(encoded[16:20], uint32(fs.ModeDir))
	wantFingerprint := sha256.Sum256(encoded[:])
	for _, tc := range []struct {
		name                      string
		entryDevice, volumeDevice int32
	}{
		{"matching normal device", 11, 11},
		{"matching high-bit device", highBitDevice, highBitDevice},
		{"different devices", highBitDevice, 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := darwinDeletionFileInfo{FileInfo: st, stat: raw}
			entry.stat.Dev = tc.entryDevice
			// Darwin SafeFS converts the signed device ID directly to uint64.
			volume := safefs.Identity{Dev: uint64(tc.volumeDevice)}
			fingerprint, err := deletionFingerprint(nil, nil, ".", entry, volume, false)
			if tc.entryDevice != tc.volumeDevice {
				if !errors.Is(err, errCheckoutPreviewChanged) {
					t.Fatalf("different devices must be refused, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("matching device %#x was refused: %v", volume.Dev, err)
			}
			if fingerprint != wantFingerprint {
				t.Fatal("the device ID changed the persisted fingerprint")
			}
		})
	}
}
