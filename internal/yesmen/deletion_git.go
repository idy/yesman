package yesmen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/idy/yesman/internal/safefs"
)

const (
	gitDeletionFileLimit     = 8 << 10
	gitDeletionConfigLimit   = 64 << 10
	gitDeletionWorktreeLimit = 512
)

func checkCheckoutGit(ctx context.Context, ws *os.Root, volume safefs.Identity, rel string, checkout checkoutObservation) error {
	dir, closeDir, err := checkoutGitDir(ws, rel)
	if err != nil {
		return err
	}
	defer closeDir()
	id, err := directoryIdentity(dir)
	if err != nil || id != checkout.directory {
		return errCheckoutPreviewChanged
	}
	// A separate common directory or a bare repository is marked by its own
	// HEAD and objects directory. Also inspect an ordinary checkout's .git.
	if checkout.Bare {
		for name, expected := range checkout.markers {
			current, err := dir.Lstat(name)
			if err != nil || !sameDeletionEntry(expected, current) {
				return errCheckoutPreviewChanged
			}
		}
		return checkGitRegistrations(ctx, ws, dir, volume, rel)
	}
	// A directory with its own Git data can also contain a .git marker.
	// Verify both so the checkout's marker cannot hide its common data.
	if objects, err := dir.Lstat("objects"); err == nil && objects.IsDir() {
		if _, head := dir.Lstat("HEAD"); head == nil {
			if err := checkGitRegistrations(ctx, ws, dir, volume, rel); err != nil {
				return err
			}
		}
	}
	for name, expected := range checkout.markers {
		current, err := dir.Lstat(name)
		if err != nil || !sameDeletionEntry(expected, current) {
			return errCheckoutPreviewChanged
		}
		if !current.IsDir() {
			// A local linked checkout or symlink only contributes its own file.
			continue
		}
		gitDir, err := directDir(dir, name)
		if err != nil {
			return err
		}
		err = checkGitRegistrations(ctx, ws, gitDir, volume, path.Join(rel, name))
		gitDir.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func checkGitRegistrations(ctx context.Context, ws, gitDir *os.Root, volume safefs.Identity, rel string) error {
	id, err := directoryIdentity(gitDir)
	if err != nil || !sameMount(volume, id) {
		return deletionIssue("Cannot verify the Git checkout directory at %s.", rel)
	}
	if err := checkGitWorktreeConfig(ctx, ws, gitDir, volume, rel); err != nil {
		return err
	}
	registry, err := directDir(gitDir, "worktrees")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return unresolvedGitDeletion(path.Join(rel, "worktrees"))
	}
	defer registry.Close()
	if id, err := directoryIdentity(registry); err != nil || !sameMount(volume, id) {
		return unresolvedGitDeletion(path.Join(rel, "worktrees"))
	}
	limit := gitDeletionWorktreeLimit + 1
	entries, err := readDeletionEntries(ctx, registry, &limit)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return unresolvedGitDeletion(path.Join(rel, "worktrees"))
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := e.Name()
		privateRel := path.Join(rel, "worktrees", name)
		private, err := directDir(registry, name)
		if err != nil {
			return unresolvedGitDeletion(privateRel)
		}
		err = checkGitRegistration(ws, private, volume, privateRel, rel)
		private.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func checkGitWorktreeConfig(ctx context.Context, ws, gitDir *os.Root, volume safefs.Identity, rel string) error {
	configRel := path.Join(rel, "config")
	data, _, err := readRegularEntry(gitDir, "config", gitDeletionConfigLimit, nil, true)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return unresolvedGitDeletion(configRel)
	}
	// Use Git's config grammar on bounded bytes already read through a verified
	// handle. Disable includes and never open a repository or use its config as
	// executable configuration. stdout is limited by the input size.
	parseCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(parseCtx, "git", "config", "--no-includes", "--file", "-", "--null", "--list")
	cmd.Stdin = bytes.NewReader(data)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(value), "GIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return unresolvedGitDeletion(configRel)
	}
	var hasWorktree, nonBare bool
	for entry := range strings.SplitSeq(string(out), "\x00") {
		key, value, _ := strings.Cut(entry, "\n")
		if key == "include.path" || strings.HasPrefix(key, "includeif.") && strings.HasSuffix(key, ".path") {
			// An include can supply core.worktree. Its effects are unverified.
			return unresolvedGitDeletion(configRel)
		}
		if key == "core.bare" && !slices.Contains([]string{"true", "yes", "on", "1"}, strings.ToLower(value)) {
			nonBare = true
		}
		if key != "core.worktree" {
			continue
		}
		hasWorktree = true
		value, err := deletionGitPath([]byte(value))
		if err != nil {
			return unresolvedGitDeletion(configRel)
		}
		worktree := absoluteGitDeletionPath(gitDir.Name(), value)
		linked, local := localGitDeletionPath(ws, worktree)
		if !local {
			return externalGitDeletion(worktree)
		}
		dir, closeDir, err := checkoutGitDir(ws, linked)
		if err != nil {
			return unresolvedGitDeletion(linked)
		}
		id, err := directoryIdentity(dir)
		closeDir()
		if err != nil || !sameMount(volume, id) {
			return unresolvedGitDeletion(linked)
		}
	}
	if nonBare && !hasWorktree && !strings.EqualFold(path.Base(rel), ".git") {
		// A separate Git directory can have no registered primary worktree
		// path. Without a backpointer the checkout cannot be verified.
		return unresolvedGitDeletion(configRel)
	}
	return nil
}

