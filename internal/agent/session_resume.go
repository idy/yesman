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
//   - one recorded where the workspace no longer is is forked: pi copies its
//     whole history, entry IDs included, into a new session at the current
//     path and leaves the original file untouched. The copy is then the
//     newest session and is opened as it is from then on.
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
		recorded, ok := sessionCwd(f.path, f.info)
		if !ok {
			continue
		}
		if sameDir(recorded, cwd) {
			return []string{"--session", f.path}
		}
		return []string{"--fork", f.path}
	}
	// Nothing pi could read as a session: let pi decide, as before.
	return []string{"--continue"}
}

// sessionCwd reads the working directory recorded in a session file's
// header, the first entry pi can parse. It reads only the file looked at,
// without waiting should a named pipe have taken its place, and no further
// than pi would.
func sessionCwd(path string, looked os.FileInfo) (string, bool) {
	f, err := os.OpenFile(path, sessionOpenFlags, 0)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(looked, info) {
		return "", false
	}
	r := bufio.NewReader(io.LimitReader(f, sessionHeaderLimit+1))
	read := 0
	for {
		line, err := r.ReadBytes('\n')
		read += len(line)
		if read > sessionHeaderLimit {
			return "", false // a header past the bound is no session to pi either
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return "", false
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
					return "", false
				}
				if _, ok := header["id"].(string); !ok {
					return "", false
				}
				cwd, _ := header["cwd"].(string)
				return cwd, true
			}
		}
		if err != nil {
			return "", false
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
