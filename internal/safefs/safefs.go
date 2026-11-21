// Package safefs opens files and directories one name at a time, relative to
// an already open directory, never following a link or other reparse point.
// The managed write entry (internal/server/files.go) and the pool's reads of
// worktree registration files (internal/pool/instance.go) use it.
//
// Each name is a single component; a name that is a link, a junction or any
// other reparse point is refused by the operating system's open of that
// name relative to the parent's handle, not by a separate look beforehand.
// What remains possible, as for any program of the same user, is changing
// the files themselves; this package does not confine other programs.
package safefs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// Identity identifies a directory or file on its file system.
//
// Dev is the file system the object is on now. It is compared only with
// another object's Dev at the same time: macOS numbers devices when they are
// mounted, so it is not kept. Ino is the inode number (the NTFS file index,
// which includes a sequence number, on Windows) and Birth the creation time
// in nanoseconds, when the file system reports one (HasBirth).
type Identity struct {
	Dev      uint64
	Ino      uint64
	Birth    int64
	HasBirth bool
	Mount    uint64 // Linux mount ID, to distinguish bind mounts on the same device
}

// FileIdentity measures an already open file or directory.
func FileIdentity(f *os.File) (Identity, error) { return identity(f) }

// ErrLink reports a name that is a link, junction or other reparse point.
var ErrLink = errors.New("is a link; write and edit do not follow links")

func checkName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || (runtime.GOOS == "windows" && strings.ContainsRune(name, ':')) {
		return fmt.Errorf("%q is not a single file name", name)
	}
	return nil
}

func linkError(name string) error {
	return fmt.Errorf("%s %w", name, ErrLink)
}

// OpenPath opens the directories names below d, one at a time, without
// following links. With perm, a missing one is made. The result is a new
// handle the caller closes; d stays open.
func OpenPath(d *Dir, names []string, perm *os.FileMode) (*Dir, error) {
	cur, err := d.Dup()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if perm != nil {
			if err := cur.Mkdir(name, *perm); err != nil {
				cur.Close()
				return nil, err
			}
		}
		next, err := cur.OpenDir(name)
		cur.Close()
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}

// ReadSmall reads the regular file name in d, without following a link or
// waiting on a FIFO, refusing one larger than limit.
func ReadSmall(d *Dir, name string, limit int64) ([]byte, error) {
	f, err := d.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("%s is larger than expected", name)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than expected", name)
	}
	return data, nil
}