func externalGitDeletion(worktree string) error {
	return deletionIssue("Git data in this workspace is still used by a worktree at %s. Move the repository and its worktrees to safety, or remove the linked worktree with Git first.", worktree)
}

func unresolvedGitDeletion(rel string) error {
	return deletionIssue("Cannot verify the Git worktree at %s. Repair or remove its registration with Git, or move the repository and its worktrees to safety before deleting this workspace.", rel)
}

func checkGitRegistration(ws, private *os.Root, volume safefs.Identity, privateRel, commonRel string) error {
	id, err := directoryIdentity(private)
	if err != nil || !sameMount(volume, id) {
		return unresolvedGitDeletion(privateRel)
	}
	data, _, err := readRegularEntry(private, "gitdir", gitDeletionFileLimit, nil, true)
	if err != nil {
		return unresolvedGitDeletion(path.Join(privateRel, "gitdir"))
	}
	back, err := deletionGitPath(data)
	if err != nil || filepath.Base(back) != ".git" {
		return unresolvedGitDeletion(path.Join(privateRel, "gitdir"))
	}
	back = absoluteGitDeletionPath(private.Name(), back)
	backRel, local := localGitDeletionPath(ws, back)
	if !local {
		return externalGitDeletion(filepath.Dir(back))
	}
	linked, closeLinked, err := checkoutGitDir(ws, path.Dir(backRel))
	if err != nil {
		return unresolvedGitDeletion(backRel)
	}
	defer closeLinked()
	if id, err := directoryIdentity(linked); err != nil || !sameMount(volume, id) {
		return unresolvedGitDeletion(backRel)
	}
	forward, _, err := readRegularEntry(linked, ".git", gitDeletionFileLimit, nil, true)
	if err != nil {
		return unresolvedGitDeletion(backRel)
	}
	prefix, ok := strings.CutPrefix(string(forward), "gitdir: ")
	value, err := deletionGitPath([]byte(prefix))
	if !ok || err != nil {
		return unresolvedGitDeletion(backRel)
	}
	resolved, local := localGitDeletionPath(ws, absoluteGitDeletionPath(linked.Name(), value))
	if !local || resolved != privateRel {
		return unresolvedGitDeletion(backRel)
	}
	common, _, err := readRegularEntry(private, "commondir", gitDeletionFileLimit, nil, true)
	if err != nil {
		return unresolvedGitDeletion(path.Join(privateRel, "commondir"))
	}
	value, err = deletionGitPath(common)
	resolved, local = localGitDeletionPath(ws, absoluteGitDeletionPath(private.Name(), value))
	if err != nil || !local || resolved != commonRel {
		return unresolvedGitDeletion(path.Join(privateRel, "commondir"))
	}
	return nil
}

func absoluteGitDeletionPath(base, name string) string {
	if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	return filepath.Join(base, name)
}

func localGitDeletionPath(ws *os.Root, name string) (string, bool) {
	// Only the trusted workspace root may have an alias. Never resolve the
	// path stored in Git or open it until it is proved workspace-relative.
	root := ws.Name()
	rel, err := filepath.Rel(root, name)
	if err == nil && filepath.IsLocal(rel) {
		return filepath.ToSlash(rel), true
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical == root {
		return "", false
	}
	rel, err = filepath.Rel(canonical, name)
	return filepath.ToSlash(rel), err == nil && filepath.IsLocal(rel)
}

func deletionGitPath(raw []byte) (string, error) {
	value := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if value == "" || strings.ContainsAny(value, "\n\r\x00") || strings.TrimSpace(value) == "" || filepath.VolumeName(value) != "" && !filepath.IsAbs(value) {
		return "", fmt.Errorf("invalid Git path")
	}
	// Git writes absolute paths or an initial chain of parent components.
	// Refuse an embedded parent component whose effect could depend on a link.
	cleaned := strings.TrimPrefix(filepath.ToSlash(value), filepath.ToSlash(filepath.VolumeName(value)))
	cleaned = strings.TrimLeft(cleaned, "/")
	if !filepath.IsAbs(value) {
		for strings.HasPrefix(cleaned, "../") {
			cleaned = strings.TrimPrefix(cleaned, "../")
		}
		if cleaned == ".." {
			return value, nil
		}
	}
	for component := range strings.SplitSeq(cleaned, "/") {
		if component == ".." {
			return "", fmt.Errorf("ambiguous Git path")
		}
	}
	return value, nil
}

func checkoutGitDir(ws *os.Root, rel string) (*os.Root, func(), error) {
	if rel == "." {
		return ws, func() {}, nil
	}
	dir, err := openDeletionChild(ws, rel)
	if err != nil {
		return nil, nil, err
	}
	return dir, func() { dir.Close() }, nil
}
