package pool

import (
	"context"
	"crypto/sha256"
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
	"github.com/idy/yesman/internal/yesmen"
)

type desktopDiscardObservation struct {
	marker      fs.FileInfo
	gitfile     string
	private     fs.FileInfo
	resolveUndo [sha256.Size]byte
	hasUndo     bool
}

func (o *CheckoutObservation) DiscardHasResolveUndo() bool {
	return o != nil && o.discard != nil && o.discard.hasUndo
}

// DesktopDiscard inspects or deletes a single explicitly selected temporary
// checkout. The desktop owns task admission and the one-use confirmation.
// A detached or dirty working copy needs neither a clean index nor new refs.
func (p *Pool) DesktopDiscard(ctx context.Context, workspace string, w Worktree, orphan bool, retry *yesmen.WorktreeDiscardRetry, saveRetry func(yesmen.WorktreeDiscardRetry) error) (*CheckoutObservation, bool, error) {
	remove := saveRetry != nil
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
	if err != nil || id.Ino == 0 || id.Dev == 0 || w.Root != nil && !sameCleanupRoot(*w.Root, id) ||
		retry != nil && (!id.Usable() || id.Ino != retry.Root.Ino || id.Birth != retry.Root.Birth || w.Head != retry.Head || orphan != retry.Orphan) {
		return fail(&RemovalHold{Reason: "The checkout directory is missing or was replaced.", Detail: path})
	}
	var private, admin *os.Root
	var privatePin *safefs.Dir
	defer func() {
		if privatePin != nil {
			privatePin.Close()
		}
	}()
	var privateInfo fs.FileInfo
	var adminName string
	var rootMarker fs.FileInfo
	var recheckIndex func() error
	var gitfile string
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
		marker, err := root.Lstat(".git")
		var data []byte
		missingMarker := errors.Is(err, fs.ErrNotExist) && retry != nil
		switch {
		case missingMarker:
			data = []byte(retry.Gitfile)
		case err != nil || !marker.Mode().IsRegular():
			return errors.New("checkout .git entry changed")
		default:
			data, err = safefs.ReadSmall(pin, ".git", maxGitFile)
			if err != nil || !sameDiscardEntry(root, ".git", marker) {
				return errors.New("checkout .git entry changed")
			}
		}
		if !wellFormedGitfile(data) || retry != nil && string(data) != retry.Gitfile {
			return errors.New("cannot verify the old checkout and its Git pointer")
		}
		rootMarker, gitfile = marker, string(data)
		o.discard = &desktopDiscardObservation{marker: marker, gitfile: gitfile}
		prior := w.observedDiscard
		if remove && (prior == nil || prior.gitfile != gitfile || !sameOptionalDiscardEntry(prior.marker, marker)) {
			return errors.New("checkout Git pointer changed after preview")
		}
		if !w.Detached {
			o.Tracking, err = cleanupTrackingBranch(ctx, r, w.Branch)
			if err != nil || w.observedTracking != nil && *w.observedTracking != o.Tracking {
				return errors.New("the tracking branch changed")
			}
		}
		if orphan {
			// Root provenance is required if the current Git registration
			// cannot establish the checkout instance. Do not touch a stale
			// pointer's destination or repair any old pool registration.
			if w.Root == nil || !w.Root.Usable() {
				return errors.New("cannot verify the old checkout and its Git pointer")
			}
			if err := checkMissingPrivate(r.Path, path, data); err != nil {
				return err
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
			pd, privatePath, err := openPrivateWithBackpointer(r.Path, path, data, missingMarker)
			if err != nil {
				return err
			}
			privatePin = pd
			nonce, err := readInstanceMarker(pd)
			if err != nil || nonce != w.CleanupID {
				return errors.New("private Git instance changed")
			}
			common, err := safefs.ReadSmall(pd, "commondir", maxGitFile)
			if err != nil || strings.TrimSpace(string(common)) != "../.." {
				return errors.New("private Git directory does not use the recorded pool repository")
			}
			if err := p.verifyDiscardRegistration(ctx, r, path, nonce, w, missingMarker); err != nil {
				return err
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
			digest, hasUndo, err := observeDiscardIndex(ctx, pd, path, privatePath, len(w.Head))
			if err != nil {
				return err
			}
			o.discard.resolveUndo, o.discard.hasUndo = digest, hasUndo
			if remove && prior.resolveUndo != digest {
				return ErrWorktreeResolveUndo
			}
			recheckIndex = func() error {
				current, _, err := observeDiscardIndex(ctx, pd, path, privatePath, len(w.Head))
				if err != nil {
					return err
				}
				if current != digest {
					return ErrWorktreeResolveUndo
				}
				return nil
			}
		}
		if err := discardWalk(ctx, parent, root, rootMarker, false, false); err != nil {
			return err
		}
		return checkDesktopWorktreeUse(ctx, path, !remove)
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
	if !id.Usable() {
		o.Close()
		return fail(errors.New("cannot identify this directory for interrupted bulk cleanup"))
	}
	if !remove {
		return o, false, nil
	}
	if !sameDiscardEntry(parent, w.Dir, st) {
		return fail(errors.New("checkout directory changed"))
	}
	if recheckIndex != nil {
		if err := recheckIndex(); err != nil {
			return fail(err)
		}
	}
	// Keep the gitfile during the purge. Record the exact root before
	// unlinking it so a failed rmdir can be reviewed after restart.
	if err := discardWalk(ctx, parent, root, rootMarker, false, true); err != nil {
		return fail(discardPartial(root, err))
	}
	if recheckIndex != nil {
		if err := recheckIndex(); err != nil {
			return fail(discardPartial(root, err))
		}
	}
	if rootMarker != nil {
		if err := checkOnlyDiscardMarker(root); err != nil {
			return fail(discardPartial(root, err))
		}
		current, err := safefs.ReadSmall(pin, ".git", maxGitFile)
		if err != nil || string(current) != gitfile || !sameDiscardEntry(root, ".git", rootMarker) {
			return fail(discardPartial(root, errors.New("checkout Git pointer changed during removal")))
		}
		intent := yesmen.WorktreeDiscardRetry{Root: yesmen.WorktreeRoot{Ino: id.Ino, Birth: id.Birth}, Head: w.Head, Gitfile: gitfile, Orphan: orphan}
		if err := saveRetry(intent); err != nil {
			return fail(discardPartial(root, err))
		}
		if err := ctx.Err(); err != nil {
			return fail(discardPartial(root, err))
		}
		if err := discardEntry(root, ".git", rootMarker); err != nil {
			return fail(discardPartial(root, err))
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(discardPartial(root, err))
	}
	if err := discardEntry(parent, w.Dir, st); err != nil {
		return fail(discardPartial(root, err))
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

func checkMissingPrivate(repo, path string, data []byte) error {
	target, _ := strings.CutPrefix(strings.TrimRight(string(data), "\r\n"), "gitdir: ")
	if !filepath.IsAbs(target) {
		if filepath.VolumeName(target) != "" {
			return errors.New("cannot verify the old Git pointer")
		}
		target = filepath.Join(path, target)
	}
	target = filepath.Clean(target)
	adminPath := filepath.Dir(target)
	if filepath.Base(adminPath) != "worktrees" || realPath(filepath.Dir(adminPath)) != realPath(repo) {
		return errors.New("the old Git pointer does not name this repository's private registration")
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Lstat("worktrees"); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	admin, _, err := discardDir(root, "worktrees")
	if err != nil {
		return err
	}
	defer admin.Close()
	if _, err := admin.Lstat(filepath.Base(target)); !errors.Is(err, fs.ErrNotExist) {
		return errors.New("the old Git pointer's private registration is still present or inaccessible")
	}
	return nil
}

func (p *Pool) verifyDiscardRegistration(ctx context.Context, r Repo, path, nonce string, expected Worktree, missingMarker bool) error {
	if !missingMarker {
		wts, err := p.worktrees(ctx, r)
		if err != nil {
			return err
		}
		for _, w := range wts {
			if w.CleanupID == nonce && realPath(w.Path) == realPath(path) && w.Head == expected.Head && w.Branch == expected.Branch && w.Detached == expected.Detached {
				return nil
			}
		}
		return errors.New("Git registration changed")
	}
	// The ordinary inventory can omit a prunable registration after the
	// confirmed partial removal unlinked the gitfile.
	listed, err := git(ctx, r.Path, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	matches := 0
	for record := range strings.SplitSeq(listed, "\x00\x00") {
		var actual Worktree
		for field := range strings.SplitSeq(record, "\x00") {
			key, val, _ := strings.Cut(field, " ")
			switch key {
			case "worktree":
				actual.Path = val
			case "branch":
				actual.Branch = strings.TrimPrefix(val, "refs/heads/")
			case "HEAD":
				actual.Head = val
			case "detached":
				actual.Detached = true
			}
		}
		if actual.Path != "" && realPath(actual.Path) == realPath(path) && actual.Head == expected.Head && actual.Branch == expected.Branch && actual.Detached == expected.Detached {
			matches++
		}
	}
	if matches != 1 {
		return errors.New("Git registration changed")
	}
	return nil
}

func checkOnlyDiscardMarker(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	names, err := file.Readdirnames(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) != 1 || names[0] != ".git" {
		return errors.New("the checkout directory changed before final removal")
	}
	return nil
}

func discardPartial(root *os.Root, cause error) error {
	const limit = 8
	file, err := root.Open(".")
	if err != nil {
		return &RemovalHold{Reason: "Some files may have been removed. Review this directory again.", Detail: fmt.Sprintf("%v. Cannot list remaining entries: %v", cause, err)}
	}
	defer file.Close()
	names, err := file.Readdirnames(limit + 1)
	detail := cause.Error()
	if err != nil && !errors.Is(err, io.EOF) {
		detail += fmt.Sprintf(". Cannot list remaining entries: %v", err)
	}
	if len(names) > 0 {
		slices.Sort(names)
		detail += fmt.Sprintf(". Remaining entries include %q", names[:min(limit, len(names))])
	}
	return &RemovalHold{Reason: "Some files may have been removed. Review this directory again.", Detail: detail}
}

func sameOptionalDiscardEntry(a, b fs.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b)
}

func wellFormedGitfile(data []byte) bool {
	if len(data) > maxGitFile {
		return false
	}
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
	budget, nameBytes := 1_000_000, 32<<20
	var visit func(*os.Root, string, int) error
	visit = func(dir *os.Root, rel string, depth int) error {
		if depth > 256 {
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
		// Finish this directory's enumeration before removing any entry.
		// Mutating the directory while a ReadDir cursor is active can skip
		// entries. Limit the total number of names across the walk.
		var names []string
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			batch, err := file.Readdirnames(min(128, budget+1))
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			budget -= len(batch)
			for _, name := range batch {
				nameBytes -= len(name)
			}
			if budget < 0 || nameBytes < 0 {
				return &RemovalHold{Reason: "The worktree cleanup scan reached its limit.", Detail: dir.Name()}
			}
			names = append(names, batch...)
			if errors.Is(err, io.EOF) {
				break
			}
		}
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
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
