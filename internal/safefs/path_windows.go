package safefs

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// PhysicalPath resolves the object reached through a path, including Windows
// directory junctions that EvalSymlinks can leave in the name.
func PhysicalPath(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", err
		}
		if n >= uint32(len(buf)) {
			buf = make([]uint16, n+1)
			continue
		}
		resolved := windows.UTF16ToString(buf[:n])
		if unc, ok := strings.CutPrefix(resolved, `\\?\UNC\`); ok {
			return `\\` + unc, nil
		}
		if drive, ok := strings.CutPrefix(resolved, `\\?\`); ok && len(drive) >= 2 && drive[1] == ':' {
			return drive, nil
		}
		return resolved, nil
	}
}
