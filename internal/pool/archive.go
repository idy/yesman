package pool

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/idy/yesman/internal/safefs"
)

// LinkedLocation describes what Git and the directory say now, separately
// from what was checked out originally. Only ordinary absolute links and a
// live local branch are supported. Moving a directory preserves its index,
// tracked, untracked and ignored files, and the branch's reachability.
type LinkedLocation struct {
	Active  string `json:"activePath"`
	Stored  string `json:"archivePath"`
	Common  string `json:"commonDir"`
	Private string `json:"privateDir"`
	Branch  string `json:"branch"`
	Head    string `json:"head"`
}

type MoveRequest struct {
	Repo, ID, Active, Stored, OriginalBranch string
	Ino                                      uint64
	Birth                                    int64
	Restore                                  bool
	// Pending and LockReason come only from the persisted move intent.
	Pending    bool
	LockReason string
}

// LinkedMove exists only inside WithLinkedMove, under the repo and both
// directory locks. Physical and Registered can differ only after a journaled
// rename and before the private Git backpointer has been published.
type LinkedMove struct {
	LinkedLocation
	Physical, Registered string
	locked               bool
	req                  MoveRequest
}

func (m LinkedMove) endpoints() (string, string) {
	if m.req.Restore {
		return m.Stored, m.Active
	}
	return m.Active, m.Stored
}

var commitID = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

func (p *Pool) WithLinkedMove(ctx context.Context, req MoveRequest, fn func(*LinkedMove) error) error {
	if !cleanupIDRE.MatchString(req.ID) || req.Ino == 0 || req.Birth == 0 {
		return errors.New("missing original checkout identity")
	}
	for _, path := range []string{req.Active, req.Stored} {
		if !filepath.IsAbs(path) || path != filepath.Clean(path) || strings.ContainsAny(path, "\r\n") || realPath(path) != path {
			return errors.New("worktree move requires ordinary absolute paths")
		}
	}
	if req.Active == req.Stored || filepath.Base(req.Stored) != "worktree-"+req.ID {
		return errors.New("invalid archive destination")
	}
	// The exact lifecycle repo, never a suffix match or a supplied Git dir.
	for _, part := range strings.Split(req.Repo, "/") {
		if !validSegment(part) {
			return errors.New("invalid pool repository name")
		}
	}
	common := filepath.Join(realPath(p.root), filepath.FromSlash(req.Repo)+".git")
	if realPath(common) != common {
		return errors.New("linked pool repository path")
	}
	l := p.lock(req.Repo)
	l.Lock()
	defer l.Unlock()
	active, stored := p.treeLock(req.Active), p.treeLock(req.Stored)
	active.Lock()
	defer active.Unlock()
	stored.Lock()
	defer stored.Unlock()
	m := &LinkedMove{LinkedLocation: LinkedLocation{Active: req.Active, Stored: req.Stored, Common: common}, req: req}
	if err := m.inspect(ctx); err != nil {
		return err
	}
	if err := m.checkOperations(ctx); err != nil {
		return err
	}
	if err := p.checkMoveRegistrations(ctx, m); err != nil {
		return err
	}
	return fn(m)
}

