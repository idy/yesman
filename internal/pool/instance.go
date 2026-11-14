package pool

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const worktreeInstanceFile = "yesman-instance"

// privateWorktreeDir checks the linked worktree's private Git directory and
// its backpointer. The marker belongs to an actual Git instance, not a path or
// workspace JSON record; it grants no agent ownership by itself.
func privateWorktreeDir(repo, path string) (string, error) {
	data, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return "", err
	}
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
	back, err := os.ReadFile(filepath.Join(dir, "gitdir"))
	if err != nil {
		return "", err
	}
	if realPath(strings.TrimSpace(string(back))) != realPath(filepath.Join(path, ".git")) {
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

func readWorktreeInstance(repo, path string) (string, error) {
	dir, err := privateWorktreeDir(repo, path)
	if err != nil {
		return "", err
	}
	marker := filepath.Join(dir, worktreeInstanceFile)
	st, err := os.Lstat(marker)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() || st.Size() != 32 {
		return "", fmt.Errorf("invalid worktree instance marker")
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		return "", err
	}
	id := string(data)
	if !cleanupIDRE.MatchString(id) {
		return "", fmt.Errorf("invalid worktree instance marker")
	}
	return id, nil
}
