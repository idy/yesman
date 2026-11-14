package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/idy/yesman/internal/yesmen"
)

// The scheduler's files live in <workspace>/.yesman/schedules. Every read,
// write and removal goes through handles opened from the workspace itself,
// never through a path: os.Root keeps each step inside the folder it was
// opened from, and each folder and file on the way is checked to be a real
// folder or regular file, not a link, on the handle that is then used. A link
// put anywhere on the way, .yesman and schedules included, or swapped in
// while a file is opened, is refused and left as it is.

// errUnsafePath reports a path yesman will not read or write through.
type errUnsafePath struct{ path string }

func (e errUnsafePath) Error() string {
	return fmt.Sprintf("%s is a link or not a regular file or folder, so yesman left it unchanged", e.path)
}

// openSchedules opens a workspace's schedules folder. When create is false
// and a folder on the way is missing, the error is fs.ErrNotExist.
func openSchedules(y yesmen.Yesman, create bool) (*os.Root, error) {
	ws, err := os.OpenRoot(y.Path)
	if err != nil {
		return nil, err
	}
	defer ws.Close()
	meta, err := openDir(ws, ".yesman", create)
	if err != nil {
		return nil, err
	}
	defer meta.Close()
	return openDir(meta, "schedules", create)
}

// openRuns opens the runs folder of a schedules folder.
func openRuns(dir *os.Root, create bool) (*os.Root, error) { return openDir(dir, "runs", create) }

// openDir opens the folder name in parent: a real folder, not a link,
// created when create and missing. The folder opened is checked to be the
// one found at that name, before and after it was opened, so a link swapped
// in meanwhile is refused too.
func openDir(parent *os.Root, name string, create bool) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := parent.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		info, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, errUnsafePath{name}
	}
	sub, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := sub.Stat(".")
	if err == nil && !os.SameFile(info, opened) {
		err = errUnsafePath{name}
	}
	if err == nil {
		if again, aerr := parent.Lstat(name); aerr != nil || again.Mode()&fs.ModeSymlink != 0 || !os.SameFile(again, opened) {
			err = errUnsafePath{name}
		}
	}
	if err != nil {
		sub.Close()
		return nil, err
	}
	return sub, nil
}

// stampIn describes a file, to tell when it changed.
func stampIn(dir *os.Root, name string) fileStamp {
	if dir == nil {
		return fileStamp{}
	}
	info, err := dir.Lstat(name)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{size: info.Size(), mod: info.ModTime(), ok: true}
}

// readIn reads a regular file of at most limit bytes. A missing file reads
// as nil. The file read is checked, on its handle, to be the regular file
// found at that name.
func readIn(dir *os.Root, name string, limit int64) ([]byte, error) {
	info, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errUnsafePath{name}
	}
	f, err := dir.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if opened, err := f.Stat(); err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errUnsafePath{name}
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d KiB, so yesman left it unchanged", name, limit>>10)
	}
	return data, nil
}

// writeIn replaces a file atomically, unless something other than a regular
// file is at its name.
func writeIn(dir *os.Root, name string, data []byte) error {
	if info, err := dir.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return errUnsafePath{name}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmp := name + "." + randomHex(4) + ".tmp"
	f, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = dir.Rename(tmp, name)
	}
	if err != nil {
		_ = dir.Remove(tmp)
	}
	return err
}

// errClaimed: the record already exists, made by another tick, process or
// click.
var errClaimed = errors.New("already claimed")

// createIn creates a file that must not exist yet: the claim of a run. A
// link at its name exists, and is not followed.
func createIn(dir *os.Root, name string, data []byte) error {
	f, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return errClaimed
	}
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// The claim was ours; without its content it must not stand. If it
		// cannot be removed, it reads as damaged and its occurrence as handled.
		_ = dir.Remove(name)
	}
	return err
}

// removeIn removes a file of the folder; a link there is removed, not
// followed.
func removeIn(dir *os.Root, name string) error {
	err := dir.Remove(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// readRunsIn reads every run record of a schedules folder. A record that
// cannot be read stays on disk and reads as damaged: its occurrence counts
// as handled and it is never sent. A missing folder holds none.
func readRunsIn(dir *os.Root) (map[string]*record, error) {
	runs := map[string]*record{}
	if dir == nil {
		return runs, nil
	}
	rd, err := openRuns(dir, false)
	if errors.Is(err, fs.ErrNotExist) {
		return runs, nil
	}
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	f, err := rd.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !runIDRE.MatchString(name) {
			continue // temporary files and anything else
		}
		runs[name] = readRecordIn(rd, name)
	}
	return runs, nil
}

// readRecordIn reads one run record, damaged when it cannot be read.
func readRecordIn(rd *os.Root, name string) *record {
	plan, _, _ := strings.Cut(name, "-")
	data, err := readIn(rd, name+".json", maxRunFile)
	r := &record{}
	if err == nil && data == nil {
		err = fs.ErrNotExist
	}
	if err == nil {
		err = json.Unmarshal(data, &r.Run)
	}
	if err == nil && (r.ID != name || r.Plan != plan) {
		err = errors.New("its ID does not match its file")
	}
	if err == nil {
		r.extra, err = unknown(data, runKeys)
	}
	if err != nil {
		r = &record{Run: Run{ID: name, Plan: plan, Kind: RunScheduled, State: StateDamaged, Reason: "This record cannot be read: " + clip(err.Error(), 200)}}
	}
	return r
}
