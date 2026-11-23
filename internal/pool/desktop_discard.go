package pool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/idy/yesman/internal/safefs"
)

type desktopDiscardObservation struct {
	marker  fs.FileInfo
	gitfile string
	private fs.FileInfo
}

// DesktopDiscard inspects or deletes a single explicitly selected temporary
// checkout. The desktop owns task admission and the one-use confirmation.
// A detached or dirty working copy needs neither a clean index nor new refs.
func (p *Pool) DesktopDiscard(ctx context.Context, workspace string, w Worktree, orphan, remove bool) (*CheckoutObservation, bool, error) {
	fail := func(err error) (*CheckoutObservation, bool, error) { return nil, false, err }
	path := filepath.Join(workspace, w.Dir)
	if !filepath.IsLocal(w.Dir) || w.Dir == "." || filepath.Base(w.Dir) != w.Dir ||
		realPath(path) != filepath.Join(realPath(workspace), w.Dir) || within(realPath(path), realPath(p.root)) || within(realPath(p.root), realPath(path)) {
		return fail(&RemovalHold{Reason: "The directory is outside this workspace.", Detail: path})
	}
	r, err := p.RecordedRepo(ctx, w.Repo)
	if err != nil {
		return fail(err)
	}
	if w.Branch != "" && (r.DefaultBranch == "" || w.Branch == r.DefaultBranch || w.Branch == "main" || w.Branch == "master") {
		return fail(&RemovalHold{Reason: "The primary branch is protected.", Detail: w.Branch})
	}
	l := p.lock(r.Name)
	if !l.TryLock() {
		return fail(errors.New("repository is in use"))
	}
	defer l.Unlock()
	tl, err := p.treeLock(path)
	if err != nil {
		return fail(err)
	}
	if !tl.TryLock() {
		return fail(errors.New("checkout is in use"))
	}
	defer tl.Unlock()
	if err := p.checkWorktreeEditors(tl); err != nil {
		return fail(err)
	}
	parent, err := os.OpenRoot(workspace)
	if err != nil {
		return fail(err)
	}
	defer parent.Close()
	parentFile, err := parent.Open(".")
	if err != nil {
		return fail(err)
	}
	err = checkDiscardSharing(parentFile, w.Dir)
	parentFile.Close()
	if err != nil {
		return fail(err)
	}
	root, st, err := discardDir(parent, w.Dir)
	if err != nil {
		return fail(err)
	}
	defer root.Close()
	id, err := discardIdentity(root)
	if err != nil || id.Ino == 0 || id.Dev == 0 || w.Root != nil && !sameCleanupRoot(*w.Root, id) {
		return fail(&RemovalHold{Reason: "The checkout directory is missing or was replaced.", Detail: path})
	}
	var private, admin *os.Root
	var privateInfo fs.FileInfo
	var adminName string
	var rootMarker fs.FileInfo
	// Check that the working copy still has its own regular gitfile, and
	// compare the rooted handle with the path used by Git's registration.
	pin, err := openWorktree(path)
	if err != nil {
		return fail(err)
	}
	o := &CheckoutObservation{Identity: id, pin: pin}
	if !id.Usable() {
		o.Identity.Birth, o.Identity.HasBirth = 0, false
	}
	defer func() {
		if remove || id.Usable() {
			o.Close()
		}
	}()
	pinned, err := pin.Stat()
	if err != nil || !os.SameFile(st, pinned) {
		o.Close()
		return fail(errors.New("checkout directory changed"))
	}
	prepare := func() error {
		data, err := safefs.ReadSmall(pin, ".git", maxGitFile)
		if err != nil {
			return err
		}
		marker, err := root.Lstat(".git")
		if err != nil || !marker.Mode().IsRegular() {
			return errors.New("checkout .git entry changed")
		}
		rootMarker = marker
		o.discard = &desktopDiscardObservation{marker: marker, gitfile: string(data)}
		if prior := w.observedDiscard; remove && (prior == nil || prior.gitfile != string(data) || !os.SameFile(prior.marker, marker)) {
			return errors.New("checkout Git pointer changed after preview")
		}
		if orphan {
			// Root provenance is required if the current Git registration
			// cannot establish the checkout instance. Do not touch a stale
			// pointer's destination or repair any old pool registration.
			if w.Root == nil || !w.Root.Usable() || !wellFormedGitfile(data) {
				return errors.New("cannot verify the old checkout and its Git pointer")
			}
			if err := p.CheckWorkspaceDeletion(ctx, path); err != nil {
				return err
			}
			registrations, err := p.worktrees(ctx, r)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(registrations, func(actual Worktree) bool { return actual.CleanupID == w.CleanupID }) {
				return errors.New("checkout is still registered at another path")
			}
		} else {
			pd, privatePath, err := openPrivateFrom(r.Path, path, data)
			if err != nil {
				return err
			}
			defer pd.Close()
			nonce, err := readInstanceMarker(pd)
			if err != nil || nonce != w.CleanupID {
				return errors.New("private Git instance changed")
			}
			common, err := safefs.ReadSmall(pd, "commondir", maxGitFile)
			if err != nil || strings.TrimSpace(string(common)) != "../.." {
				return errors.New("private Git directory does not use the recorded pool repository")
			}
			wts, err := p.worktrees(ctx, r)
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(wts, func(actual Worktree) bool {
				return realPath(actual.Path) == realPath(path) && actual.CleanupID == nonce && actual.Branch == w.Branch && actual.Head == w.Head && actual.Detached == w.Detached
			}) {
				return errors.New("Git registration changed")
			}
			if !w.Detached {
				o.Tracking, err = cleanupTrackingBranch(ctx, r, w.Branch)
				if err != nil || w.observedTracking != nil && *w.observedTracking != o.Tracking {
					return errors.New("the tracking branch changed")
				}
			}
			repository, err := os.OpenRoot(r.Path)
			if err != nil {
				return err
			}
			defer repository.Close()
			admin, _, err = discardDir(repository, "worktrees")
			if err != nil {
				return err
			}
			adminName = filepath.Base(privatePath)
			private, privateInfo, err = discardDir(admin, adminName)
			if err != nil {
				return err
			}
			verified, err := pd.Stat()
			if err != nil || !os.SameFile(privateInfo, verified) {
				return errors.New("private Git directory changed")
			}
			o.discard.private = privateInfo
			if prior := w.observedDiscard; remove && (prior.private == nil || !os.SameFile(prior.private, privateInfo)) {
				return errors.New("private Git directory changed after preview")
			}
			allowed := false
			if err := p.checkWorkspaceRegistrations(ctx, path, func(repo Repo, registered string) bool {
				if allowed || repo.Name != r.Name || realPath(registered) != realPath(path) {
					return false
				}
				allowed = true
				return true
			}); err != nil {
				return err
			}
			if !allowed {
				return errors.New("Git registration disappeared")
			}
			if err := discardWalk(ctx, repository, private, nil, true, false); err != nil {
				return err
			}
		}
		if err := discardWalk(ctx, parent, root, rootMarker, false, false); err != nil {
			return err
		}
		return checkDesktopWorktreeUse(ctx, path)
	}
	err = prepare()
	if admin != nil {
		defer admin.Close()
	}
	if private != nil {
		defer private.Close()
	}
	if err != nil {
		o.Close()
		return fail(err)
	}
	if !remove {
		return o, false, nil
	}
	if !sameDiscardEntry(parent, w.Dir, st) {
		return fail(errors.New("checkout directory changed"))
	}
	// Keep the root gitfile until ordinary bytes are gone. An interrupted
	// purge can recheck the same checkout without borrowing another path.
	if err := discardWalk(ctx, parent, root, rootMarker, false, true); err != nil {
		return fail(&RemovalHold{Reason: "Some files may have been removed. Review this directory again.", Detail: err.Error()})
	}
	if err := discardEntry(parent, w.Dir, st); err != nil {
		return fail(&RemovalHold{Reason: "Some files may have been removed. Review this directory again.", Detail: err.Error()})
	}
	if private != nil {
		// The only pool mutation is removing the verified registration's own
		// directory. Git's recursive worktree command would reopen the path
		// by name, potentially operating on a replacement after local purge.
		if !sameDiscardEntry(admin, adminName, privateInfo) {
			return nil, true, errors.New("private Git registration changed after removing the directory")
		}
		if err := discardWalk(ctx, admin, private, nil, true, true); err != nil {
			return nil, true, err
		}
		if err := discardEntry(admin, adminName, privateInfo); err != nil {
			return nil, true, err
		}
	}
	return nil, true, nil
}

