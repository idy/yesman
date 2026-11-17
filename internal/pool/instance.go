package pool

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const worktreeInstanceFile = "yesman-instance"

// privateWorktreeDir checks the linked worktree's private Git directory and
// its backpointer. The marker belongs to an actual Git instance, not a path or
// workspace JSON record; it grants no agent ownership by itself.
func privateWorktreeDir(repo, path string) (string, error) {
	data, err := readSmall(filepath.Join(path, ".git"), maxGitFile)
	if err != nil {
		return "", err
	}
	return privateWorktreeDirFrom(repo, path, data)
}

// privateWorktreeDirFrom checks the private Git directory that data, the
// content of the worktree's .git file, names.
func privateWorktreeDirFrom(repo, path string, data []byte) (string, error) {
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		return "", errors.New("not a linked worktree")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(path, dir)
	}
	dir = realPath(dir)
	if filepath.Dir(dir) != filepath.Join(realPath(repo), "worktrees") {
		return "", errors.New("private Git directory belongs to another repository")
	}
	back, err := readSmall(filepath.Join(dir, "gitdir"), maxGitFile)
	if err != nil {
		return "", err
	}
	backPath := strings.TrimSpace(string(back))
	if !filepath.IsAbs(backPath) {
		backPath = filepath.Join(dir, backPath)
	}
	if realPath(backPath) != realPath(filepath.Join(path, ".git")) {
		return "", errors.New("private Git directory belongs to another worktree")
	}
	return dir, nil
}

// newWorktreeInstance is called only after git creates a NEW linked worktree,
// under the repository lock. Every pool checkout (UI/user and agent) gets a new
// nonce. Existing/legacy/unknown trees are never given or repaired a marker.
func newWorktreeInstance(repo, path string) (string, error) {
	dir, err := privateWorktreeDir(repo, path)
	if err != nil {
		return "", err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(nonce[:])
	f, err := os.OpenFile(filepath.Join(dir, worktreeInstanceFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	_, writeErr := f.WriteString(id)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return "", err
	}
	return id, nil
}

// InstanceOf returns the private instance ID of the worktree at path of the
// pool repository name, given dotGit, the content of its .git file as the
// caller read it through its own handle of the worktree. It reads only small
// regular files of the pool, never waiting on one. It identifies the
// checkout; it never grants ownership by itself.
func (p *Pool) InstanceOf(name, path string, dotGit []byte) (string, error) {
	dir, err := privateWorktreeDirFrom(p.dir(name), path, dotGit)
	if err != nil {
		return "", err
	}
	return readInstanceMarker(dir)
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
	dir, err := privateWorktreeDir(repo, path)
	if err != nil {
		return "", err
	}
	return readInstanceMarker(dir)
}

func readInstanceMarker(dir string) (string, error) {
	data, err := readSmall(filepath.Join(dir, worktreeInstanceFile), 32)
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

// readSmall reads the regular file at path, without following a final link
// or waiting on a FIFO, refusing one larger than limit.
func readSmall(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|noFollowNonblock, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("%s is larger than expected", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than expected", path)
	}
	return data, nil
}
