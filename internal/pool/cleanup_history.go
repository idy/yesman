package pool

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/idy/yesman/internal/safefs"
)

const maxPrivateReflog = 8 << 20

// privateHistory reads through the verified private Git directory. Git's
// reflog presentation omits each entry's old OID, which can be the last
// reference to a detached commit after a gap in logging.
func privateHistory(repo, path string, wt Worktree) ([]string, error) {
	pd, _, err := openPrivate(repo, path)
	if err != nil {
		return nil, err
	}
	defer pd.Close()
	if id, err := readInstanceMarker(pd); err != nil || id != wt.CleanupID {
		return nil, errors.New("the checkout changed while reading its private history")
	}
	width := len(wt.Head)
	if width != 40 && width != 64 {
		return nil, errors.New("unknown Git object format")
	}
	seen := map[string]bool{wt.Head: true}
	zero := strings.Repeat("0", width)
	var heads []string
	add := func(field []byte, allowZero bool) error {
		if len(field) != width {
			return errors.New("invalid object name in private Git history")
		}
		if _, err := hex.Decode(make([]byte, width/2), field); err != nil {
			return errors.New("invalid object name in private Git history")
		}
		oid := strings.ToLower(string(field))
		if oid == zero {
			if allowZero {
				return nil
			}
			return errors.New("invalid zero ORIG_HEAD")
		}
		if !seen[oid] {
			heads = append(heads, oid)
			seen[oid] = true
		}
		return nil
	}
	orig, err := safefs.ReadSmall(pd, "ORIG_HEAD", int64(width+2))
	if err == nil {
		err = add(bytes.TrimSuffix(bytes.TrimSuffix(orig, []byte("\n")), []byte("\r")), false)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	logs, err := pd.OpenDir("logs")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if logs != nil {
		defer logs.Close()
		log, err := safefs.ReadSmall(logs, "HEAD", maxPrivateReflog)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		for len(log) > 0 {
			line, rest, complete := bytes.Cut(log, []byte("\n"))
			if !complete || len(line) <= 2*width+2 || line[width] != ' ' || line[2*width+1] != ' ' {
				return nil, errors.New("incomplete private HEAD reflog entry")
			}
			if err := add(line[:width], true); err != nil {
				return nil, err
			}
			if err := add(line[width+1:2*width+1], true); err != nil {
				return nil, err
			}
			log = rest
		}
	}
	slices.Sort(heads)
	return heads, nil
}

// readRecoverableHistory checks object type and availability before a checkout
// can enter a preview or be removed. Read the objects without replacement refs.
func readRecoverableHistory(ctx context.Context, repo Repo, path string, wt Worktree) ([]string, error) {
	heads, err := privateHistory(repo.Path, path, wt)
	if err != nil {
		return nil, &RemovalHold{"The checkout’s Git history could not be verified.", err.Error()}
	}
	for batch := range slices.Chunk(heads, 64) {
		types, err := gitInput(ctx, repo.Path, strings.Join(batch, "\n")+"\n", "--no-replace-objects", "cat-file", "--batch-check=%(objecttype)")
		if err != nil {
			return nil, err
		}
		kinds := strings.Fields(types)
		if len(kinds) != len(batch) || slices.ContainsFunc(kinds, func(kind string) bool { return kind != "commit" }) {
			return nil, &RemovalHold{"The checkout’s Git history could not be verified.", "ORIG_HEAD / logs/HEAD"}
		}
	}
	return heads, nil
}

// Pin private history outside the durable refs. Re-read the set at final
// admission so a new ORIG_HEAD or reflog entry requires a fresh attempt.
func preserveWorktreeHistory(ctx context.Context, repo Repo, wt Worktree, heads []string) error {
	for batch := range slices.Chunk(heads, 64) {
		// Bound Git's output as well as the private metadata. If the walk
		// hits the bound, pin the entire batch conservatively.
		args := append([]string{"--no-replace-objects", "rev-list", "--max-count=65"}, batch...)
		args = append(args, "--not", wt.Head, "--branches", "--tags", "--glob=refs/yesman/*")
		unanchored, err := git(ctx, repo.Path, args...)
		if err != nil {
			return err
		}
		unanchoredIDs := strings.Fields(unanchored)
		for _, oid := range batch {
			if len(unanchoredIDs) < 65 && !slices.Contains(unanchoredIDs, oid) {
				continue
			}
			ref := "refs/yesman/worktree-history/" + wt.CleanupID + "/" + oid
			if _, err := git(ctx, repo.Path, "symbolic-ref", "--quiet", ref); err == nil {
				return &RemovalHold{"The recovery reference points elsewhere.", ref}
			}
			if old, err := git(ctx, repo.Path, "show-ref", "--verify", "--hash", ref); err == nil {
				if old != oid {
					return &RemovalHold{"The recovery reference already preserves another commit.", ref}
				}
			} else if _, err := git(ctx, repo.Path, "update-ref", "--no-deref", ref, oid, strings.Repeat("0", len(oid))); err != nil {
				return err
			}
			if got, err := git(ctx, repo.Path, "show-ref", "--verify", "--hash", ref); err != nil || got != oid {
				return fmt.Errorf("cannot verify saved history reference %s", ref)
			}
		}
	}
	return nil
}
