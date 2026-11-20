package pool

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/idy/yesman/internal/safefs"
)

// A private registration disappears with git worktree remove. Inventory it
// through directory handles, with fixed limits on bytes, names and objects.
// Anything we cannot reconstruct keeps the checkout in place.
const (
	maxHistoryFile    = 1 << 20
	maxHistoryObjects = 256
)

type privateHistory map[string]string // exact bytes of the recoverable private files

func privateNames(d *safefs.Dir) ([]string, error) {
	const limit = 32
	names, err := d.ReadNames(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(names) > limit {
		return nil, errors.New("too many entries in private Git directory")
	}
	return names, nil
}

func readPrivateHistory(repo Repo, path string, wt Worktree) (privateHistory, error) {
	d, dir, err := openPrivate(repo.Path, path)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	id, err := readInstanceMarker(d)
	if err != nil || id != wt.CleanupID {
		return nil, errors.New("private Git instance changed")
	}
	common, err := safefs.ReadSmall(d, "commondir", maxGitFile)
	if err != nil {
		return nil, err
	}
	shared := strings.TrimSpace(string(common))
	if !filepath.IsAbs(shared) {
		shared = filepath.Join(dir, shared)
	}
	if realPath(shared) != realPath(repo.Path) {
		return nil, errors.New("private Git directory points to another repository")
	}
	history := privateHistory{}
	if err := history.read(d, ""); err != nil {
		return nil, err
	}
	return history, nil
}

func (h privateHistory) read(d *safefs.Dir, prefix string) error {
	names, err := privateNames(d)
	if err != nil {
		return err
	}
	for _, name := range names {
		file := prefix + name
		switch file {
		case "logs", "refs", "refs/heads", "refs/tags":
			child, err := d.OpenDir(name)
			if err != nil {
				return err
			}
			err = h.read(child, file+"/")
			child.Close()
			if err != nil {
				return err
			}
		case "commondir", "gitdir", worktreeInstanceFile:
			// Verified through this directory's handle by openPrivate and its caller.
		case "HEAD":
			if _, err := safefs.ReadSmall(d, name, maxGitFile); err != nil {
				return err
			}
		case "index":
			f, err := d.OpenFile(name, os.O_RDONLY, 0)
			if err != nil {
				return err
			}
			info, err := f.Stat()
			f.Close()
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("irregular private Git file %s", file)
			}
		case "logs/HEAD", "logs/ORIG_HEAD", "ORIG_HEAD", "FETCH_HEAD", "COMMIT_EDITMSG":
			data, err := safefs.ReadSmall(d, name, maxHistoryFile)
			if err != nil {
				return err
			}
			h[file] = string(data)
		default:
			return fmt.Errorf("retain private Git state %q", file)
		}
	}
	return nil
}

func historyOID(oid string, size int) bool {
	return (size == 40 || size == 64) && len(oid) == size && strings.Trim(oid, "0123456789abcdef") == ""
}

// The value is the required object type. FETCH_HEAD can also name a tag.
func (h privateHistory) targets(head string) (map[string]string, error) {
	if !historyOID(head, len(head)) {
		return nil, errors.New("invalid source HEAD")
	}
	oids := map[string]string{head: "commit"}
	zero := strings.Repeat("0", len(head))
	add := func(oid, file string) error {
		if !historyOID(oid, len(head)) {
			return fmt.Errorf("invalid object ID in %s", file)
		}
		if oid == zero && strings.HasPrefix(file, "logs/") {
			return nil // Git's reflog sentinel.
		}
		if _, known := oids[oid]; !known || file != "FETCH_HEAD" {
			oids[oid] = "commit"
			if file == "FETCH_HEAD" {
				oids[oid] = ""
			}
		}
		if len(oids) > maxHistoryObjects {
			return errors.New("private Git history exceeds the object limit")
		}
		return nil
	}
	for file, raw := range h {
		switch file {
		case "ORIG_HEAD":
			if err := add(strings.TrimSpace(raw), file); err != nil {
				return nil, err
			}
		case "logs/HEAD", "logs/ORIG_HEAD", "FETCH_HEAD":
			for line := range strings.SplitSeq(strings.TrimSuffix(raw, "\n"), "\n") {
				if raw == "" {
					break
				}
				if strings.HasPrefix(file, "logs/") {
					old, rest, ok := strings.Cut(line, " ")
					current, _, ok2 := strings.Cut(rest, " ")
					if !ok || !ok2 || add(old, file) != nil || add(current, file) != nil {
						return nil, errors.New("invalid or oversized private HEAD reflog")
					}
				} else {
					oid, _, ok := strings.Cut(line, "\t")
					if !ok || add(oid, file) != nil {
						return nil, errors.New("invalid or oversized private FETCH_HEAD")
					}
				}
			}
		}
	}
	return oids, nil
}

