package pool

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/idy/yesman/internal/safefs"
)

const worktreeInstanceFile = "yesman-instance"

// The registration files of a linked worktree (its .git file, the private
// Git directory's gitdir backpointer and yesman's instance marker) are read
// as small regular files through handles of their verified parent
// directories, never following a link or reparse point and never waiting on
// a FIFO (internal/safefs). The worktree directory and the private directory
// are themselves opened without following a link; only the workspace and
// the pool repository, trusted locations, are opened by path.

// openWorktree opens the worktree directory at path: its parent by path,
// the directory itself without following a link.
func openWorktree(path string) (*safefs.Dir, error) {
	parent, err := safefs.Open(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	return parent.OpenDir(filepath.Base(path))
}

// privateWorktreeDir checks the linked worktree's private Git directory and
// its backpointer. The marker belongs to an actual Git instance, not a path or
// workspace JSON record; it grants no agent ownership by itself.
func privateWorktreeDir(repo, path string) (string, error) {
	d, dir, err := openPrivate(repo, path)
	if err != nil {
		return "", err
	}
	d.Close()
	return dir, nil
}

// openPrivate reads the .git file of the worktree at path and opens the
// private Git directory it names.
func openPrivate(repo, path string) (*safefs.Dir, string, error) {
	wt, err := openWorktree(path)
	if err != nil {
		return nil, "", err
	}
	data, err := safefs.ReadSmall(wt, ".git", maxGitFile)
	wt.Close()
	if err != nil {
		return nil, "", err
	}
	return openPrivateFrom(repo, path, data)
}

// openPrivateFrom opens the private Git directory that data, the content of
// the worktree's .git file, names: a directory directly below the pool
// repository's worktrees directory, reached from the repository's handle
// without following a link, whose gitdir file points back at path's .git.
func openPrivateFrom(repo, path string, data []byte) (*safefs.Dir, string, error) {
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		return nil, "", errors.New("not a linked worktree")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(path, dir)
	}
	dir = realPath(dir)
	if filepath.Dir(dir) != filepath.Join(realPath(repo), "worktrees") {
		return nil, "", errors.New("private Git directory belongs to another repository")
	}
	rd, err := safefs.Open(repo)
	if err != nil {
		return nil, "", err
	}
	defer rd.Close()
	wd, err := rd.OpenDir("worktrees")
	if err != nil {
		return nil, "", err
	}
	defer wd.Close()
	pd, err := wd.OpenDir(filepath.Base(dir))
	if err != nil {
		return nil, "", err
	}
	back, err := safefs.ReadSmall(pd, "gitdir", maxGitFile)
	if err != nil {
		pd.Close()
		return nil, "", err
	}
	backPath := strings.TrimSpace(string(back))
	if !filepath.IsAbs(backPath) {
		backPath = filepath.Join(dir, backPath)
	}
	if realPath(backPath) != realPath(filepath.Join(path, ".git")) {
		pd.Close()
		return nil, "", errors.New("private Git directory belongs to another worktree")
	}
	return pd, dir, nil
}

// newWorktreeInstance is called only after git creates a NEW linked worktree,
// under the repository lock. Every pool checkout (UI/user and agent) gets a new
// nonce. Existing/legacy/unknown trees are never given or repaired a marker.
func newWorktreeInstance(repo, path string) (string, error) {
	pd, _, err := openPrivate(repo, path)
	if err != nil {
		return "", err
	}
	defer pd.Close()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(nonce[:])
	f, err := pd.OpenFile(worktreeInstanceFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	_, writeErr := f.WriteString(id)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return "", err
	}
	return id, nil
}

// rootIdentity is called right after newWorktreeInstance, under the same
// locks: it opens the new checkout's directory without following a link,
// checks through that handle that it carries instance id, and returns the
// directory's identity. A file system that reports no creation time, or a
// zero one (ambiguous with none), gives no identity, and no write grant
// follows from it.
func rootIdentity(repo, path, id string) (*safefs.Identity, error) {
	wt, ident, err := openInstanceRoot(repo, path, id)
	if err != nil {
		return nil, err
	}
	defer wt.Close()
	if !ident.Usable() {
		return nil, errors.New("the file system reports no usable creation time for the checkout's directory")
	}
	return &ident, nil
}

