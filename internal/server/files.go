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
//     nothing. The grant is chosen from the path before any checkout's
//     metadata is read, and only the chosen checkout's is read: small regular
//     files, through handles, never waiting on one.
//   - The change is admitted with BeginChange inside the Store's Use, and held
//     across the commit, so a write of a stopped or replaced process either
//     lands before Stop returns, in that process's own directories, or not at
//     all. A worktree's instance is checked, and its directory opened, while
//     the pool cannot add or remove that checkout (Pool.WithWorktree), and the
//     I/O goes through that opened directory: a directory put in its place
//     later is never written. Lock order: the workspace's checkout lock, then
//     the Store's Use, then the agent's change lease, then the pool's
//     repository lock, then the worktree's lock. The Manager's own lock is
//     never held across any of it.
//   - Every name below the workspace is opened relative to its parent's
//     handle without following links (files_unix.go, files_windows.go): a
//     link anywhere, inside the grant or out of it, is refused, so no write
//     reaches .git or another directory through one. Files are opened without
//     truncation, checked to be a regular file with one link, compared with
//     the content an edit read, and only then truncated and written.
//   - An edit's write carries the hash of what it read and only changes that
//     existing file: it creates neither the file nor a directory, so a file
//     removed meanwhile stays removed. A plain write creates what it needs.
//
// This covers these two tools only. Shell commands and anything else the
// agent runs write with the process's own permissions; it is not a sandbox.

// maxManagedRead bounds what an edit may read through the app.
const maxManagedRead = 32 << 20

type fileRequest struct {
	Op      string  `json:"op"`
	Path    string  `json:"path"`
	Content *string `json:"content,omitempty"`
	// Expect, when present, makes a write an edit of the existing file whose
	// content had this SHA-256.
	Expect *string `json:"expect,omitempty"`
}

