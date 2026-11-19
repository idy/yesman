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
	"github.com/idy/yesman/internal/safefs"
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
//   - Managed file operations, including reads, hold the workspace's checkout
//     lock from before admission through commit. This serializes an edit's
//     content check with its write and keeps reads from seeing a partial
//     managed write. Workspace and process identity are checked after waiting.
//     The change is admitted with BeginChange inside the Store's Use, and held
//     across the commit, so a write of a stopped or replaced process either
//     lands before Stop returns, in that process's own directories, or not at
//     all. A worktree's directory is opened, and checked, while the pool
//     cannot add or remove that checkout (Pool.WithWorktree): the opened
//     directory must have the identity recorded when yesman created the
//     checkout (inode or file index and creation time, on the workspace's
//     file system) and carry the recorded Git instance, and the I/O goes
//     through that opened directory. A checkout recorded without an identity
//     is not written; its identity is never taken from what is there now.
//     Lock order: the workspace's checkout lock, then the Store's Use, then
//     the agent's change lease, then the pool's repository lock, then the
//     worktree's lock. The Manager's own lock is never held across any of it.
//   - Every name below the workspace is opened relative to its parent's
//     handle without following links or reparse points (internal/safefs):
//     a link or junction anywhere, inside the grant or out of it, is refused
//     by that open, so no write reaches .git or another directory through
//     one. Files are opened without
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
	// A worktree: its pool repository, the instance its record names and
	// the identity its directory had when yesman created it.
	repo, instance string
	root           *yesmen.WorktreeRoot
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
	// Share checkout/removal's workspace lock, before either lifecycle lease.
	// The caller snapshot predates this wait; Use and BeginChange below must
	// revalidate its workspace generation and exact process credential.
	l := s.worktreeLock(c.yesman.Name)
	l.Lock()
	defer l.Unlock()
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
	var recordedDir bool
	if len(rest) > 0 && s.Pool != nil {
		records, err := s.Yesmen.WorktreeRecords(y.Name)
		if err != nil && y.TaskRoot {
			return fileGrant{}, nil, fmt.Errorf("check task root worktrees: %w", err)
		}
		if err == nil {
			// The latest record of the directory is the checkout there now.
			for i := len(records) - 1; i >= 0; i-- {
				rec := records[i]
				if !sameName(rec.Dir, rest[0]) {
					continue
				}
				recordedDir = true
				if rec.Removed || rec.Owner != c.agent.Name || !rec.OwnerSince.Equal(c.agent.Since) || (rec.Purpose != "author" && rec.Purpose != "release") || !plainComponent(rec.Dir) || rec.ID == "" {
					break
				}
				sub, ok := below(fileGrant{rel: rec.Dir})
				if !ok {
					break
				}
				if rec.Root == nil {
					// Never measured now: a directory found at request
					// time is not evidence it is the one yesman made.
					return fileGrant{}, nil, fmt.Errorf("write and edit cannot change %s: yesman has no record of the directory it created for this checkout (it was made before such records were kept, or its file system reports no creation time). Make a new checkout with yesman_checkout (purpose author or release, your open task) to use write and edit, or change these files from the shell", rec.Dir)
				}
				return fileGrant{rel: rec.Dir, repo: rec.Repo, instance: rec.ID, root: rec.Root}, sub, nil
			}
		}
	}
	// An explicitly attached task directory grants ordinary project files to
	// the crew. Recorded checkout names and all management paths retain their
	// existing rules. workspaceRelative has already rejected .git components.
	if y.TaskRoot && !recordedDir && s.Yesmen.TaskRootInfo() != nil {
		for _, part := range rest {
			if strings.EqualFold(part, ".yesman") || strings.EqualFold(part, ".git") {
				return fileGrant{}, nil, fmt.Errorf("%s is a protected project path", target)
			}
		}
		return fileGrant{created: s.Yesmen.TaskRootInfo()}, rest, nil
	}
	where := "your own worktrees (author or release checkouts yesman_checkout made for you)"
	if haveTemp {
		where += fmt.Sprintf(", %s and %s", filepath.Join(y.Path, filepath.FromSlash(scratch.Rel)), filepath.Join(y.Path, filepath.FromSlash(evidence.Rel)))
	}
	return fileGrant{}, nil, fmt.Errorf("write and edit only change files in %s; %s is not one of them. Other workspace files, yesman's metadata, other agents' directories and anything outside the workspace are refused", where, target)
}

