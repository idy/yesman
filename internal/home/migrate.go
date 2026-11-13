package home

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CheckDestination validates an empty or not-yet-created destination and returns
// its absolute path with existing symbolic links resolved. It makes no changes.
func (h Home) CheckDestination(destination string) (string, error) {
	if !filepath.IsAbs(h.Root) {
		return "", errors.New("the current yesman folder must be an absolute path")
	}
	source, err := filepath.EvalSymlinks(h.Root)
	if err != nil {
		return "", fmt.Errorf("find the current yesman folder: %w", err)
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("the current yesman folder is not a directory")
	}
	if !filepath.IsAbs(destination) {
		return "", errors.New("enter the full path of the new yesman folder")
	}
	destination, err = resolveMissingPath(destination)
	if err != nil {
		return "", fmt.Errorf("find the new yesman folder: %w", err)
	}
	if underPath(destination, source) || underPath(source, destination) {
		return "", errors.New("the new yesman folder must be separate from the current folder")
	}
	// SameFile also catches aliases on case-insensitive filesystems. Missing
	// destination components cannot alias the existing source, but ancestors can.
	for path := destination; ; path = filepath.Dir(path) {
		st, err := os.Stat(path)
		if err == nil && os.SameFile(info, st) {
			return "", errors.New("the new yesman folder is inside the current folder")
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	st, err := os.Stat(destination)
	if errors.Is(err, fs.ErrNotExist) {
		return destination, nil
	}
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", errors.New("the new yesman folder is not a directory")
	}
	for path := source; ; path = filepath.Dir(path) {
		ancestor, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if os.SameFile(st, ancestor) {
			return "", errors.New("the new yesman folder contains the current folder")
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return "", err
	}
	if len(entries) != 0 {
		return "", errors.New("choose an empty folder so existing files are never overwritten")
	}
	return destination, nil
}

// CopyTo prepares a new home without changing or removing the current one. The
// caller must stop agents and other writers before calling it, and persist the
// new home only after it succeeds. A failure removes only its own staging copy.
func (h Home) CopyTo(ctx context.Context, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	destination, err := h.CheckDestination(destination)
	if err != nil {
		return err
	}
	source, err := filepath.EvalSymlinks(h.Root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".yesman-move-*")
	if err != nil {
		return err
	}
	defer removeStaging(stage)
	var dirs []copiedDir
	if err := copyHome(ctx, source, stage, source, destination, &dirs); err != nil {
		return fmt.Errorf("copy yesman data: %w", err)
	}
	if err := repairCopiedWorktrees(ctx, stage, source, destination); err != nil {
		return fmt.Errorf("repair copied worktrees: %w", err)
	}
	if err := repairCopiedGitFiles(ctx, stage, source, destination); err != nil {
		return fmt.Errorf("repair copied git files: %w", err)
	}
	if err := repairCopiedSessions(ctx, stage, source, destination); err != nil {
		return fmt.Errorf("update copied sessions: %w", err)
	}
	// Keep directories writable until every repair is complete, then restore
	// their permissions and times from the leaves towards the root.
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := preserveMetadata(dirs[i].path, dirs[i].info); err != nil {
			return err
		}
	}
	checked, err := h.CheckDestination(destination)
	if err != nil {
		return err
	}
	if checked != destination {
		return errors.New("the destination changed during the copy; choose it again")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Windows cannot rename a directory over an existing empty directory. Only
	// remove an empty target, immediately before publication, and restore it if
	// the rename fails. Remove never deletes a non-empty directory.
	var previous fs.FileInfo
	if st, err := os.Lstat(destination); err == nil {
		previous = st
		if err := os.Remove(destination); err != nil {
			return fmt.Errorf("prepare the empty destination: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(stage, destination); err != nil {
		if previous != nil {
			restoreErr := os.Mkdir(destination, previous.Mode().Perm())
			if restoreErr == nil {
				restoreErr = preserveMetadata(destination, previous)
			}
			err = errors.Join(err, restoreErr)
		}
		return fmt.Errorf("publish the new yesman folder: %w", err)
	}
	return nil
}

type copiedDir struct {
	path string
	info fs.FileInfo
}

func copyHome(ctx context.Context, source, target, oldRoot, newRoot string, dirs *[]copiedDir) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	st, err := os.Lstat(source)
	if err != nil {
		return err
	}
	switch {
	case st.Mode()&os.ModeSymlink != 0:
		link, err := os.Readlink(source)
		if err != nil {
			return err
		}
		// Absolute links into the old home should follow the copy. Relative and
		// external links keep their original targets and are never traversed.
		if next, ok, err := relocatePath(link, oldRoot, newRoot); err != nil {
			return err
		} else if ok {
			link = next
		}
		return os.Symlink(link, target)
	case st.IsDir():
		if source != oldRoot {
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
		}
		*dirs = append(*dirs, copiedDir{target, st})
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyHome(ctx, filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name()), oldRoot, newRoot, dirs); err != nil {
				return err
			}
		}
		return nil
	case st.Mode().IsRegular():
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, contextReader{ctx, in})
		closeErr := out.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
		after, err := in.Stat()
		if err != nil {
			return err
		}
		if st.Size() != after.Size() || !st.ModTime().Equal(after.ModTime()) {
			return fmt.Errorf("%s changed during the copy; stop its writers and try again", source)
		}
		return preserveMetadata(target, st)
	default:
		return fmt.Errorf("cannot copy special file %s (%s)", source, st.Mode().Type())
	}
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func preserveMetadata(path string, st fs.FileInfo) error {
	if err := os.Chmod(path, st.Mode().Perm()|st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)); err != nil {
		return err
	}
	return os.Chtimes(path, st.ModTime(), st.ModTime())
}