// preserveWorktreeHistory is shared by all managed removal paths. Call it
// under the exclusive checkout lease after source and content checks. Create
// or verify the complete set of immutable recovery refs before Git removes
// the private registration. A retry can use pins left by an interrupted run.
func preserveWorktreeHistory(ctx context.Context, repo Repo, path string, wt Worktree) error {
	if !cleanupIDRE.MatchString(wt.CleanupID) {
		return errors.New("invalid checkout ID")
	}
	h, err := readPrivateHistory(repo, path, wt)
	if err != nil {
		return err
	}
	oids, err := h.targets(wt.Head)
	if err != nil {
		return err
	}
	pins := map[string]string{"refs/yesman/worktrees/" + wt.CleanupID: wt.Head}
	for oid := range oids {
		if oid != wt.Head {
			pins["refs/yesman/worktree-history/"+wt.CleanupID+"/"+oid] = oid
		}
	}
	for name, data := range h {
		oid, err := historyGit(ctx, repo, data, "hash-object", "-w", "--stdin")
		if err != nil {
			return err
		}
		if !historyOID(oid, len(wt.Head)) {
			return errors.New("could not store private Git metadata")
		}
		if oids[oid] == "commit" {
			return fmt.Errorf("private Git history names a metadata blob as a commit in %s", name)
		}
		oids[oid] = "blob"
		pins["refs/yesman/worktree-metadata/"+wt.CleanupID+"/"+name+"/"+oid] = oid
	}
	if err := verifyWorktreeObjects(ctx, repo, oids); err != nil {
		return err
	}
	if err := pinWorktreeObjects(ctx, repo, wt, pins); err != nil {
		return err
	}
	current, err := readPrivateHistory(repo, path, wt)
	if err != nil {
		return err
	}
	if !maps.Equal(h, current) {
		return errors.New("private Git history changed during recovery")
	}
	return nil
}

// Input is a bounded metadata file or a list of validated object/ref names.
func historyGit(ctx context.Context, repo Repo, input string, args ...string) (string, error) {
	cmd := gitCommand(ctx, repo.Path, args...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("preserving worktree history with git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

func verifyWorktreeObjects(ctx context.Context, repo Repo, oids map[string]string) error {
	ids := slices.Sorted(maps.Keys(oids))
	out, err := historyGit(ctx, repo, strings.Join(ids, "\n")+"\n", "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return err
	}
	rows := bufio.NewScanner(strings.NewReader(out))
	for _, id := range ids {
		if !rows.Scan() {
			return errors.New("incomplete private Git object check")
		}
		oid, kind, _ := strings.Cut(rows.Text(), " ")
		if oid != id || !slices.Contains([]string{"commit", "tag", "blob", "tree"}, kind) || oids[id] != "" && oids[id] != kind {
			return fmt.Errorf("missing or unexpected private Git object %s", id)
		}
	}
	return rows.Err()
}

func existingWorktreePins(ctx context.Context, repo Repo, wt Worktree, pins map[string]string) (map[string]bool, error) {
	// Read at most the bounded recovery namespaces for this generation.
	// Dangling symrefs omitted by for-each-ref also make `create` fail.
	out, cut, _, err := gitHead(ctx, repo.Path, 128<<10, "for-each-ref", "--count=513", "--format=%(refname) %(objectname) %(symref)",
		"refs/yesman/worktrees/"+wt.CleanupID, "refs/yesman/worktree-history/"+wt.CleanupID+"/", "refs/yesman/worktree-metadata/"+wt.CleanupID+"/")
	if err != nil || cut {
		return nil, errors.New("could not inspect existing worktree recovery refs")
	}
	exists := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		name, rest, ok := strings.Cut(line, " ")
		if oid, needed := pins[name]; ok && needed {
			if rest != oid+" " {
				return nil, fmt.Errorf("recovery ref %s changed or is symbolic", name)
			}
			exists[name] = true
		}
	}
	return exists, nil
}

func pinWorktreeObjects(ctx context.Context, repo Repo, wt Worktree, pins map[string]string) error {
	exists, err := existingWorktreePins(ctx, repo, wt, pins)
	if err != nil {
		return err
	}
	var transaction strings.Builder
	for _, name := range slices.Sorted(maps.Keys(pins)) {
		action := "create"
		if exists[name] {
			action = "verify"
		}
		fmt.Fprintln(&transaction, action, name, pins[name])
	}
	if _, err := historyGit(ctx, repo, transaction.String(), "update-ref", "--no-deref", "--stdin"); err != nil {
		return err
	}
	confirmed, err := existingWorktreePins(ctx, repo, wt, pins)
	if err != nil || len(confirmed) != len(pins) {
		return errors.New("could not verify the complete immutable worktree recovery refs")
	}
	return nil
}