// commitFile opens the grant's directory and performs req inside it.
func (s *Server) commitFile(y yesmen.Yesman, g fileGrant, sub []string, req fileRequest) (any, error) {
	ws, err := safefs.Open(y.Path)
	if err != nil {
		return nil, err
	}
	defer ws.Close()
	var parts []string
	if g.rel != "" {
		parts = strings.Split(g.rel, "/")
	}
	root, err := safefs.OpenPath(ws, parts, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", g.rel, err)
	}
	defer root.Close()
	if g.created != nil {
		// The very directory reserved when this agent started.
		if info, err := root.Stat(); err != nil || !os.SameFile(info, g.created) {
			return nil, fmt.Errorf("%s was replaced since this agent started; it is not written", g.rel)
		}
	}
	if g.repo != "" {
		if err := s.checkInstance(y, g, ws, root); err != nil {
			return nil, err
		}
	}
	return doFile(root, g, sub, req)
}

// checkInstance requires the opened worktree directory to be the checkout
// its lifecycle record names: the very directory yesman created (its
// identity, on the workspace's file system, as recorded at creation), whose
// .git file, read through root, names the private Git directory carrying
// that instance.
func (s *Server) checkInstance(y yesmen.Yesman, g fileGrant, ws, root *safefs.Dir) error {
	refuse := func(why error) error {
		return fmt.Errorf("%s is not the checkout made for you (%v); it is not written", g.rel, why)
	}
	have, err := root.Identity()
	if err != nil {
		return refuse(err)
	}
	wsID, err := ws.Identity()
	if err != nil {
		return refuse(err)
	}
	if err := sameRoot(have, wsID, g.root); err != nil {
		return refuse(err)
	}
	data, err := safefs.ReadSmall(root, ".git", maxGitFile)
	if err != nil {
		return refuse(err)
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

// sameRoot requires have, the opened checkout directory's current identity,
// to be the one recorded at creation, on the workspace's file system (ws).
// A missing record, and a recorded or current identity without a usable
// creation time (none, or zero, ambiguous with none), are refused: the
// inode number alone does not name the directory yesman made.
func sameRoot(have, ws safefs.Identity, rec *yesmen.WorktreeRoot) error {
	if rec == nil {
		return errors.New("no recorded directory identity")
	}
	if rec.Birth == 0 || !have.Usable() {
		return errors.New("no usable creation time for the directory")
	}
	if have.Dev != ws.Dev || have.Ino != rec.Ino || have.Birth != rec.Birth {
		return errors.New("not the directory yesman created for it")
	}
	return nil
}

// maxGitFile bounds a worktree's .git file: one line naming a directory.
const maxGitFile = 8 << 10

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
func doFile(root *safefs.Dir, g fileGrant, sub []string, req fileRequest) (any, error) {
	name := strings.Join(sub, "/")
	dirPerm := fs.FileMode(0o777)
	if g.private {
		dirPerm = 0o700
	}
	if req.Op == "mkdir" {
		d, err := safefs.OpenPath(root, sub, &dirPerm)
		if err != nil {
			return nil, err
		}
		return map[string]any{}, d.Close()
	}
	if len(sub) == 0 {
		return nil, errors.New("a file path is needed")
	}
	// A plain write makes missing directories; nothing else creates any.
	var mkPerm *fs.FileMode
	if req.Op == "write" && req.Expect == nil {
		mkPerm = &dirPerm
	}
	parent, err := safefs.OpenPath(root, sub[:len(sub)-1], mkPerm)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
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
func openRegular(parent *safefs.Dir, name, leaf string, flag int) (*os.File, error) {
	f, err := parent.OpenFile(leaf, flag, 0o666)
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
