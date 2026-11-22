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

func (f *ForceDeletion) checkCheckoutGit(ctx context.Context, ws *os.Root, volume safefs.Identity, rel string, checkout checkoutObservation, verified *[]deletionGitRegistration) error {
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
		return f.checkGitRegistrations(ctx, ws, dir, volume, rel, false, verified)
	}
	// A directory with its own Git data can also contain a .git marker.
	// Verify both so the checkout's marker cannot hide its common data.
	if objects, err := dir.Lstat("objects"); err == nil && objects.IsDir() {
		if _, head := dir.Lstat("HEAD"); head == nil {
			if err := f.checkGitRegistrations(ctx, ws, dir, volume, rel, false, verified); err != nil {
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
			// Outward gitfiles and links are only local entries. Inward
			// gitfiles can corroborate a separate directory's ownership.
			continue
		}
		gitDir, err := directDir(dir, name)
		if err != nil {
			return err
		}
		err = f.checkGitRegistrations(ctx, ws, gitDir, volume, path.Join(rel, name), true, verified)
		gitDir.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *ForceDeletion) checkGitRegistrations(ctx context.Context, ws, gitDir *os.Root, volume safefs.Identity, rel string, directMarker bool, verified *[]deletionGitRegistration) error {
	id, err := directoryIdentity(gitDir)
	if err != nil || !sameMount(volume, id) {
		return deletionIssue("Cannot verify the Git checkout directory at %s.", rel)
	}
	for _, name := range []string{"commondir", "gitdir"} {
		// Private linked metadata belongs under a verified common directory's
		// worktrees registry. Here a pointer has no verified owner.
		if _, err := gitDir.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
			return unresolvedGitDeletion(path.Join(rel, name))
		}
	}
	config, err := readGitCommonConfig(ctx, gitDir, rel)
	if err != nil {
		return err
	}
	registry, err := directDir(gitDir, "worktrees")
	if errors.Is(err, fs.ErrNotExist) {
		return f.checkGitWorktreeConfig(ctx, ws, gitDir, volume, rel, directMarker, false, config)
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
		err = f.checkGitRegistration(ctx, ws, private, volume, privateRel, rel, id, config.worktreeConfig, verified)
		private.Close()
		if err != nil {
			return err
		}
	}
	if current, err := readGitCommonConfig(ctx, gitDir, rel); err != nil || current != config {
		return unresolvedGitDeletion(path.Join(rel, "config"))
	}
	return f.checkGitWorktreeConfig(ctx, ws, gitDir, volume, rel, directMarker, len(entries) > 0, config)
}

func readGitDeletionConfig(ctx context.Context, dir *os.Root, name string) (string, error) {
	data, _, err := readRegularEntry(dir, name, gitDeletionConfigLimit, nil, true)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
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
	return string(out), err
}

type gitCommonConfig struct {
	path, contents string
	worktreeConfig bool
}

func readGitCommonConfig(ctx context.Context, gitDir *os.Root, rel string) (gitCommonConfig, error) {
	config := gitCommonConfig{path: path.Join(rel, "config")}
	out, err := readGitDeletionConfig(ctx, gitDir, "config")
	if err != nil {
		return config, unresolvedGitDeletion(config.path)
	}
	config.contents = out
	for record := range strings.SplitSeq(out, "\x00") {
		key, value, _ := strings.Cut(record, "\n")
		if key == "extensions.worktreeconfig" {
			config.worktreeConfig = !gitConfigFalse(value)
		}
	}
	if config.worktreeConfig {
		worktree, err := readGitDeletionConfig(ctx, gitDir, "config.worktree")
		if err != nil {
			return config, unresolvedGitDeletion(path.Join(rel, "config.worktree"))
		}
		if worktree != "" {
			config.path = path.Join(rel, "config.worktree")
			config.contents += worktree
		}
	}
	return config, nil
}

func (f *ForceDeletion) checkGitWorktreeConfig(ctx context.Context, ws, gitDir *os.Root, volume safefs.Identity, rel string, directMarker, hasVerifiedRegistration bool, config gitCommonConfig) error {
	hasWorktree, bare, err := checkConfiguredGitWorktree(ws, gitDir, volume, config.path, config.contents)
	if err != nil || hasWorktree || bare || config.contents == "" {
		return err
	}
	if f.hasLocalGitPointer(ctx, ws, rel) || directMarker && f.hasImplicitGitWorktree(ctx, ws, gitDir, rel, hasVerifiedRegistration) {
		return nil
	}
	return unresolvedGitDeletion(config.path)
}

