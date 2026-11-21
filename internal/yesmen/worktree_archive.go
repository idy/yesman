package yesmen

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/idy/yesman/internal/safefs"
)

// Archive uses the same workspace and task-decision leases as final removal.
func (s *Store) UseWorktreeMove(name string, fn func(Yesman) error) error {
	y, err := s.Get(name)
	if err != nil {
		return err
	}
	return s.TryWorktreeCommit(y, func() error { return fn(y) })
}

// WorktreeMoveActors returns the same Board and worker generations used to fence cleanup.
func (s *Store) WorktreeMoveActors(y Yesman) (Board, []Worker, error) {
	_, board, workers, err := s.WorktreeState(y)
	return board, workers, err
}

func (s *Store) WorktreeArchiveDir() string { return s.archive }

// WorktreeMoveFile patches only this instance's archive journal. Registry
// access shares the store's per-workspace lock with checkout and retirement.
// The caller holds the workspace commit lease across the Git operation. Each
// publication compares the last observed registry before replacing it.
func (s *Store) WorktreeMoveFile(y Yesman, fn func([]WorktreeRecord, func(int, WorktreeArchive) error) error) error {
	const name = "worktrees.json"
	var data []byte
	var records []WorktreeRecord
	err := s.withWorktreeMeta(y, func(meta *os.Root) error {
		var err error
		data, _, err = readRegularEntry(meta, name, maxWorktreeState, nil, true)
		if err != nil {
			return err
		}
		if !json.Valid(data) {
			return errors.New("invalid worktree registry")
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		return dec.Decode(&records)
	})
	if err != nil {
		return err
	}
	publish := func(i int, move WorktreeArchive) error {
		var raw []map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		value, err := json.Marshal(move)
		if err != nil {
			return err
		}
		raw[i]["archive"] = value
		next, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return err
		}
		next = append(next, '\n')
		if len(next) > maxWorktreeState {
			return errors.New("worktree records exceed the archive journal limit")
		}
		return s.withWorktreeMeta(y, func(meta *os.Root) error {
			tmp := ".worktree-move-" + rand.Text()
			out, err := meta.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			defer meta.Remove(tmp)
			_, err = out.Write(next)
			if err == nil {
				err = out.Sync()
			}
			if err = errors.Join(err, out.Close()); err != nil {
				return err
			}
			current, _, err := readRegularEntry(meta, name, maxWorktreeState, nil, true)
			if err != nil {
				return err
			}
			if !bytes.Equal(data, current) {
				return errors.New("worktree records changed during confirmation")
			}
			replace := s.ReplaceWorktreeJournal
			if replace == nil {
				replace = safefs.ReplaceSynced
			}
			if err := replace(meta, tmp, name); err != nil {
				return err
			}
			data = next
			return nil
		})
	}
	return fn(records, publish)
}

// WorktreePaths resolves from the configured parent. Workspace, archive and
// checkout directory names below it must be real, single path components.
func (s *Store) WorktreePaths(y Yesman, dir, id string) (string, string, error) {
	if !filepath.IsLocal(dir) || filepath.Base(dir) != dir || !filepath.IsLocal(id) || filepath.Base(id) != id {
		return "", "", errors.New("invalid worktree directory or instance")
	}
	wsParent, err := safefs.PhysicalPath(s.root)
	if err != nil {
		return "", "", err
	}
	archiveParent, err := safefs.PhysicalPath(filepath.Dir(s.archive))
	if err != nil {
		return "", "", err
	}
	archive := filepath.Join(archiveParent, filepath.Base(s.archive))
	st, err := os.Lstat(archive)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("archive must be an existing directory of the data folder")
	}
	return filepath.Join(wsParent, y.Name, dir), filepath.Join(archive, "worktree-"+id), nil
}
