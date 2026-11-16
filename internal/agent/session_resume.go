package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// sessionHeaderLimit bounds the search for a session file's header, as pi
// bounds its own.
const sessionHeaderLimit = 1 << 20

// resumeArgs tells pi which session of an agent's session directory to
// resume, or returns nil when the directory holds no session yet.
//
// The directory belongs to one agent of one workspace, so its newest session
// is that agent's conversation wherever the workspace now lives. pi's
// --continue cannot be relied on for this: given a --session-dir it resumes
// only a session recorded under the current working directory's exact path,
// and otherwise silently starts an empty one. When the workspace's absolute
// path changes, as when the volume holding it mounts under another name,
// every agent would lose its conversation on restart. So yesman picks the
// session itself, newest first as pi does:
//
//   - one recorded in this same directory, under any path to it, is opened
//     as it is;
//   - one recorded where the workspace no longer is is forked: its whole
//     history, entry IDs included, is copied into a new session at the
//     current path, and the original file is left untouched. The copy is
//     then the newest session and is opened as it is from then on. spawn
//     makes the copy with copySession rather than pi's --fork, which writes
//     its copy in place, so that an interrupted copy is never a session.
func resumeArgs(sessions, cwd string) []string {
	entries, err := os.ReadDir(sessions)
	if err != nil {
		return nil
	}
	type candidate struct {
		path string
		info os.FileInfo
	}
	var files []candidate
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(sessions, e.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, candidate{path, info})
	}
	if len(files) == 0 {
		return nil
	}
	// pi orders sessions by modification time. Its file names start with
	// the session's start time, which breaks ties.
	sort.Slice(files, func(i, j int) bool {
		a, b := files[i].info.ModTime(), files[j].info.ModTime()
		if !a.Equal(b) {
			return a.After(b)
		}
		return files[i].info.Name() > files[j].info.Name()
	})
	for _, f := range files {
		header, ok := readSessionHeader(f.path, f.info)
		if !ok {
			continue
		}
		if sameDir(header.cwd, cwd) {
			return []string{"--session", f.path}
		}
		return []string{"--fork", f.path}
	}
	// Nothing pi could read as a session: let pi decide, as before.
	return []string{"--continue"}
}

// sessionHead is what yesman reads of a session file's header.
type sessionHead struct {
	cwd     string
	version json.RawMessage // as recorded, or nil
	end     int64           // the offset just after the header's line
}

// readSessionHeader reads the header of a session file, the first entry pi
// can parse. It reads only the file looked at, without waiting should a
// named pipe have taken its place, and no further than pi would.
func readSessionHeader(path string, looked os.FileInfo) (sessionHead, bool) {
	f, err := os.OpenFile(path, sessionOpenFlags, 0)
	if err != nil {
		return sessionHead{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(looked, info) {
		return sessionHead{}, false
	}
	return parseSessionHeader(f)
}

func parseSessionHeader(f io.Reader) (sessionHead, bool) {
	r := bufio.NewReader(io.LimitReader(f, sessionHeaderLimit+1))
	read := 0
	for {
		line, err := r.ReadBytes('\n')
		read += len(line)
		if read > sessionHeaderLimit {
			return sessionHead{}, false // a header past the bound is no session to pi either
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return sessionHead{}, false
		}
		// pi skips blank lines and lines it cannot parse, or that parse to a
		// false value, before the header; any other entry first is no session.
		var entry any
		if line = bytes.TrimSpace(line); len(line) > 0 && json.Unmarshal(line, &entry) == nil {
			switch entry {
			case nil, false, 0.0, "":
			default:
				header, _ := entry.(map[string]any)
				if header["type"] != "session" {
					return sessionHead{}, false
				}
				if _, ok := header["id"].(string); !ok {
					return sessionHead{}, false
				}
				var fields struct {
					Version json.RawMessage `json:"version"`
				}
				_ = json.Unmarshal(line, &fields)
				cwd, _ := header["cwd"].(string)
				return sessionHead{cwd: cwd, version: fields.Version, end: int64(read)}, true
			}
		}
		if err != nil {
			return sessionHead{}, false
		}
	}
}

// sameDir reports whether a session recorded in dir belongs to cwd: the
// same path, or another path to the same directory, such as through a link.
func sameDir(dir, cwd string) bool {
	if !filepath.IsAbs(dir) {
		return false
	}
	a, b := filepath.Clean(dir), filepath.Clean(cwd)
	if a == b || runtime.GOOS == "windows" && strings.EqualFold(a, b) {
		return true
	}
	da, err := os.Stat(a)
	if err != nil {
		return false
	}
	db, err := os.Stat(b)
	return err == nil && os.SameFile(da, db)
}
