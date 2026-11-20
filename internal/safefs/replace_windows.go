//go:build windows

package safefs

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Windows supplies a write-through rename in place of directory fsync. Use
// the final path of the open directory so configured aliases stay out of the
// operation. Keep its handle open through the call and accept sibling names only.
func ReplaceSynced(root *os.Root, from, to string) error {
	if err := checkName(from); err != nil {
		return err
	}
	if err := checkName(to); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	h := windows.Handle(dir.Fd())
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return err
		}
		if n >= uint32(len(buf)) {
			buf = make([]uint16, n+1)
			continue
		}
		parent := windows.UTF16ToString(buf[:n])
		// Pin the observed directory against rename through this path-based
		// Windows API. Recheck its identity after acquiring the no-delete handle.
		pin, err := windows.CreateFile(&buf[0], windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			return err
		}
		guard := os.NewFile(uintptr(pin), parent)
		defer guard.Close()
		original, err := dir.Stat()
		if err != nil {
			return err
		}
		pinned, err := guard.Stat()
		if err != nil {
			return err
		}
		if !pinned.IsDir() || !os.SameFile(original, pinned) {
			return errors.New("metadata directory changed before publication")
		}
		src, err := windows.UTF16PtrFromString(filepath.Join(parent, from))
		if err != nil {
			return err
		}
		dst, err := windows.UTF16PtrFromString(filepath.Join(parent, to))
		if err != nil {
			return err
		}
		return windows.MoveFileEx(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
	}
}
