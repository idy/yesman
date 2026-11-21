package pool

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

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
	Cancel                                   bool // only end a pending intent that is still at its source
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
	gitPath              string // exact Git spelling of the authenticated private backpointer
	locked               bool
	req                  MoveRequest
}

func (m LinkedMove) endpoints() (string, string) {
	if m.req.Restore {
		return m.Stored, m.Active
	}
	return m.Active, m.Stored
}

// Cancel only releases the pending move at its source. An unrelated object
// can occupy the destination without participating in that operation.
func (m LinkedMove) pathsToInspect() []string {
	source, dest := m.endpoints()
	if m.req.Cancel {
		return []string{source}
	}
	return []string{source, dest}
}

var commitID = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

func (p *Pool) WithLinkedMove(ctx context.Context, req MoveRequest, fn func(*LinkedMove) error) error {
	if !cleanupIDRE.MatchString(req.ID) || req.Ino == 0 || req.Birth == 0 {
		return errors.New("missing original checkout identity")
	}
	m := &LinkedMove{LinkedLocation: LinkedLocation{Active: req.Active, Stored: req.Stored}, req: req}
	for _, path := range []string{req.Active, req.Stored} {
		if !filepath.IsAbs(path) || path != filepath.Clean(path) || strings.ContainsAny(path, "\r\n") {
			return errors.New("worktree move requires ordinary absolute paths")
		}
	}
	for _, path := range m.pathsToInspect() {
		if realPath(path) != path {
			return errors.New("worktree path has changed or is a link")
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
	source, dest := m.endpoints()
	first, err := p.treeLock(source)
	if err != nil {
		return err
	}
	first.Lock()
	defer first.Unlock()
	if !req.Cancel {
		second, err := p.treeLock(dest)
		if err != nil {
			return err
		}
		if second == first {
			return errors.New("archive and workspace resolve to the same directory entry")
		}
		second.Lock()
		defer second.Unlock()
	}
	m.Common = common
	if err := m.inspect(ctx); err != nil {
		return err
	}
	if err := m.checkOperations(ctx); err != nil {
		return err
	}
	if req.Cancel {
		if !req.Pending || m.Physical != source || m.Registered != source {
			return errors.New("the directory has already moved. Choose Finish to complete its registration")
		}
	} else if m.Physical == source {
		if err := m.checkDefaultBranch(ctx); err != nil {
			return err
		}
	}
	if err := p.checkMoveRegistrations(ctx, m); err != nil {
		return err
	}
	return fn(m)
}

// Include prunable registrations and nested worktrees. Moving a directory
// containing another checkout would strand the other checkout's backpointer.
func (p *Pool) checkMoveRegistrations(ctx context.Context, m *LinkedMove) error {
	protected := m.pathsToInspect()
	for i, path := range protected {
		physical, err := registrationPath(path)
		if err != nil {
			return err
		}
		protected[i] = physical
	}
	matches := 0
	repos, err := p.repositoryPaths(ctx)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		found, err := exists(filepath.Join(repo.Path, "worktrees"))
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		list, err := git(ctx, repo.Path, "worktree", "list", "--porcelain", "-z")
		if err != nil {
			return err
		}
		for field := range strings.SplitSeq(list, "\x00") {
			path, ok := strings.CutPrefix(field, "worktree ")
			if !ok {
				continue
			}
			if repo.Name == m.req.Repo && managedPathMatches(path, m.Registered, m.locationDepth(m.Registered)) {
				matches++
				continue
			}
			physical, err := registrationPath(filepath.FromSlash(path))
			if err != nil {
				return err
			}
			for _, endpoint := range protected {
				if within(physical, endpoint) || within(endpoint, physical) {
					return fmt.Errorf("another Git registration uses %s", path)
				}
			}
		}
	}
	if matches != 1 {
		return errors.New("expected exactly one matching pool Git registration")
	}
	return nil
}

// Git lists prunable registrations too. Resolve the nearest existing parent
// so a junction still identifies a nested registration with a missing suffix.
func registrationPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("invalid registered worktree path %q", path)
	}
	path = filepath.Clean(path)
	for current := path; ; current = filepath.Dir(current) {
		resolved, err := PhysicalPath(current)
		if err == nil {
			suffix, err := filepath.Rel(current, path)
			if err != nil {
				return "", err
			}
			return filepath.Join(resolved, suffix), nil
		}
		if !errors.Is(err, os.ErrNotExist) || current == filepath.Dir(current) {
			return "", fmt.Errorf("inspect registration %s: %w", path, err)
		}
	}
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Git may have recorded an absolute path through an alias of the configured
// storage root (for example /var on macOS). Compare the root by identity and
// require the literal managed components below it. This also works for the
// missing source of a split registration, without following links in a tree.
func managedPathMatches(raw, canonical string, depth int) bool {
	path := filepath.FromSlash(raw)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	if path == canonical {
		return true
	}
	for range depth {
		a, b := filepath.Base(path), filepath.Base(canonical)
		if a != b && !(runtime.GOOS == "windows" && strings.EqualFold(a, b)) {
			return false
		}
		path, canonical = filepath.Dir(path), filepath.Dir(canonical)
	}
	a, err := os.Stat(path)
	if err != nil {
		return false
	}
	b, err := os.Stat(canonical)
	return err == nil && os.SameFile(a, b)
}