func wellFormedGitfile(data []byte) bool {
	value, ok := strings.CutPrefix(strings.TrimRight(string(data), "\r\n"), "gitdir: ")
	return ok && strings.TrimSpace(value) != "" && !strings.ContainsAny(value, "\r\n\x00")
}

func discardDir(parent *os.Root, name string) (*os.Root, fs.FileInfo, error) {
	st, err := parent.Lstat(name)
	if err != nil || st.Mode().Type() != os.ModeDir {
		return nil, nil, fmt.Errorf("cannot verify directory %s", filepath.Join(parent.Name(), name))
	}
	dir, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := dir.Stat(".")
	if err != nil || !os.SameFile(st, opened) || !sameDiscardEntry(parent, name, st) {
		dir.Close()
		return nil, nil, errors.New("checkout directory changed")
	}
	return dir, st, nil
}

func sameDiscardEntry(dir *os.Root, name string, st fs.FileInfo) bool {
	now, err := dir.Lstat(name)
	return err == nil && now.Mode().Type() == st.Mode().Type() && os.SameFile(st, now)
}

func discardEntry(dir *os.Root, name string, st fs.FileInfo) error {
	if !sameDiscardEntry(dir, name, st) {
		return fmt.Errorf("entry changed: %s", filepath.Join(dir.Name(), name))
	}
	return dir.Remove(name)
}