// Include prunable registrations and nested worktrees. Moving a directory
// containing another checkout would strand the other checkout's backpointer.
func (p *Pool) checkMoveRegistrations(ctx context.Context, m *LinkedMove) error {
	matches := 0
	err := p.visitRepos(func(repo, dir string) error {
		if found, err := exists(filepath.Join(dir, "worktrees")); err != nil || !found {
			return err
		}
		list, err := git(ctx, dir, "worktree", "list", "--porcelain", "-z")
		if err != nil {
			return err
		}
		for field := range strings.SplitSeq(list, "\x00") {
			path, ok := strings.CutPrefix(field, "worktree ")
			if !ok {
				continue
			}
			path = realPath(filepath.FromSlash(path))
			switch {
			case path == m.Registered && repo == m.req.Repo:
				matches++
			case within(path, m.Active) || within(path, m.Stored) || within(m.Active, path) || within(m.Stored, path):
				return fmt.Errorf("another Git registration uses %s", path)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if matches != 1 {
		return errors.New("expected exactly one matching pool Git registration")
	}
	return nil
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (m *LinkedMove) inspect(ctx context.Context) error {
	source, dest := m.endpoints()
	hasSrc, err := exists(source)
	if err != nil {
		return err
	}
	hasDst, err := exists(dest)
	if err != nil {
		return err
	}
	if hasSrc && hasDst {
		return fmt.Errorf("destination already exists: %s", dest)
	}
	if !hasSrc && (!m.req.Pending || !hasDst) {
		return fmt.Errorf("original checkout missing: %s", source)
	}
	m.Physical = source
	if hasDst {
		m.Physical = dest
	}
	wt, err := openWorktree(m.Physical)
	if err != nil {
		return err
	}
	defer wt.Close()
	root, err := wt.Identity()
	if err != nil {
		return err
	}
	if !root.Usable() || root.Ino != m.req.Ino || root.Birth != m.req.Birth {
		return errors.New("worktree directory identity changed")
	}
	for _, path := range []string{source, dest} {
		parent, err := openWorktree(filepath.Dir(path))
		if err != nil {
			return err
		}
		id, err := parent.Identity()
		parent.Close()
		if err != nil || id.Dev != root.Dev {
			return errors.New("worktree and archive must be on the same volume")
		}
	}
	data, err := safefs.ReadSmall(wt, ".git", maxGitFile)
	if err != nil {
		return err
	}
	name, ok := strings.CutPrefix(strings.TrimSuffix(string(data), "\n"), "gitdir: ")
	m.Private = filepath.FromSlash(strings.TrimSuffix(name, "\r"))
	if !ok || !filepath.IsAbs(m.Private) || realPath(m.Private) != m.Private || filepath.Dir(m.Private) != filepath.Join(m.Common, "worktrees") {
		return errors.New("unsupported or foreign private Git directory")
	}
	pd, err := openWorktree(m.Private)
	if err != nil {
		return err
	}
	defer pd.Close()
	id, err := readInstanceMarker(pd)
	if err != nil || id != m.req.ID {
		return errors.New("private checkout instance changed or is missing")
	}
	back, err := safefs.ReadSmall(pd, "gitdir", maxGitFile)
	if err != nil {
		return err
	}
	backPath := filepath.FromSlash(strings.TrimRight(string(back), "\r\n"))
	if filepath.Base(backPath) != ".git" {
		return errors.New("invalid private Git backpointer")
	}
	m.Registered = filepath.Dir(backPath)
	if backPath != filepath.Join(m.Registered, ".git") || m.Registered != m.Physical && !(m.req.Pending && m.Physical == dest && m.Registered == source) {
		return errors.New("private Git backpointer points to another worktree")
	}
	commondir, err := safefs.ReadSmall(pd, "commondir", maxGitFile)
	if err != nil || strings.TrimSpace(string(commondir)) != "../.." {
		return errors.New("unexpected Git common directory")
	}
	if err := m.inspectLock(pd); err != nil {
		return err
	}
	if m.Physical != m.Registered && !m.locked {
		return errors.New("split registration without the confirmed move lock")
	}
	fields, err := git(ctx, m.Physical, "rev-parse", "--path-format=absolute", "--git-common-dir", "--absolute-git-dir", "--show-toplevel", "HEAD^{commit}")
	if err != nil {
		return err
	}
	v := strings.Split(fields, "\n")
	if len(v) != 4 || filepath.FromSlash(v[0]) != m.Common || filepath.FromSlash(v[1]) != m.Private || filepath.FromSlash(v[2]) != m.Physical || !commitID.MatchString(v[3]) {
		return errors.New("Git reports a different common directory, worktree or commit")
	}
	m.Head = v[3]
	ref, err := git(ctx, m.Physical, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || !strings.HasPrefix(ref, "refs/heads/") {
		return errors.New("worktree archive requires a local branch")
	}
	m.Branch = strings.TrimPrefix(ref, "refs/heads/")
	def, err := git(ctx, m.Common, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	if err != nil || !strings.HasPrefix(def, "refs/remotes/origin/") {
		return errors.New("cannot verify the protected default branch")
	}
	for _, b := range []string{m.Branch, m.req.OriginalBranch} {
		if b == "main" || b == "master" || b == strings.TrimPrefix(def, "refs/remotes/origin/") {
			return errors.New("primary/default branch is protected")
		}
	}
	return nil
}

func (m *LinkedMove) inspectLock(pd *safefs.Dir) error {
	lock, err := safefs.ReadSmall(pd, "locked", 1024)
	m.locked = err == nil
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !m.req.Pending || m.req.LockReason == "" || strings.TrimRight(string(lock), "\r\n") != m.req.LockReason {
		return errors.New("worktree has a foreign Git lock")
	}
	return nil
}

func (m *LinkedMove) checkOperations(ctx context.Context) error {
	for _, path := range []string{filepath.Join(m.Common, "packed-refs.lock"), filepath.Join(m.Common, "config.lock"), filepath.Join(m.Common, "refs", "heads", filepath.FromSlash(m.Branch)) + ".lock"} {
		if found, err := exists(path); err != nil || found {
			return fmt.Errorf("Git lock or unreadable state at %s", path)
		}
	}
	for _, name := range []string{"HEAD.lock", "index.lock", "gitdir.lock", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer", "BISECT_LOG", "modules", "config.worktree"} {
		if found, err := exists(filepath.Join(m.Private, name)); err != nil || found {
			return fmt.Errorf("unsupported or active Git state: %s", name)
		}
	}
	// Gitlinks are uncommon. Keep all tracked submodule configurations in
	// place, even if a checkout's submodule is only partly initialized.
	entries, err := git(ctx, m.Physical, "ls-files", "--stage", "-z")
	if err != nil {
		return err
	}
	for entry := range strings.SplitSeq(entries, "\x00") {
		if strings.HasPrefix(entry, "160000 ") {
			return errors.New("worktrees containing submodules are not supported")
		}
	}
	return nil
}

// Finish is called only after the intent is durably journaled. Git's normal
// move uses an overwriting rename and leaves registration prunable between
// rename and its backpointer write. Keep Git's lock across those two steps,
// use the OS no-replace operation, and update only this absolute backpointer.
// Every cut is resumable by inspecting observable state, even after unlock.
func (m *LinkedMove) Finish(ctx context.Context, reason string) error {
	want := m.LinkedLocation
	m.req.Pending, m.req.LockReason = true, reason
	if err := m.inspect(ctx); err != nil {
		return err
	}
	if m.LinkedLocation != want {
		return errors.New("checkout changed after confirmation")
	}
	src, dst := m.endpoints()
	if !m.locked && m.Physical == src {
		if _, err := git(ctx, m.Common, "worktree", "lock", "--reason", reason, "--", m.Registered); err != nil {
			return err
		}
		if err := syncDirectory(m.Private); err != nil {
			return err
		}
	}
	if m.Physical == src {
		if err := m.inspect(ctx); err != nil {
			return err
		}
		if m.LinkedLocation != want || !m.locked {
			return errors.New("worktree or its Git lock changed before move")
		}
		if err := m.checkOperations(ctx); err != nil {
			return err
		}
		if err := moveNoReplace(src, dst); err != nil {
			return fmt.Errorf("move worktree: %w", err)
		}
	}
	for _, d := range []string{filepath.Dir(dst), filepath.Dir(src)} {
		if err := syncDirectory(d); err != nil {
			return err
		}
	}
	if err := m.inspect(ctx); err != nil {
		return err
	}
	if m.LinkedLocation != want || m.Physical != dst {
		return errors.New("moved checkout identity changed")
	}
	if m.Registered != dst {
		if err := m.publishBackpointer(dst); err != nil {
			return err
		}
	}
	if err := syncDirectory(m.Private); err != nil {
		return err
	}
	if err := m.inspect(ctx); err != nil {
		return err
	}
	if m.LinkedLocation != want || m.Registered != dst {
		return errors.New("moved Git registration changed")
	}
	if m.locked {
		if _, err := git(ctx, m.Common, "worktree", "unlock", "--", dst); err != nil {
			return err
		}
		return syncDirectory(m.Private)
	}
	return nil
}

func (m *LinkedMove) publishBackpointer(dst string) error {
	r, err := os.OpenRoot(m.Private)
	if err != nil {
		return err
	}
	defer r.Close()
	tmp := "yesman-gitdir-" + rand.Text()
	f, err := r.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer r.Remove(tmp)
	_, err = fmt.Fprintln(f, filepath.ToSlash(filepath.Join(dst, ".git")))
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	return r.Rename(tmp, "gitdir")
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
