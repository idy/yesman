package yesmen

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"sync"
)

// Read through the hired workspace's own metadata directory. A lifecycle
// event can arrive when nobody is looking at the workspace, including on
// startup. A pipe or replaced metadata path must never start or stall cleanup.
const maxWorktreeState = 32 << 20

func (s *Store) withWorktreeMeta(y Yesman, fn func(*os.Root) error) error {
	s.lmu.Lock()
	if s.worktreeLocks == nil {
		s.worktreeLocks = map[string]*sync.Mutex{}
	}
	lock := s.worktreeLocks[y.Name]
	if lock == nil {
		lock = &sync.Mutex{}
		s.worktreeLocks[y.Name] = lock
	}
	s.lmu.Unlock()
	lock.Lock()
	defer lock.Unlock()
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
	return fn(meta)
}

func worktreeJSON(meta *os.Root, name string, dst any, strict bool) error {
	data, _, err := readRegularEntry(meta, name, maxWorktreeState, nil, true)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !json.Valid(data) {
		return fmt.Errorf("invalid %s", name)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if strict {
		// Rewriting the registry must never discard a newer version's fields.
		dec.DisallowUnknownFields()
	}
	return dec.Decode(dst)
}

// WorktreeState reads the proof and the current generations needed by the
// service. No session, pool snapshot or model call is needed to find work.
func (s *Store) WorktreeState(y Yesman) (records []WorktreeRecord, board Board, workers []Worker, err error) {
	err = s.withWorktreeMeta(y, func(meta *os.Root) error {
		if err := worktreeJSON(meta, "worktrees.json", &records, true); err != nil {
			return err
		}
		for _, rec := range records {
			if !rec.Removed && (rec.Purpose == "author" || rec.Purpose == "review" || rec.Purpose == "release") {
				// The existing board/worker writers use this lock. In
				// particular a Windows reader must close before their rename.
				s.mu.Lock()
				defer s.mu.Unlock()
				if err := worktreeJSON(meta, "board.json", &board, false); err != nil {
					return err
				}
				return worktreeJSON(meta, "workers.json", &workers, false)
			}
		}
		return nil
	})
	return
}

// TryWorktreeCommit takes the workspace's exclusive lifetime lease only for
// the final local operation. Board updates, worker replacement, checkout and
// archive must happen wholly before or after it. Never wait for an in-flight
// agent startup with the worktree lock held. GitHub requests run before this.
func (s *Store) TryWorktreeCommit(y Yesman, fn func() error) error {
	lock := s.lockFor(y.Name)
	if !lock.TryLock() {
		return errors.New("workspace is in use")
	}
	defer lock.Unlock()
	current, err := s.Get(y.Name)
	if err != nil || y.HiredAt.IsZero() || !y.HiredAt.Equal(current.HiredAt) {
		return errors.New("workspace generation changed")
	}
	return fn()
}

// CommitWorktreeRecord updates just this checkout. Reload under the caller's
// lifecycle lock so unrelated history stays intact and a changed record or
// future schema fails closed, including after an interrupted removal.
func (s *Store) CommitWorktreeRecord(y Yesman, before, after WorktreeRecord) error {
	return s.withWorktreeMeta(y, func(meta *os.Root) error {
		var records []WorktreeRecord
		if err := worktreeJSON(meta, "worktrees.json", &records, true); err != nil {
			return err
		}
		i := slices.IndexFunc(records, func(r WorktreeRecord) bool { return r.ID == before.ID })
		if i < 0 || !reflect.DeepEqual(records[i], before) {
			return errors.New("worktree record changed before the update")
		}
		records[i] = after
		return writeWorktreeRecords(meta, records)
	})
}

func writeWorktreeRecords(meta *os.Root, records []WorktreeRecord) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxWorktreeState {
		return fmt.Errorf("worktree registry exceeds %d bytes", maxWorktreeState)
	}
	name := ".worktrees-" + rand.Text() + ".tmp"
	f, err := meta.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer meta.Remove(name)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	return renameReplace(meta, name, "worktrees.json")
}