// The active tree is <workspace-root>/<hire>/<dir>. The archived tree is a
// direct child of the archive root.
func (m *LinkedMove) locationDepth(path string) int {
	if path == m.Active {
		return 2
	}
	return 1
}

func (m *LinkedMove) inspect(ctx context.Context) error {
	source, dest := m.endpoints()
	hasSrc, err := exists(source)
	if err != nil {
		return err
	}
	m.Physical = source
	if m.req.Cancel {
		if !m.req.Pending || !hasSrc {
			return errors.New("the original source is missing. Choose Finish to verify the moved directory")
		}
	} else {
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
		if hasDst {
			m.Physical = dest
		}
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
	for _, path := range m.pathsToInspect() {
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
	rawPrivate := filepath.FromSlash(strings.TrimSuffix(name, "\r"))
	commonDepth := 1 + strings.Count(m.req.Repo, "/")
	m.Private = filepath.Join(m.Common, "worktrees", filepath.Base(rawPrivate))
	if !ok || !managedPathMatches(rawPrivate, m.Private, commonDepth+2) || realPath(m.Private) != m.Private {
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
	backPath := strings.TrimRight(string(back), "\r\n")
	switch {
	case managedPathMatches(backPath, filepath.Join(m.Physical, ".git"), m.locationDepth(m.Physical)+1):
		m.Registered = m.Physical
	case m.req.Pending && m.Physical == dest && managedPathMatches(backPath, filepath.Join(source, ".git"), m.locationDepth(source)+1):
		m.Registered = source
	default:
		return errors.New("private Git backpointer points to another worktree")
	}
	m.gitPath = filepath.ToSlash(filepath.Dir(filepath.FromSlash(backPath)))
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
	if len(v) != 4 || !managedPathMatches(v[0], m.Common, commonDepth) || !managedPathMatches(v[1], m.Private, commonDepth+2) || !managedPathMatches(v[2], m.Physical, m.locationDepth(m.Physical)) || !commitID.MatchString(v[3]) {
		return errors.New("Git reports a different common directory, worktree or commit")
	}
	m.Head = v[3]
	ref, err := git(ctx, m.Physical, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || !strings.HasPrefix(ref, "refs/heads/") {
		return errors.New("worktree archive requires a local branch")
	}
	m.Branch = strings.TrimPrefix(ref, "refs/heads/")
	return nil
}

// Origin's cached HEAD can stay on the old default after fetch --prune. Ask
// the remote on preview and confirmation, and immediately before a rename.
// Finishing a rename that has already landed only repairs local metadata.
func (m *LinkedMove) checkDefaultBranch(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	remote, err := git(ctx, m.Common, "ls-remote", "--exit-code", "--symref", "origin", "HEAD")
	first, second, _ := strings.Cut(remote, "\n")
	ref, name, _ := strings.Cut(strings.TrimSuffix(first, "\r"), "\t")
	def, ok := strings.CutPrefix(ref, "ref: refs/heads/")
	head, target, _ := strings.Cut(second, "\t")
	if err != nil || !ok || def == "" || name != "HEAD" || !commitID.MatchString(head) || target != "HEAD" {
		return errors.New("cannot verify the current default branch on origin. Check the remote connection and try again")
	}
	for _, b := range []string{m.Branch, m.req.OriginalBranch} {
		if b == "main" || b == "master" || b == def {
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
	if m.req.Cancel {
		return errors.New("canceling a pending move cannot rename a directory")
	}
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
		if _, err := git(ctx, m.Common, "worktree", "lock", "--reason", reason, "--", m.gitPath); err != nil {
			return err
		}
	}
	if m.Physical == src {
		if err := m.syncOwnedLock(reason); err != nil {
			return err
		}
		if err := m.checkDefaultBranch(ctx); err != nil {
			return err
		}
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
		if _, err := git(ctx, m.Common, "worktree", "unlock", "--", m.gitPath); err != nil {
			return err
		}
		return syncDirectory(m.Private)
	}
	return nil
}

// Flush the contents of Git's lock as well as its directory before moving. A
// retry can observe a lock created just before a crash or a failed sync.
func (m *LinkedMove) syncOwnedLock(reason string) error {
	pd, err := openWorktree(m.Private)
	if err != nil {
		return err
	}
	defer pd.Close()
	f, err := pd.OpenFile("locked", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 1024 {
		return errors.New("invalid worktree move lock")
	}
	data, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil || strings.TrimRight(string(data), "\r\n") != reason {
		return errors.New("the worktree move lock changed")
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return syncDirectory(m.Private)
}

// Cancel releases only this intent's lock. The same original directory must
// still be physically present and registered at the source on both checks.
func (m *LinkedMove) Cancel(ctx context.Context) error {
	want := m.LinkedLocation
	if !m.req.Cancel || !m.req.Pending {
		return errors.New("only a pending move can be canceled")
	}
	if err := m.inspect(ctx); err != nil {
		return err
	}
	src, _ := m.endpoints()
	if m.LinkedLocation != want || m.Physical != src || m.Registered != src {
		return errors.New("checkout changed before cancellation")
	}
	if m.locked {
		if _, err := git(ctx, m.Common, "worktree", "unlock", "--", m.gitPath); err != nil {
			return err
		}
	}
	return syncDirectory(m.Private)
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
	return safefs.ReplaceSynced(r, tmp, "gitdir")
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