func removeStaging(path string) {
	// A failed publication may leave copied read-only directories or files.
	// WalkDir never follows the copied symbolic links.
	_ = filepath.WalkDir(path, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&os.ModeSymlink == 0 {
			if d.IsDir() {
				_ = os.Chmod(path, 0o700)
			} else {
				_ = os.Chmod(path, 0o600)
			}
		}
		return nil
	})
	_ = os.RemoveAll(path)
}

// resolveMissingPath resolves symbolic links in the nearest existing ancestor.
func resolveMissingPath(path string) (string, error) {
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) || filepath.Dir(path) == path {
		return "", err
	}
	parent, err := resolveMissingPath(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

func underPath(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func relocatePath(path, oldRoot, newRoot string) (string, bool, error) {
	if !filepath.IsAbs(path) {
		return path, false, nil
	}
	path = filepath.Clean(path)
	anchor := oldRoot
	if !underPath(path, oldRoot) {
		rootInfo, err := os.Stat(oldRoot)
		if err != nil {
			return "", false, err
		}
		// Find an alias of the root rather than following the final component:
		// a copied symbolic link may be broken, cyclic, or point outside it.
		for anchor = path; ; anchor = filepath.Dir(anchor) {
			info, err := os.Stat(anchor)
			if err == nil && os.SameFile(info, rootInfo) {
				break
			}
			if filepath.Dir(anchor) == anchor {
				return path, false, nil
			}
		}
	}
	rel, err := filepath.Rel(anchor, path)
	if err != nil {
		return "", false, err
	}
	return filepath.Join(newRoot, rel), true, nil
}

// repairCopiedWorktrees updates both sides of each copied worktree link. Running
// git worktree repair before these links were changed could modify the old pool.
func repairCopiedWorktrees(ctx context.Context, stage, oldRoot, newRoot string) error {
	root := filepath.Join(stage, "repos")
	if st, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	} else if !st.IsDir() {
		return errors.New("the repo pool must be a directory, not a symbolic link")
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.IsDir() || !strings.HasSuffix(d.Name(), ".git") {
			return nil
		}
		if _, err := os.Stat(filepath.Join(path, "HEAD")); errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		rel, err := filepath.Rel(stage, path)
		if err != nil {
			return err
		}
		newRepo := filepath.Join(newRoot, rel)
		recordsDir := filepath.Join(path, "worktrees")
		if err := noSymlinkComponents(stage, recordsDir); errors.Is(err, fs.ErrNotExist) {
			return filepath.SkipDir
		} else if err != nil {
			return err
		}
		records, err := os.ReadDir(recordsDir)
		if err != nil {
			return err
		}
		for _, record := range records {
			if !record.IsDir() {
				return fmt.Errorf("unexpected worktree record %s", record.Name())
			}
			gitdirFile := filepath.Join(path, "worktrees", record.Name(), "gitdir")
			data, err := readRegularFile(gitdirFile)
			if err != nil {
				return err
			}
			oldGitFile := strings.TrimSpace(string(data))
			newGitFile, moved, err := relocatePath(oldGitFile, oldRoot, newRoot)
			if err != nil {
				return err
			}
			if !moved {
				return fmt.Errorf("worktree %s is outside the yesman folder; move it inside before relocating storage", oldGitFile)
			}
			gitRel, err := filepath.Rel(newRoot, newGitFile)
			if err != nil || filepath.Base(gitRel) != ".git" {
				return fmt.Errorf("invalid worktree link %s", oldGitFile)
			}
			stageGitFile := filepath.Join(stage, gitRel)
			// Missing worktrees remain prunable; they must not block relocation.
			if err := noSymlinkComponents(stage, stageGitFile); errors.Is(err, fs.ErrNotExist) {
				// There is no checkout to reconnect.
			} else if err != nil {
				return err
			} else {
				data, err := readRegularFile(stageGitFile)
				if err != nil {
					return err
				}
				if !bytes.HasPrefix(data, []byte("gitdir: ")) {
					return fmt.Errorf("invalid worktree git file %s", oldGitFile)
				}
				if err := replaceCopiedFile(stageGitFile, []byte("gitdir: "+filepath.Join(newRepo, "worktrees", record.Name())+"\n")); err != nil {
					return err
				}
			}
			if err := replaceCopiedFile(gitdirFile, []byte(newGitFile+"\n")); err != nil {
				return err
			}
			commonFile := filepath.Join(path, "worktrees", record.Name(), "commondir")
			common, err := readRegularFile(commonFile)
			if err != nil {
				return err
			}
			if next, moved, err := relocatePath(strings.TrimSpace(string(common)), oldRoot, newRoot); err != nil {
				return err
			} else if moved {
				if err := replaceCopiedFile(commonFile, []byte(next+"\n")); err != nil {
					return err
				}
			}
		}
		return filepath.SkipDir
	})
}

