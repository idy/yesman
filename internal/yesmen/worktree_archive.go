package yesmen

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/idy/yesman/internal/safefs"
	"os"
	"path/filepath"
)

// UseWorktreeMove fences workspace dismissal, UI checkout, board changes and
// agent admission for this workspace only. The caller first drains admitted
// agent worktree operations on the server's worktree lock.
func (s *Store) UseWorktreeMove(name string, fn func(Yesman) error) error {
	if err := ValidName(name); err != nil {
		return err
	}
	l := s.lockFor(name)
	l.Lock()
	defer l.Unlock()
	y, err := s.Get(name)
	if err != nil {
		return err
	}
	return fn(y)
}

// WorktreeMoveActors reads the board and worker generations used to authorize
// a move. The exclusive workspace lease fences managed writers. Linked or
// non-regular state files must never supply authorization.
func (s *Store) WorktreeMoveActors(y Yesman) (Board, []Worker, error) {
	ws, meta, err := openMeta(s.root, y, &s.metaMu)
	if ws != nil {
		defer ws.Close()
	}
	if meta != nil {
		defer meta.Close()
	}
	var board Board
	var workers []Worker
	if err != nil {
		return board, nil, err
	}
	for _, file := range []struct {
		name string
		dest any
	}{{"board.json", &board}, {"workers.json", &workers}} {
		data, _, err := readRegularLinks(meta, file.name, 32<<20, &s.metaMu, true)
		if err != nil {
			return board, nil, err
		}
		if err := json.Unmarshal(data, file.dest); err != nil {
			return board, nil, err
		}
	}
	return board, workers, nil
}

func (s *Store) WorktreeArchiveDir() string { return s.archive }

// WorktreeMoveFile reads the original lifecycle evidence through this hire's
// verified metadata directory. Publish patches only the selected archive field
// and checks the bytes read, preserving all historical and unknown fields.
func (s *Store) WorktreeMoveFile(y Yesman, fn func([]WorktreeRecord, func(int, WorktreeArchive) error) error) error {
	ws, meta, err := openMeta(s.root, y, &s.metaMu)
	if ws != nil {
		defer ws.Close()
	}
	if meta != nil {
		defer meta.Close()
	}
	if err != nil {
		return err
	}
	const name = "worktrees.json"
	read := func() ([]byte, error) {
		data, _, err := readRegularLinks(meta, name, 32<<20, &s.metaMu, true)
		return data, err
	}
	data, err := read()
	if err != nil {
		return err
	}
	var records []WorktreeRecord
	if err := json.Unmarshal(data, &records); err != nil {
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
		if len(next) > 32<<20 {
			return errors.New("worktree records exceed the archive journal limit")
		}
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
		current, err := read()
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
		s.metaMu.Lock()
		err = replace(meta, tmp, name)
		s.metaMu.Unlock()
		if err == nil {
			data = next
		}
		return err
	}
	return fn(records, publish)
}

// WorktreePaths resolves from the configured parent. Workspace, archive and
// checkout directory names below it must be real, single path components.
func (s *Store) WorktreePaths(y Yesman, dir, id string) (string, string, error) {
	if !filepath.IsLocal(dir) || filepath.Base(dir) != dir || !filepath.IsLocal(id) || filepath.Base(id) != id {
		return "", "", errors.New("invalid worktree directory or instance")
	}
	wsParent, err := filepath.EvalSymlinks(s.root)
	if err != nil {
		return "", "", err
	}
	archiveParent, err := filepath.EvalSymlinks(filepath.Dir(s.archive))
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
