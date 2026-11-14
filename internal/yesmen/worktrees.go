package yesmen

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// WorktreeRecord tracks agent-created temporary resources, independently of
// worker sessions and board status. Removed records remain as recovery evidence.
// UI/user checkouts and older worktrees without a record are never adopted.
type WorktreeRecord struct {
	ID            string              `json:"id"`
	Dir           string              `json:"dir"`
	Repo          string              `json:"repo"`
	Owner         string              `json:"owner"`
	OwnerSince    time.Time           `json:"ownerSince"`
	Purpose       string              `json:"purpose"`
	Task          string              `json:"task,omitempty"`
	Branch        string              `json:"branch,omitempty"`
	Head          string              `json:"head"`
	CleanupOwner  string              `json:"cleanupOwner,omitempty"`
	CleanupSince  time.Time           `json:"cleanupSince,omitzero"`
	Completion    *WorktreeCompletion `json:"completion,omitempty"`
	Attempts      []WorktreeAttempt   `json:"attempts,omitempty"`
	Removed       bool                `json:"removed,omitempty"`
	RetiredReason string              `json:"retiredReason,omitempty"`
}

// WorktreeAttempt keeps rejected author/release evidence without granting
// removal authority or freezing the owner/lead's ability to correct it.
type WorktreeAttempt struct {
	Completion WorktreeCompletion `json:"completion"`
	Reason     string             `json:"reason"`
}

// WorktreeCompletion is saved before removal is attempted. Ref identifies the
// reviewed PR/ref; Head is the immutable checkout ref used to reconstruct it.
// Evidence must live outside the worktree (board, session or task log).
type WorktreeCompletion struct {
	Head     string    `json:"head"`
	Outcome  string    `json:"outcome"`
	Ref      string    `json:"ref"`
	Evidence string    `json:"evidence"`
	Unused   bool      `json:"unused"`
	SavedAt  time.Time `json:"savedAt,omitzero"`
}

func (s *Store) WorktreeRecords(name string) ([]WorktreeRecord, error) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	y, err := s.get(name)
	if err != nil {
		return nil, err
	}
	var records []WorktreeRecord
	err = readJSON(filepath.Join(y.MetaDir(), "worktrees.json"), &records)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return records, err
}

// SaveWorktreeRecords is used by the serialized checkout/remove API. It never
// removes old evidence when a directory is reused for another checkout.
func (s *Store) SaveWorktreeRecords(name string, records []WorktreeRecord) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	y, err := s.get(name)
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(y.MetaDir(), "worktrees.json"), records)
}
