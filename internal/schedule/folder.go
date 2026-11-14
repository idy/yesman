package schedule

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// The scheduler's files live in <workspace>/.yesman/schedules. Every read,
// write and removal goes through handles opened from the folder of the
// workspaces, the store's, never through a path: os.Root keeps each step
// inside the folder it was opened from, and each folder and file on the way
// is checked to be a real folder or regular file, not a link, on the handle
// that is then used. A link put anywhere on the way, the workspace itself,
// .yesman and schedules included, or swapped in while a file is opened, is
// refused and left as it is. Only the store's own folder is opened by its
// path, which may lead through a link the user chose (a moved home or data
// folder, /tmp on macOS). A file is opened without waiting (openRead), so a
// FIFO swapped in after the file was found regular is refused on its handle,
// not waited on for a writer.
//
// os.Root opens files on Windows sharing their deletion, so these reads do
// not stop the store or a user replacing a file meanwhile.

// errUnsafePath reports a path yesman will not read or write through.
type errUnsafePath struct{ path string }

func (e errUnsafePath) Error() string {
	return fmt.Sprintf("%s is a link or not a regular file or folder, so yesman left it unchanged", e.path)
}

// errReplaced: the workspace at the name is not the one hired when the
// caller read it, but one hired since; nothing is read or written for the
// one that was dismissed.
var errReplaced = errors.New("this yesman's workspace was replaced by a newer one while it was read, so nothing was written to it")

// openSchedules opens a workspace's schedules folder. When create is false
// and a folder on the way is missing, the error is fs.ErrNotExist. The
// workspace is never created.
func openSchedules(y yesmen.Yesman, create bool) (*os.Root, error) {
	ws, err := openWorkspace(y)
	if err != nil {
		return nil, err
	}
	defer ws.Close()
	meta, err := openDir(ws, ".yesman", create)
	if err != nil {
		return nil, err
	}
	defer meta.Close()
	if err := sameGeneration(meta, y); err != nil {
		return nil, err
	}
	return openDir(meta, "schedules", create)
}

// openWorkspace opens a workspace from its store's folder: the workspace
// must be a real folder at its name there, not a link to one elsewhere.
// The store makes a workspace's path its folder joined with the name.
func openWorkspace(y yesmen.Yesman) (*os.Root, error) {
	path := filepath.Clean(y.Path)
	name := filepath.Base(path)
	if name != y.Name || yesmen.ValidName(name) != nil {
		return nil, errUnsafePath{y.Path}
	}
	store, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return openDir(store, name, false)
}

// maxMetaFile is the most of a workspace's yesman.json read here.
const maxMetaFile = 1 << 20

// sameGeneration checks, on the .yesman folder opened, that the workspace is
// the one hired when y was read, as the store reads it: a same-name
// workspace hired since, put at its place meanwhile, is another one.
func sameGeneration(meta *os.Root, y yesmen.Yesman) error {
	data, err := readIn(meta, "yesman.json", maxMetaFile)
	if err != nil {
		return err
	}
	var hired struct {
		HiredAt time.Time `json:"hiredAt"`
	}
	if data != nil {
		_ = json.Unmarshal(data, &hired) // as the store: what cannot be read is no time
	}
	if !hired.HiredAt.Equal(y.HiredAt) {
		return errReplaced
	}
	return nil
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
	data, _, err := readVersionIn(dir, name, limit)
	return data, err
}

// fileVersion is a file as it was read or written: which file it was, its
// size and a digest of its content. With no file, info is nil.
type fileVersion struct {
	info fs.FileInfo
	size int64
	sum  [sha256.Size]byte
}

func versionOf(info fs.FileInfo, data []byte) fileVersion {
	return fileVersion{info: info, size: int64(len(data)), sum: sha256.Sum256(data)}
}

// stamp is the version's size and time, as stampIn would give them.
func (v fileVersion) stamp() fileStamp {
	if v.info == nil {
		return fileStamp{}
	}
	return fileStamp{size: v.info.Size(), mod: v.info.ModTime(), ok: true}
}

// openRead opens name in dir to read it, without waiting: had a FIFO been
// put at the name since it was found a regular file, a plain open would wait
// for a writer, holding the caller (the background check, a Save, Close)
// before the handle could be checked and refused. The caller checks the
// handle is the regular file it found.
func openRead(dir *os.Root, name string) (*os.File, error) {
	if beforeOpenRead != nil {
		beforeOpenRead(name)
	}
	return dir.OpenFile(name, os.O_RDONLY|openNoWait, 0)
}

// beforeOpenRead, for tests, runs just before openRead opens a file: after
// the file was found regular at its name.
var beforeOpenRead func(name string)

// readVersionIn is readIn, also giving the version of the file read.
func readVersionIn(dir *os.Root, name string, limit int64) ([]byte, fileVersion, error) {
	info, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fileVersion{}, nil
	}
	if err != nil {
		return nil, fileVersion{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, fileVersion{}, errUnsafePath{name}
	}
	f, err := openRead(dir, name)
	if err != nil {
		return nil, fileVersion{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fileVersion{}, errUnsafePath{name}
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fileVersion{}, err
	}
	if int64(len(data)) > limit {
		return nil, fileVersion{}, fmt.Errorf("%s is larger than %d KiB, so yesman left it unchanged", name, limit>>10)
	}
	return data, versionOf(opened, data), nil
}

// errChangedOnDisk: the file was changed or replaced on disk since yesman
// read it, by something else than this scheduler, and is left as it is.
var errChangedOnDisk = fmt.Errorf("%w (the tasks file was changed on disk since yesman read it, and was left as it is)", ErrConflict)

// writeIn replaces a file atomically, unless something other than a regular
// file is at its name. With want, the file must still be the one read or
// written as want, the same file with the same content, or still be missing
// when want has none: it is checked again, through the same folder, at the
// last moment before the new content takes its place, and anything else
// leaves it as it is and fails with errChangedOnDisk. Between that check and
// the replacement, a writer outside yesman can still be overwritten; the
// check covers the time from the read to the write, not that instant. It
// returns the version written.
func writeIn(dir *os.Root, name string, data []byte, want *fileVersion) (fileVersion, error) {
	if info, err := dir.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return fileVersion{}, errUnsafePath{name}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fileVersion{}, err
	}
	tmp := name + "." + randomHex(4) + ".tmp"
	f, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fileVersion{}, err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	var written fs.FileInfo
	if err == nil {
		written, err = f.Stat()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && want != nil {
		err = unchangedIn(dir, name, *want)
	}
	if err == nil {
		err = dir.Rename(tmp, name)
	}
	if err != nil {
		_ = dir.Remove(tmp)
		return fileVersion{}, err
	}
	return versionOf(written, data), nil
}

// unchangedIn checks that the file at name is still want: the same file,
// read again through dir, with the same content.
func unchangedIn(dir *os.Root, name string, want fileVersion) error {
	info, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		if want.info == nil {
			return nil
		}
		return errChangedOnDisk
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errUnsafePath{name}
	}
	if want.info == nil || !os.SameFile(want.info, info) || info.Size() != want.size {
		return errChangedOnDisk
	}
	f, err := openRead(dir, name)
	if err != nil {
		return err
	}
	defer f.Close()
	if opened, err := f.Stat(); err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return errChangedOnDisk
	}
	data, err := io.ReadAll(io.LimitReader(f, want.size+1))
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(data); int64(len(data)) != want.size || !bytes.Equal(sum[:], want.sum[:]) {
		return errChangedOnDisk
	}
	return nil
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