func discardIdentity(dir *os.Root) (safefs.Identity, error) {
	f, err := dir.Open(".")
	if err != nil {
		return safefs.Identity{}, err
	}
	defer f.Close()
	return safefs.FileIdentity(f)
}

func discardWalk(ctx context.Context, parent, root *os.Root, marker fs.FileInfo, private, remove bool) error {
	volume, err := discardIdentity(parent)
	if err != nil {
		return err
	}
	budget := 1_000_000
	var visit func(*os.Root, string, int) error
	visit = func(dir *os.Root, rel string, depth int) error {
		if depth > 256 || budget == 0 {
			return &RemovalHold{Reason: "The worktree cleanup scan reached its limit.", Detail: dir.Name()}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		id, err := discardIdentity(dir)
		if err != nil || id.Dev != volume.Dev || id.Mount != volume.Mount || runtime.GOOS == "linux" && id.Mount == 0 {
			return &RemovalHold{Reason: "The checkout contains a different mount.", Detail: dir.Name()}
		}
		// An embedded/bare/private Git store can have outside consumers.
		// A worktree registration is allowed only at the exact known root.
		if !private {
			exists := func(name string) bool {
				_, err := dir.Lstat(name)
				return !errors.Is(err, fs.ErrNotExist)
			}
			if exists("objects") && (exists("HEAD") || exists("worktrees")) || exists("HEAD") && (exists("gitdir") || exists("commondir")) {
				return &RemovalHold{Reason: "The checkout contains separate Git data.", Detail: dir.Name()}
			}
		}
		file, err := dir.Open(".")
		if err != nil {
			return err
		}
		defer file.Close()
		for {
			entries, err := file.ReadDir(128)
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return err
				}
				budget--
				if budget < 0 {
					return &RemovalHold{Reason: "The worktree cleanup scan reached its limit.", Detail: dir.Name()}
				}
				name := entry.Name()
				if !remove {
					if err := checkDiscardSharing(file, name); err != nil {
						return err
					}
				}
				child := filepath.Join(rel, name)
				if private && !discardPrivateEntry(child) {
					return &RemovalHold{Reason: "The checkout contains a Git lock, operation or private state.", Detail: child}
				}
				if !private && strings.EqualFold(name, ".git") {
					if depth == 0 && name == ".git" && marker != nil && sameDiscardEntry(dir, name, marker) {
						continue
					}
					return &RemovalHold{Reason: "The checkout contains separate Git data.", Detail: filepath.Join(dir.Name(), name)}
				}
				st, err := dir.Lstat(name)
				if err != nil {
					return err
				}
				switch st.Mode().Type() {
				case os.ModeDir:
					next, opened, err := discardDir(dir, name)
					if err != nil {
						return err
					}
					if !os.SameFile(st, opened) {
						next.Close()
						return errors.New("checkout directory changed")
					}
					err = visit(next, child, depth+1)
					next.Close()
					if err != nil {
						return err
					}
				case 0, os.ModeSymlink:
					if !private || st.Mode().IsRegular() {
						break
					}
					fallthrough
				default:
					return &RemovalHold{Reason: "The checkout contains a special filesystem entry.", Detail: filepath.Join(dir.Name(), name)}
				}
				if remove {
					if err := discardEntry(dir, name, st); err != nil {
						return err
					}
				}
			}
			if errors.Is(err, io.EOF) {
				break
			}
		}
		if remove && depth == 0 && marker != nil {
			return discardEntry(dir, ".git", marker)
		}
		return nil
	}
	return visit(root, "", 0)
}

func discardPrivateEntry(name string) bool {
	switch filepath.ToSlash(name) {
	case "commondir", "gitdir", worktreeInstanceFile, "HEAD", "index", "logs", "logs/HEAD", "logs/ORIG_HEAD", "ORIG_HEAD", "FETCH_HEAD", "COMMIT_EDITMSG", "refs", "refs/heads", "refs/tags":
		return true
	}
	return false
}