type fileGrant struct {
	rel     string      // slash-separated directory below the workspace
	created fs.FileInfo // identity the directory must still have, if known
	private bool        // a reserved directory: new subdirectories are 0700
	// A worktree: its pool repository and the instance its record names.
	repo, instance string
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
		grant, sub, err := s.fileGrant(c, y, req.Path, req.Op == "mkdir")
		if err != nil {
			return err
		}
		if grant.repo == "" {
			out, err = s.commitFile(y, grant, sub, req)
			return err
		}
		return s.Pool.WithWorktree(filepath.Join(y.Path, filepath.FromSlash(grant.rel)), func() error {
			out, err = s.commitFile(y, grant, sub, req)
			return err
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// fileGrant finds the grant path falls in, from trusted state, and returns
// the part of path below it. It reads no checkout's metadata; commitFile
// checks the chosen checkout's instance.
func (s *Server) fileGrant(c caller, y yesmen.Yesman, target string, dir bool) (fileGrant, []string, error) {
	rest, err := workspaceRelative(y.Path, target)
	if err != nil {
		return fileGrant{}, nil, err
	}
	below := func(g fileGrant) ([]string, bool) {
		prefix := strings.Split(g.rel, "/")
		if len(rest) < len(prefix) || (!dir && len(rest) == len(prefix)) {
			return nil, false
		}
		for i := range prefix {
			if !sameName(rest[i], prefix[i]) {
				return nil, false
			}
		}
		return rest[len(prefix):], true
	}
	scratch, evidence, haveTemp := s.Agents.ProcessTemp(c.token)
	if haveTemp {
		for _, g := range []fileGrant{{rel: scratch.Rel, created: scratch.Info, private: true}, {rel: evidence.Rel, created: evidence.Info, private: true}} {
			if sub, ok := below(g); ok {
				return g, sub, nil
			}
		}
	}
	if len(rest) > 0 && s.Pool != nil {
		if records, err := s.Yesmen.WorktreeRecords(y.Name); err == nil {
			// The latest record of the directory is the checkout there now.
			for i := len(records) - 1; i >= 0; i-- {
				rec := records[i]
				if !sameName(rec.Dir, rest[0]) {
					continue
				}
				if rec.Removed || rec.Owner != c.agent.Name || !rec.OwnerSince.Equal(c.agent.Since) || (rec.Purpose != "author" && rec.Purpose != "release") || !plainComponent(rec.Dir) || rec.ID == "" {
					break
				}
				if sub, ok := below(fileGrant{rel: rec.Dir}); ok {
					return fileGrant{rel: rec.Dir, repo: rec.Repo, instance: rec.ID}, sub, nil
				}
				break
			}
		}
	}
	where := "your own worktrees (author or release checkouts yesman_checkout made for you)"
	if haveTemp {
		where += fmt.Sprintf(", %s and %s", filepath.Join(y.Path, filepath.FromSlash(scratch.Rel)), filepath.Join(y.Path, filepath.FromSlash(evidence.Rel)))
	}
	return fileGrant{}, nil, fmt.Errorf("write and edit only change files in %s; %s is not one of them. Other workspace files, yesman's metadata, other agents' directories and anything outside the workspace are refused", where, target)
}

// commitFile opens the grant's directory and performs req inside it.
func (s *Server) commitFile(y yesmen.Yesman, g fileGrant, sub []string, req fileRequest) (any, error) {
	ws, err := openWorkspace(y.Path)
	if err != nil {
		return nil, err
	}
	defer ws.close()
	root, err := openPath(ws, strings.Split(g.rel, "/"), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", g.rel, err)
	}
	defer root.close()
	if g.created != nil {
		// The very directory reserved when this agent started.
		if info, err := root.stat(); err != nil || !os.SameFile(info, g.created) {
			return nil, fmt.Errorf("%s was replaced since this agent started; it is not written", g.rel)
		}
	}
	if g.repo != "" {
		if err := s.checkInstance(y, g, root); err != nil {
			return nil, err
		}
	}
	return doFile(root, g, sub, req)
}

// checkInstance requires the opened worktree directory to be the checkout
// its lifecycle record names: its .git file, read through root, must name
// the private Git directory carrying that instance.
func (s *Server) checkInstance(y yesmen.Yesman, g fileGrant, root *dirHandle) error {
	refuse := func(why error) error {
		return fmt.Errorf("%s is not the checkout made for you (%v); it is not written", g.rel, why)
	}
	f, err := root.openFile(".git", os.O_RDONLY)
	if err != nil {
		return refuse(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return refuse(err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxGitFile {
		return refuse(errors.New("its .git is not a small regular file"))
	}
	data, err := io.ReadAll(io.LimitReader(f, maxGitFile+1))
	if err != nil {
		return refuse(err)
	}
	if len(data) > maxGitFile {
		return refuse(errors.New("its .git is not a small regular file"))
	}
	id, err := s.Pool.InstanceOf(g.repo, filepath.Join(y.Path, filepath.FromSlash(g.rel)), data)
	if err != nil {
		return refuse(err)
	}
	if id != g.instance {
		return refuse(errors.New("another checkout instance"))
	}
	return nil
}

// maxGitFile bounds a worktree's .git file: one line naming a directory.
const maxGitFile = 8 << 10

// openPath opens the directories names below d, one at a time without
// following links. With perm, a missing one is made.
func openPath(d *dirHandle, names []string, perm *fs.FileMode) (*dirHandle, error) {
	cur := d
	for _, name := range names {
		if perm != nil {
			if err := cur.mkdir(name, *perm); err != nil {
				closeUnless(cur, d)
				return nil, err
			}
		}
		next, err := cur.openDir(name)
		closeUnless(cur, d)
		if err != nil {
			return nil, err
		}
		cur = next
	}
	if cur == d {
		// The caller closes what it is given back separately.
		return d.openDir(".")
	}
	return cur, nil
}

func closeUnless(h, keep *dirHandle) {
	if h != keep {
		h.close()
	}
}

// workspaceRelative splits target, an absolute path, into names below the
// workspace. The workspace may be spelled through an alias (/var and
// /private/var, a drive letter's case): the nearest existing ancestor that
// is the workspace marks where the names start. Names are checked here;
// links below are refused as each name is opened.
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

// doFile performs req below root, the grant's directory.
func doFile(root *dirHandle, g fileGrant, sub []string, req fileRequest) (any, error) {
	name := strings.Join(sub, "/")
	dirPerm := fs.FileMode(0o777)
	if g.private {
		dirPerm = 0o700
	}
	if req.Op == "mkdir" {
		d, err := openPath(root, sub, &dirPerm)
		if err != nil {
			return nil, err
		}
		return map[string]any{}, d.close()
	}
	if len(sub) == 0 {
		return nil, errors.New("a file path is needed")
	}
	// A plain write makes missing directories; nothing else creates any.
	var mkPerm *fs.FileMode
	if req.Op == "write" && req.Expect == nil {
		mkPerm = &dirPerm
	}
	parent, err := openPath(root, sub[:len(sub)-1], mkPerm)
	if err != nil {
		return nil, err
	}
	defer parent.close()
	leaf := sub[len(sub)-1]
	switch req.Op {
	case "access":
		f, err := openRegular(parent, name, leaf, os.O_RDWR)
		if err != nil {
			return nil, err
		}
		return map[string]any{}, f.Close()
	case "read":
		f, err := openRegular(parent, name, leaf, os.O_RDONLY)
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
		flag := os.O_RDWR
		if req.Expect == nil {
			flag |= os.O_CREATE
		}
		f, err := openRegular(parent, name, leaf, flag)
		if err != nil {
			if req.Expect != nil && errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("%s was removed since edit read it; it is not recreated", name)
			}
			return nil, err
		}
		defer f.Close()
		if req.Expect != nil {
			data, err := io.ReadAll(io.LimitReader(f, maxManagedRead+1))
			if err != nil {
				return nil, err
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != *req.Expect {
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

// openRegular opens leaf in parent without truncating, following a link or
// waiting on a FIFO, and accepts only a regular file with a single link.
func openRegular(parent *dirHandle, name, leaf string, flag int) (*os.File, error) {
	f, err := parent.openFile(leaf, flag)
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
