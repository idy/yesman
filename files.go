package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
)

// filesEvent says which files of a yesman's workspace changed, while the
// page watches it (WatchFiles).
var filesEvent = mygo.NewEvent[FilesChange]("files")

// FilesChange lists changed paths of a workspace, relative to it.
type FilesChange struct {
	Yesman string   `json:"yesman"`
	Paths  []string `json:"paths"`
}

// FileEntry is a file or directory in a yesman's workspace.
type FileEntry struct {
	Name string `json:"name"`
	// Path is relative to the workspace, separated by slashes.
	Path string `json:"path"`
	Dir  bool   `json:"dir,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// FileView is a file of a workspace, as the preview shows it.
type FileView struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	// Kind is "text", "image" or "binary".
	Kind string `json:"kind"`
	// Text is a text file's content; Truncated says it is only its start,
	// which cannot be edited.
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// Hash identifies a whole text file's content, for WriteFile to tell
	// whether it changed since.
	Hash string `json:"hash,omitempty"`
	// DataURL is an image's content.
	DataURL string `json:"dataUrl,omitempty"`
}

const (
	maxTextPreview  = 5 << 20 // text read whole, and editable, up to this size
	textPreviewHead = 512 << 10
	maxImagePreview = 8 << 20
	maxFindVisits   = 200_000
	maxFindResults  = 200
)

var imageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".svg": "image/svg+xml", ".ico": "image/x-icon", ".bmp": "image/bmp",
}

// hiddenFile is left out of listings: git's own files and the Finder's.
func hiddenFile(name string) bool { return name == ".git" || name == ".DS_Store" }

// unwatched is a directory whose changes do not matter to the tree, or
// that can hold more files than a watch should follow.
func unwatched(rel string, isDir bool) bool {
	base := path.Base(rel)
	return isDir && (base == ".git" || base == "node_modules") || rel == ".yesman/sessions"
}

// workspaceRoot opens a yesman's workspace so that paths cannot leave it,
// through ".." or symbolic links.
func (s *service) workspaceRoot(name string) (*os.Root, error) {
	y, err := s.app.yesmen.Get(name)
	if err != nil {
		return nil, err
	}
	return os.OpenRoot(y.Path)
}

// cleanRel turns a path from the page into one relative to the workspace:
// "" or "." is the workspace itself.
func cleanRel(rel string) string {
	rel = strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(rel, "\\", "/")), "/")
	if rel == "" {
		return "."
	}
	return rel
}

// ListFiles lists a directory of a yesman's workspace: directories first,
// then files, each by name regardless of case.
func (s *service) ListFiles(yesman, dir string) ([]FileEntry, error) {
	root, err := s.workspaceRoot(yesman)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir = cleanRel(dir)
	f, err := root.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	des, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	entries := make([]FileEntry, 0, len(des))
	for _, de := range des {
		if hiddenFile(de.Name()) {
			continue
		}
		e := FileEntry{Name: de.Name(), Path: path.Join(dir, de.Name()), Dir: de.IsDir()}
		if de.Type()&fs.ModeSymlink != 0 {
			// A link to a directory lists as one, if it stays inside.
			if st, err := root.Stat(e.Path); err == nil {
				e.Dir = st.IsDir()
			}
		}
		if !e.Dir {
			if info, err := de.Info(); err == nil {
				e.Size = info.Size()
			}
		}
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b FileEntry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return entries, nil
}

// ReadFile reads a file of a yesman's workspace for the preview: text up
// to a size, images as data URLs, and only the size of anything else.
func (s *service) ReadFile(yesman, file string) (FileView, error) {
	root, err := s.workspaceRoot(yesman)
	if err != nil {
		return FileView{}, err
	}
	defer root.Close()
	file = cleanRel(file)
	f, err := root.Open(file)
	if err != nil {
		return FileView{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return FileView{}, err
	}
	if st.IsDir() {
		return FileView{}, errors.New(file + " is a directory")
	}
	v := FileView{Path: file, Size: st.Size(), ModTime: st.ModTime()}
	if mime, ok := imageTypes[strings.ToLower(path.Ext(file))]; ok {
		if st.Size() > maxImagePreview {
			v.Kind = "binary"
			return v, nil
		}
		data, err := io.ReadAll(io.LimitReader(f, maxImagePreview))
		if err != nil {
			return v, err
		}
		v.Kind, v.DataURL = "image", "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(data)
		return v, nil
	}
	limit := int64(maxTextPreview)
	if st.Size() > limit {
		limit, v.Truncated = textPreviewHead, true
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return v, err
	}
	if !isText(data, v.Truncated) {
		v.Kind, v.Truncated = "binary", false
		return v, nil
	}
	if v.Truncated {
		// End on a whole line, not inside a character.
		if i := bytes.LastIndexByte(data, '\n'); i > 0 {
			data = data[:i+1]
		}
	}
	v.Kind, v.Text = "text", string(data)
	if !v.Truncated {
		v.Hash = hashOf(data)
	}
	return v, nil
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// errChanged is what WriteFile returns when the file is not what the
// editor read: an agent, or another app, changed it since.
var errChanged = errors.New("the file changed on disk since it was opened")

// WriteFile saves text as a file of a yesman's workspace, keeping its
// permissions. base is the Hash the editor read: when the file now holds
// something else, it is left alone and the error says it changed. An empty
// base writes regardless, also to a file that is gone.
func (s *service) WriteFile(yesman, file, text, base string) (FileView, error) {
	root, err := s.workspaceRoot(yesman)
	if err != nil {
		return FileView{}, err
	}
	defer root.Close()
	file = cleanRel(file)
	if file == "." {
		return FileView{}, errors.New("no file named")
	}
	perm := os.FileMode(0o644)
	link := false
	if st, err := root.Lstat(file); err == nil {
		link = st.Mode()&fs.ModeSymlink != 0
		if st.IsDir() {
			return FileView{}, errors.New(file + " is a directory")
		}
	}
	if st, err := root.Stat(file); err == nil {
		perm = st.Mode().Perm()
		if base != "" {
			current, err := root.ReadFile(file)
			if err != nil {
				return FileView{}, err
			}
			if hashOf(current) != base {
				return FileView{}, errChanged
			}
		}
	} else if base != "" {
		return FileView{}, errChanged // gone since it was read
	}
	data := []byte(text)
	if link {
		// Write through the link, which stays one.
		err = root.WriteFile(file, data, perm)
	} else {
		// Write a copy beside it and move it into place, so that an agent
		// reading the file never sees half of it.
		tmp := path.Join(path.Dir(file), "."+path.Base(file)+".yesman-"+rand.Text()[:8])
		if err = root.WriteFile(tmp, data, perm); err == nil {
			if err = root.Rename(tmp, file); err != nil {
				_ = root.Remove(tmp)
			}
		}
	}
	if err != nil {
		return FileView{}, err
	}
	v := FileView{Path: file, Size: int64(len(data)), Kind: "text", Text: text, Hash: hashOf(data)}
	if st, err := root.Stat(file); err == nil {
		v.ModTime = st.ModTime()
	}
	return v, nil
}

// isText reports whether data reads as UTF-8 text without NUL bytes. A
// cut-off start may end inside a character.
func isText(data []byte, cut bool) bool {
	head := data[:min(len(data), 8000)]
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	if cut {
		for i := 0; i < utf8.UTFMax && len(data) > 0 && !utf8.Valid(data); i++ {
			data = data[:len(data)-1]
		}
	}
	return utf8.Valid(data)
}

// FindFiles finds files of a yesman's workspace whose path has the
// query's characters in order, best matches first: those whose name
// holds the query, then whose path does. under limits it to a folder of
// the workspace, such as a worktree; "" is all of it.
func (s *service) FindFiles(ctx context.Context, yesman, query, under string) ([]string, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return []string{}, nil
	}
	root, err := s.workspaceRoot(yesman)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	type hit struct {
		path string
		rank int
	}
	var hits []hit
	visits := 0
	start := cleanRel(under)
	err = fs.WalkDir(root.FS(), start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip what cannot be read
		}
		if visits++; visits > maxFindVisits || ctx.Err() != nil {
			return fs.SkipAll
		}
		if p == start {
			return nil
		}
		if hiddenFile(d.Name()) || unwatched(p, d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// Matched below the folder searched, which every path shares.
		if rank := matchRank(strings.ToLower(strings.TrimPrefix(p, start+"/")), q); rank >= 0 {
			hits = append(hits, hit{p, rank})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(hits, func(a, b hit) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		if len(a.path) != len(b.path) {
			return len(a.path) - len(b.path)
		}
		return strings.Compare(a.path, b.path)
	})
	paths := make([]string, 0, min(len(hits), maxFindResults))
	for _, h := range hits[:min(len(hits), maxFindResults)] {
		paths = append(paths, h.path)
	}
	return paths, nil
}

// matchRank is 0 when the file's name holds q, 1 when its path does, 2
// when its path has q's characters in order, and -1 otherwise.
func matchRank(p, q string) int {
	switch {
	case strings.Contains(path.Base(p), q):
		return 0
	case strings.Contains(p, q):
		return 1
	}
	i := 0
	for _, r := range p {
		if i < len(q) && r == rune(q[i]) {
			i++
		}
	}
	if i == len(q) {
		return 2
	}
	return -1
}

// fileWatch follows the workspace whose files the page shows.
type fileWatch struct {
	mu     sync.Mutex
	yesman string
	stop   context.CancelFunc
}

// WatchFiles reports changes in a yesman's workspace to the page, as the
// "files" event, until it watches another or "" stops it.
func (s *service) WatchFiles(yesman string) error {
	w := &s.app.files
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.yesman == yesman && w.stop != nil {
		return nil
	}
	if w.stop != nil {
		w.stop()
		w.stop, w.yesman = nil, ""
	}
	if yesman == "" {
		return nil
	}
	y, err := s.app.yesmen.Get(yesman)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	watcher, err := mygo.WatchFiles(ctx, y.Path, mygo.FileWatchOptions{
		Recursive: true,
		Ignore:    unwatched,
		Debounce:  150 * time.Millisecond,
	})
	if err != nil {
		cancel()
		return err
	}
	w.yesman, w.stop = yesman, cancel
	go func() {
		defer watcher.Close()
		for {
			events, err := watcher.Next(ctx)
			if err != nil {
				if ctx.Err() == nil {
					log.Print("watch ", y.Path, ": ", err)
				}
				return
			}
			paths := make([]string, 0, len(events)*2)
			for _, e := range events {
				paths = append(paths, e.Path)
				if e.OldPath != "" {
					paths = append(paths, e.OldPath)
				}
			}
			_ = filesEvent.Broadcast(FilesChange{Yesman: yesman, Paths: paths})
		}
	}()
	return nil
}