// Submodules have their own .git files below a worktree. Their relative links
// survive the copy as-is; absolute links into the old home need the same change.
func repairCopiedGitFiles(ctx context.Context, stage, oldRoot, newRoot string) error {
	for _, area := range []string{"yesmen", "archive"} {
		root := filepath.Join(stage, area)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) && path == root {
				return nil
			}
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.Name() != ".git" {
				return nil
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			if !d.Type().IsRegular() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			pointer, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
			if !ok {
				return nil
			}
			next, moved, err := relocatePath(pointer, oldRoot, newRoot)
			if err != nil {
				return err
			}
			if moved {
				return replaceCopiedFile(path, []byte("gitdir: "+next+"\n"))
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func noSymlinkComponents(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || !underPath(path, root) {
		return errors.New("path is outside the staging directory")
	}
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		root = filepath.Join(root, component)
		st, err := os.Lstat(root)
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("worktree link traverses a symbolic link: %s", path)
		}
	}
	return nil
}

func readRegularFile(path string) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("expected a regular file: %s", path)
	}
	return os.ReadFile(path)
}

func replaceCopiedFile(path string, data []byte) error {
	return rewriteCopiedFile(path, func(_ *os.File, out *os.File) error {
		_, err := out.Write(data)
		return err
	})
}

// rewriteCopiedFile replaces a staging file while keeping its permissions and
// mtime; original session mtimes determine which session pi resumes.
func rewriteCopiedFile(path string, write func(*os.File, *os.File) error) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(path), ".yesman-rewrite-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	writeErr := write(in, out)
	closeErr := out.Close()
	inErr := in.Close() // Windows requires the old handle closed before rename.
	if err := errors.Join(writeErr, closeErr, inErr); err != nil {
		return err
	}
	if err := preserveMetadata(out.Name(), st); err != nil {
		return err
	}
	// Replacing a read-only destination fails on Windows; this is only the
	// disposable staging copy, and the replacement has the original mode.
	if err := os.Chmod(path, st.Mode().Perm()|0o200); err != nil {
		return err
	}
	return os.Rename(out.Name(), path)
}

func repairCopiedSessions(ctx context.Context, stage, oldRoot, newRoot string) error {
	for _, area := range []string{"yesmen", "archive"} {
		root := filepath.Join(stage, area)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) && path == root {
				return nil
			}
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !d.IsDir() || d.Name() != "sessions" || filepath.Base(filepath.Dir(path)) != ".yesman" {
				return nil
			}
			if err := filepath.WalkDir(path, func(session string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".jsonl") {
					return repairSessionHeader(ctx, session, oldRoot, newRoot)
				}
				return nil
			}); err != nil {
				return err
			}
			return filepath.SkipDir
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func repairSessionHeader(ctx context.Context, path, oldRoot, newRoot string) error {
	return rewriteCopiedFile(path, func(in *os.File, out *os.File) error {
		// A malformed log may have no newline at all; bound header memory while
		// preserving its bytes and streaming the rest unchanged.
		reader := bufio.NewReaderSize(in, 1<<20)
		line, err := reader.ReadSlice('\n')
		if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
			return err
		}
		var header map[string]json.RawMessage
		if err != bufio.ErrBufferFull && json.Unmarshal(line, &header) == nil && string(header["type"]) == `"session"` {
			changed := false
			for _, key := range []string{"cwd", "parentSession"} {
				var value string
				if json.Unmarshal(header[key], &value) != nil {
					continue
				}
				next, moved, err := relocatePath(value, oldRoot, newRoot)
				if err != nil {
					return err
				}
				if moved {
					header[key], _ = json.Marshal(next)
					changed = true
				}
			}
			if changed {
				newline := bytes.HasSuffix(line, []byte{'\n'})
				line, err = json.Marshal(header)
				if err != nil {
					return err
				}
				if newline {
					line = append(line, '\n')
				}
			}
		}
		if _, err := out.Write(line); err != nil {
			return err
		}
		_, err = io.Copy(out, contextReader{ctx, reader})
		return err
	})
}