func (f *ForceDeletion) hasLocalGitPointer(ctx context.Context, ws *os.Root, gitRel string) bool {
	for rel, observation := range f.paths {
		if ctx.Err() != nil {
			return false
		}
		for name, expected := range observation.markers {
			if name != ".git" || !expected.Mode().IsRegular() {
				continue
			}
			worktree, closeDir, err := checkoutGitDir(ws, rel)
			if err != nil {
				continue
			}
			data, stat, err := readRegularEntry(worktree, name, gitDeletionFileLimit, nil, true)
			id, idErr := directoryIdentity(worktree)
			base := worktree.Name()
			closeDir()
			if err != nil || idErr != nil || id != observation.directory || !sameDeletionEntry(expected, stat) {
				continue
			}
			pointer, found := strings.CutPrefix(string(data), "gitdir: ")
			value, err := deletionGitPath([]byte(pointer))
			if !found || err != nil {
				continue
			}
			if resolved, local := localGitDeletionPath(ws, absoluteGitDeletionPath(base, value)); local && resolved == gitRel {
				return true
			}
		}
	}
	return false
}

func (f *ForceDeletion) hasImplicitGitWorktree(ctx context.Context, ws, gitDir *os.Root, gitRel string, hasVerifiedRegistration bool) bool {
	// Git writes no owning-worktree pointer for a conventional checkout.
	// Require a reviewed enclosing checkout and reject relocated metadata
	// that predates it. Empty enclosing directories also need a verified
	// local registration or a pristine Git init with no index or history.
	worktree, ok := f.paths[path.Dir(gitRel)]
	gitID, err := directoryIdentity(gitDir)
	if !ok || err != nil || !gitID.Usable() || !worktree.directory.Usable() || gitID.Birth < worktree.directory.Birth {
		return false
	}
	root, closeRoot, err := checkoutGitDir(ws, path.Dir(gitRel))
	if err != nil {
		return false
	}
	defer closeRoot()
	id, err := directoryIdentity(root)
	if err != nil || id != worktree.directory {
		return false
	}
	limit := 128
	entries, err := readDeletionEntries(ctx, root, &limit)
	for _, entry := range entries {
		if !strings.EqualFold(entry.Name(), ".git") && (entry.Type().IsRegular() || entry.IsDir()) {
			return true
		}
	}
	return err == nil && (hasVerifiedRegistration || pristineGitDirectory(ctx, gitDir, gitID))
}

func pristineGitDirectory(ctx context.Context, dir *os.Root, volume safefs.Identity) bool {
	for _, name := range []string{"index", "packed-refs", "logs", "worktrees", "shallow"} {
		if _, err := dir.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
			return false
		}
	}
	limit := 128
	var empty func(*os.Root, int) bool
	empty = func(root *os.Root, depth int) bool {
		if depth > 4 {
			return false
		}
		entries, err := readDeletionEntries(ctx, root, &limit)
		if err != nil {
			return false
		}
		for _, entry := range entries {
			child, err := directDir(root, entry.Name())
			if err != nil {
				return false
			}
			id, err := directoryIdentity(child)
			ok := err == nil && sameMount(volume, id) && empty(child, depth+1)
			child.Close()
			if !ok {
				return false
			}
		}
		return true
	}
	for _, name := range []string{"objects", "refs"} {
		child, err := directDir(dir, name)
		if err != nil {
			return false
		}
		id, err := directoryIdentity(child)
		ok := err == nil && sameMount(volume, id) && empty(child, 0)
		child.Close()
		if !ok {
			return false
		}
	}
	return true
}

func gitConfigFalse(value string) bool {
	return slices.Contains([]string{"false", "no", "off", "0"}, strings.ToLower(value))
}

func checkConfiguredGitWorktree(ws, gitDir *os.Root, volume safefs.Identity, configRel, out string) (hasWorktree, bare bool, err error) {
	for entry := range strings.SplitSeq(out, "\x00") {
		key, value, _ := strings.Cut(entry, "\n")
		if key == "include.path" || strings.HasPrefix(key, "includeif.") && strings.HasSuffix(key, ".path") {
			// An include can supply core.worktree. Its effects are unverified.
			return false, false, unresolvedGitDeletion(configRel)
		}
		if key == "core.bare" {
			bare = slices.Contains([]string{"true", "yes", "on", "1"}, strings.ToLower(value))
		}
		if key != "core.worktree" {
			continue
		}
		hasWorktree = true
		value, err := deletionGitPath([]byte(value))
		if err != nil {
			return false, false, unresolvedGitDeletion(configRel)
		}
		worktree := absoluteGitDeletionPath(gitDir.Name(), value)
		linked, local := localGitDeletionPath(ws, worktree)
		if !local {
			return false, false, externalGitDeletion(worktree)
		}
		dir, closeDir, err := checkoutGitDir(ws, linked)
		if err != nil {
			return false, false, unresolvedGitDeletion(linked)
		}
		id, err := directoryIdentity(dir)
		closeDir()
		if err != nil || !sameMount(volume, id) {
			return false, false, unresolvedGitDeletion(linked)
		}
	}
	return hasWorktree, bare, nil
}

