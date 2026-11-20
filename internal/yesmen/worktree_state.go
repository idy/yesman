package yesmen

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// WorktreeState holds the three records used to decide whether a desktop
// removal can proceed. A confirmed removal runs under the workspace's
// exclusive lease so a board edit, rehire, checkout or agent start cannot
// slip between the decision and Git. Other workspaces remain available.
type WorktreeState struct {
	Records  []WorktreeRecord
	Board    Board
	Workers  []Worker
	meta     *os.Root
	original []byte
	store    *Store
}

const maxCleanupMetadata = 32 << 20

func (s *Store) WithWorktreeState(name string, since time.Time, confirm bool, fn func(Yesman, *WorktreeState) error) error {
	if err := ValidName(name); err != nil {
		return err
	}
	l := s.lockFor(name)
	if confirm {
		l.Lock()
		defer l.Unlock()
	} else {
		l.RLock()
		defer l.RUnlock()
	}
	if since.IsZero() {
		return ErrRehired
	}
	y := Yesman{Name: name, Path: filepath.Join(s.root, name), HiredAt: since}
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
	state := &WorktreeState{meta: meta, store: s}
	for _, file := range []struct {
		name   string
		target any
	}{
		{"worktrees.json", &state.Records}, {"board.json", &state.Board}, {"workers.json", &state.Workers},
	} {
		data, err := readRegular(meta, file.name, maxCleanupMetadata, &s.mu)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil {
			err = json.Unmarshal(data, file.target)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", file.name, err)
		}
		if file.name == "worktrees.json" {
			state.original = data
		}
	}
	return fn(y, state)
}

// RecordRemoval preserves the original checkout record, completion and any
// fields written by newer versions. The recovery ref already exists at head.
func (s *WorktreeState) RecordRemoval(index int, head string) error {
	var records []json.RawMessage
	if err := json.Unmarshal(s.original, &records); err != nil {
		return err
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(records[index], &record); err != nil {
		return err
	}
	record["removed"] = json.RawMessage("true")
	record["retiredReason"], _ = json.Marshal("Removed from the Worktrees panel. Recover " + head + " from refs/yesman/worktrees/" + s.Records[index].ID)
	var err error
	if records[index], err = json.Marshal(record); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxCleanupMetadata {
		return errors.New("worktrees.json exceeds its size limit")
	}
	name := ".worktrees-" + rand.Text() + ".tmp"
	tmp, err := s.meta.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer s.meta.Remove(name)
	_, err = tmp.Write(data)
	if err = errors.Join(err, tmp.Sync(), tmp.Close()); err != nil {
		return err
	}
	current, err := readRegular(s.meta, "worktrees.json", maxCleanupMetadata, &s.store.metaMu)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, s.original) {
		return errors.New("worktrees.json changed during removal")
	}
	s.store.metaMu.Lock()
	defer s.store.metaMu.Unlock()
	return renameReplace(s.meta, name, "worktrees.json")
}
