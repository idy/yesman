package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// The managed write entry for workers' write and edit tools (pi/yesman.ts
// managedFiles). pi still resolves the path and computes the edit; the app
// decides where the calling process may write and does the I/O:
//
//   - The grants come from trusted state only: the worktrees whose lifecycle
//     record names the caller's current generation as owner, for authoring or
//     release, and whose private Git instance is the recorded checkout; and
//     the temporary and evidence directories Manager reserved for the calling
//     process. A path, a directory name, owner.json or a task name grants
//     nothing.
//   - The change is admitted with BeginChange inside the Store's Use, and held
//     across the commit, so a write of a stopped or replaced process either
//     lands before Stop returns, in that process's own directories, or not at
//     all.
//   - All I/O goes through os.Root opened at the grant root, so a link or a
//     parent swapped for one cannot take it outside. Files are opened without
//     truncation, checked to be a regular file with one link, compared with
//     the content an edit read, and only then truncated and written.
//
// This covers these two tools only. Shell commands and anything else the
// agent runs write with the process's own permissions; it is not a sandbox.

// maxManagedRead bounds what an edit may read through the app.
const maxManagedRead = 32 << 20

type fileRequest struct {
	Op      string  `json:"op"`
	Path    string  `json:"path"`
	Content *string `json:"content,omitempty"`
	Expect  string  `json:"expect,omitempty"`
}

type fileGrant struct {
	rel     string      // slash-separated directory below the workspace
	created fs.FileInfo // identity the directory must still have, if known
	private bool        // a reserved directory: new subdirectories are 0700
}