func externalGitDeletion(worktree string) error {
	return deletionIssue("Git data in this workspace is still used by a worktree at %s. Move the repository and its worktrees to safety, or remove the linked worktree with Git first.", worktree)
}

func unresolvedGitDeletion(rel string) error {
	return deletionIssue("Cannot verify the Git worktree at %s. Repair or remove its registration with Git, or move the repository and its worktrees to safety before deleting this workspace.", rel)
}

func (f *ForceDeletion) checkGitRegistration(ctx context.Context, ws, private *os.Root, volume safefs.Identity, privateRel, commonRel string, commonID safefs.Identity, worktreeConfig bool, verified *[]deletionGitRegistration) error {
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
	if worktreeConfig {
		configRel := path.Join(privateRel, "config.worktree")
		config, err := readGitDeletionConfig(ctx, private, "config.worktree")
		if err != nil {
			return unresolvedGitDeletion(configRel)
		}
		if _, _, err := checkConfiguredGitWorktree(ws, private, volume, configRel, config); err != nil {
			return err
		}
	}
	common, _, err := readRegularEntry(private, "commondir", gitDeletionFileLimit, nil, true)
	if err != nil {
		return unresolvedGitDeletion(path.Join(privateRel, "commondir"))
	}
	value, err := deletionGitPath(common)
	if err != nil {
		return unresolvedGitDeletion(path.Join(privateRel, "commondir"))
	}
	resolved, local := localGitDeletionPath(ws, absoluteGitDeletionPath(private.Name(), value))
	if !local || resolved != commonRel {
		if f.target.Pending && f.stagedGit[privateRel].Private != "" {
			return f.checkStagedGitRegistration(ws, privateRel, commonID, id, data, common)
		}
		return unresolvedGitDeletion(path.Join(privateRel, "commondir"))
	}
	back = absoluteGitDeletionPath(private.Name(), back)
	backRel, local := localGitDeletionPath(ws, back)
	if !local {
		if f.target.Pending && f.stagedGit[privateRel].Private != "" {
			return f.checkStagedGitRegistration(ws, privateRel, commonID, id, data, common)
		}
		return externalGitDeletion(filepath.Dir(back))
	}
	if f.target.Pending && f.stagedGit[privateRel].Private != "" && stagedGitfileAbsent(ws, backRel, volume, f.stagedGit[privateRel].Linked) {
		return f.checkStagedGitRegistration(ws, privateRel, commonID, id, data, common)
	}
	linked, closeLinked, err := checkoutGitDir(ws, path.Dir(backRel))
	if err != nil {
		return unresolvedGitDeletion(backRel)
	}
	defer closeLinked()
	linkedID, err := directoryIdentity(linked)
	if err != nil || !sameMount(volume, linkedID) || !linkedID.Usable() {
		return unresolvedGitDeletion(backRel)
	}
	forward, _, err := readRegularEntry(linked, ".git", gitDeletionFileLimit, nil, true)
	if err != nil {
		return unresolvedGitDeletion(backRel)
	}
	prefix, ok := strings.CutPrefix(string(forward), "gitdir: ")
	value, err = deletionGitPath([]byte(prefix))
	if !ok || err != nil {
		return unresolvedGitDeletion(backRel)
	}
	resolved, local = localGitDeletionPath(ws, absoluteGitDeletionPath(linked.Name(), value))
	if !local || resolved != privateRel {
		return unresolvedGitDeletion(backRel)
	}
	if !id.Usable() || !commonID.Usable() {
		return unresolvedGitDeletion(privateRel)
	}
	if verified != nil {
		if len(*verified) >= gitDeletionWorktreeLimit {
			return unresolvedGitDeletion(path.Dir(privateRel))
		}
		*verified = append(*verified, newDeletionGitRegistration(privateRel, backRel, commonID, id, linkedID, forward, data, common))
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