// openInstanceRoot verifies the nonce and both Git pointers through the same
// directory handle whose identity it returns. The caller owns that handle.
func openInstanceRoot(repo, path, id string) (*safefs.Dir, safefs.Identity, error) {
	wt, err := openWorktree(path)
	if err != nil {
		return nil, safefs.Identity{}, err
	}
	ident, err := func() (safefs.Identity, error) {
		data, err := safefs.ReadSmall(wt, ".git", maxGitFile)
		if err != nil {
			return safefs.Identity{}, err
		}
		pd, _, err := openPrivateFrom(repo, path, data)
		if err != nil {
			return safefs.Identity{}, err
		}
		defer pd.Close()
		got, err := readInstanceMarker(pd)
		if err != nil {
			return safefs.Identity{}, err
		}
		if got != id {
			return safefs.Identity{}, errors.New("checkout instance changed")
		}
		return wt.Identity()
	}()
	if err != nil {
		wt.Close()
		return nil, safefs.Identity{}, err
	}
	return wt, ident, nil
}

// InstanceOf returns the private instance ID of the worktree at path of the
// pool repository name, given dotGit, the content of its .git file as the
// caller read it through its own handle of the worktree. It reads only small
// regular files of the pool, never waiting on one. It identifies the
// checkout; it never grants ownership by itself.
func (p *Pool) InstanceOf(name, path string, dotGit []byte) (string, error) {
	pd, _, err := openPrivateFrom(p.dir(name), path, dotGit)
	if err != nil {
		return "", err
	}
	defer pd.Close()
	return readInstanceMarker(pd)
}

// SameCheckout checks a cached pool inventory entry without rescanning all Git
// repositories. It verifies the private marker and both registration pointers.
func (p *Pool) SameCheckout(w Worktree) bool {
	id, err := readWorktreeInstance(p.dir(w.Repo), w.Path)
	return err == nil && id == w.CleanupID
}

// WithCheckoutFiles gives a desktop file request a root at its exact checkout.
// Compare both opened handles so a directory replaced between opens is refused.
// Holding the shared worktree lease also orders a pending save with removal.
func (p *Pool) WithCheckoutFiles(workspace, repo, dir, id string, fn func(*os.Root) error) error {
	if !filepath.IsLocal(dir) || filepath.Base(dir) != dir || dir == "." || !cleanupIDRE.MatchString(id) {
		return errors.New("invalid checkout identity")
	}
	for segment := range strings.SplitSeq(repo, "/") {
		if !validSegment(segment) {
			return errors.New("invalid checkout repository")
		}
	}
	path := filepath.Join(workspace, dir)
	return p.WithWorktree(path, func() error {
		wt, _, err := openInstanceRoot(p.dir(repo), path, id)
		if err != nil {
			return err
		}
		defer wt.Close()
		info, err := wt.Stat()
		if err != nil {
			return err
		}
		root, err := os.OpenRoot(path)
		if err != nil {
			return err
		}
		defer root.Close()
		opened, err := root.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			return errors.New("checkout directory changed")
		}
		return fn(root)
	})
}

// WithWorktree runs fn while no checkout or removal by the pool can create,
// replace or remove the worktree at path. Several fn may run at once; the
// pool's own operations on path wait for them, and fn waits for those.
// Lock order: a caller may hold the workspace's lease and an agent's change
// lease; the pool takes the repository lock before this one, never after.
func (p *Pool) WithWorktree(path string, fn func() error) error {
	l := p.treeLock(path)
	l.RLock()
	defer l.RUnlock()
	return fn()
}

// treeLock is the lock of the worktree directory at path. The pool takes
// it, after the repository's lock, while it adds or removes that directory.
func (p *Pool) treeLock(path string) *sync.RWMutex {
	key := filepath.Clean(path)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.trees == nil {
		p.trees = map[string]*sync.RWMutex{}
	}
	l := p.trees[key]
	if l == nil {
		l = &sync.RWMutex{}
		p.trees[key] = l
	}
	return l
}

func readWorktreeInstance(repo, path string) (string, error) {
	pd, _, err := openPrivate(repo, path)
	if err != nil {
		return "", err
	}
	defer pd.Close()
	return readInstanceMarker(pd)
}

func readInstanceMarker(pd *safefs.Dir) (string, error) {
	data, err := safefs.ReadSmall(pd, worktreeInstanceFile, 32)
	if err != nil {
		return "", err
	}
	id := string(data)
	if len(data) != 32 || !cleanupIDRE.MatchString(id) {
		return "", fmt.Errorf("invalid worktree instance marker")
	}
	return id, nil
}

// maxGitFile bounds a .git or gitdir file: one path.
const maxGitFile = 8 << 10