func (s *Server) files(ctx context.Context, c caller, r *http.Request) (any, error) {
	var req fileRequest
	r.Body = http.MaxBytesReader(nil, r.Body, 2*maxManagedRead)
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if c.agent.Role == agent.RoleLead {
		return nil, errors.New("the lead's write and edit tools do not use the managed entry")
	}
	var out any
	err := s.Yesmen.Use(c.yesman.Name, func(y yesmen.Yesman) error {
		if !y.HiredAt.Equal(c.yesman.HiredAt) {
			return agent.ErrRetired
		}
		release, err := s.Agents.BeginChange(c.token, y)
		if err != nil {
			return err
		}
		defer release()
		grant, sub, err := s.fileGrant(ctx, c, y, req.Path, req.Op == "mkdir")
		if err != nil {
			return err
		}
		out, err = doFile(y.Path, grant, sub, req)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// fileGrant finds the grant path falls in, from trusted state, and returns
// the part of path below it.
func (s *Server) fileGrant(ctx context.Context, c caller, y yesmen.Yesman, target string, dir bool) (fileGrant, []string, error) {
	rest, err := workspaceRelative(y.Path, target)
	if err != nil {
		return fileGrant{}, nil, err
	}
	var grants []fileGrant
	if scratch, evidence, ok := s.Agents.ProcessTemp(c.token); ok {
		grants = append(grants, fileGrant{rel: scratch.Rel, created: scratch.Info, private: true}, fileGrant{rel: evidence.Rel, created: evidence.Info, private: true})
	}
	if records, err := s.Yesmen.WorktreeRecords(y.Name); err == nil && s.Pool != nil {
		for _, rec := range records {
			if rec.Removed || rec.Owner != c.agent.Name || !rec.OwnerSince.Equal(c.agent.Since) || (rec.Purpose != "author" && rec.Purpose != "release") || !plainComponent(rec.Dir) {
				continue
			}
			if id, err := s.Pool.WorktreeInstance(rec.Repo, filepath.Join(y.Path, rec.Dir)); err != nil || id != rec.ID {
				continue
			}
			grants = append(grants, fileGrant{rel: rec.Dir})
		}
	}
	for _, g := range grants {
		prefix := strings.Split(g.rel, "/")
		if len(rest) < len(prefix) || (!dir && len(rest) == len(prefix)) {
			continue
		}
		match := true
		for i := range prefix {
			if !sameName(rest[i], prefix[i]) {
				match = false
				break
			}
		}
		if match {
			return g, rest[len(prefix):], nil
		}
	}
	where := "your own worktrees (author or release checkouts yesman_checkout made for you)"
	if scratch, evidence, ok := s.Agents.ProcessTemp(c.token); ok {
		where += fmt.Sprintf(", %s and %s", filepath.Join(y.Path, filepath.FromSlash(scratch.Rel)), filepath.Join(y.Path, filepath.FromSlash(evidence.Rel)))
	}
	return fileGrant{}, nil, fmt.Errorf("write and edit only change files in %s; %s is not one of them. Other workspace files, yesman's metadata, other agents' directories and anything outside the workspace are refused", where, target)
}

// workspaceRelative splits target, an absolute path, into names below the
// workspace. The workspace may be spelled through an alias (/var and
// /private/var, a drive letter's case): the nearest existing ancestor that
// is the workspace marks where the names start. Names are checked here;
// links below are left to os.Root.
func workspaceRelative(workspace, target string) ([]string, error) {
	if !filepath.IsAbs(target) || strings.HasPrefix(target, `\\?\`) || strings.HasPrefix(target, `\\.\`) {
		return nil, fmt.Errorf("%s is not an absolute file path", target)
	}
	ws, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}
	wsInfo, err := os.Stat(ws)
	if err != nil {
		return nil, err
	}
	cur := filepath.Clean(target)
	var rest []string
	for {
		if info, err := os.Stat(cur); err == nil && os.SameFile(info, wsInfo) {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil, fmt.Errorf("%s is outside this workspace", target)
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
	}
	for _, name := range rest {
		if !plainComponent(name) || sameName(name, ".git") {
			return nil, fmt.Errorf("%s names a path yesman does not write (%q)", target, name)
		}
	}
	return rest, nil
}

func plainComponent(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return false
	}
	if runtime.GOOS == "windows" {
		if strings.ContainsAny(name, `:*?"<>|`) || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
			return false
		}
		base, _, _ := strings.Cut(strings.ToUpper(name), ".")
		switch base {
		case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
			"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
			return false
		}
	}
	return true
}

// sameName compares names as the file systems yesman runs on do by
// default: without case on macOS and Windows.
func sameName(a, b string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// doFile performs req inside the grant.
func doFile(workspace string, g fileGrant, sub []string, req fileRequest) (any, error) {
	ws, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	defer ws.Close()
	// The grant directory itself must be a plain directory, and the very one
	// reserved when its identity is known.
	parts := strings.Split(g.rel, "/")
	for i := range parts {
		info, err := ws.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, err
		}
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("%s is not a plain directory", strings.Join(parts[:i+1], "/"))
		}
		if i == len(parts)-1 && g.created != nil && !os.SameFile(info, g.created) {
			return nil, fmt.Errorf("%s was replaced since this agent started; it is not written", g.rel)
		}
	}
	root, err := ws.OpenRoot(g.rel)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if info, err := root.Stat("."); err != nil || (g.created != nil && !os.SameFile(info, g.created)) {
		return nil, fmt.Errorf("%s was replaced since this agent started; it is not written", g.rel)
	}
	name := strings.Join(sub, "/")
	dirPerm := fs.FileMode(0o777)
	if g.private {
		dirPerm = 0o700
	}
	switch req.Op {
	case "mkdir":
		if name == "" {
			return map[string]any{}, nil
		}
		return map[string]any{}, root.MkdirAll(name, dirPerm)
	case "access":
		f, err := openRegular(root, name, os.O_RDWR)
		if err != nil {
			return nil, err
		}
		return map[string]any{}, f.Close()
	case "read":
		f, err := openRegular(root, name, os.O_RDONLY)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxManagedRead+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxManagedRead {
			return nil, fmt.Errorf("%s is larger than %d MiB; edit does not read it through yesman", name, maxManagedRead>>20)
		}
		sum := sha256.Sum256(data)
		return map[string]any{"data": base64.StdEncoding.EncodeToString(data), "sha256": hex.EncodeToString(sum[:])}, nil
	case "write":
		if req.Content == nil {
			return nil, errors.New("no content to write")
		}
		if dir := strings.Join(sub[:len(sub)-1], "/"); dir != "" {
			if err := root.MkdirAll(dir, dirPerm); err != nil {
				return nil, err
			}
		}
		f, err := openRegular(root, name, os.O_RDWR|os.O_CREATE)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if req.Expect != "" {
			data, err := io.ReadAll(io.LimitReader(f, maxManagedRead+1))
			if err != nil {
				return nil, err
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != req.Expect {
				return nil, fmt.Errorf("%s changed since edit read it; read it again", name)
			}
		}
		if err := f.Truncate(0); err != nil {
			return nil, err
		}
		if _, err := f.WriteAt([]byte(*req.Content), 0); err != nil {
			return nil, err
		}
		return map[string]any{}, f.Close()
	}
	return nil, fmt.Errorf("unknown file operation %q", req.Op)
}

// openRegular opens name below root without truncating, never waiting on a
// FIFO, and accepts only a regular file with a single link.
func openRegular(root *os.Root, name string, flag int) (*os.File, error) {
	if name == "" {
		return nil, errors.New("a file path is needed")
	}
	f, err := root.OpenFile(name, flag|nonblockFlag, 0o666)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", name)
	}
	if err == nil {
		var links uint64
		if links, err = linkCount(f, info); err == nil && links != 1 {
			err = fmt.Errorf("%s has %d hard links; yesman does not write through them", name, links)
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
