package yesmen

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/idy/yesman/internal/pool"
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
	// Root identifies the checkout's directory as yesman created it. It is
	// written once, with the record, and never filled in later: records
	// without it (older ones, or a file system with no creation time) get
	// no write/edit grant through the managed entry.
	Root *WorktreeRoot `json:"root,omitempty"`
	// Archive is the user's last move of this instance. Completion, owner,
	// branch and HEAD above still describe the original lifecycle.
	Archive *WorktreeArchive `json:"archive,omitempty"`
}

// WorktreeArchive journals one human-confirmed transition. Keep the last
// transition after restoration so a previous confirmation cannot be replayed.
// Its paths are evidence, never accepted as destinations from the client.
type WorktreeArchive struct {
	pool.LinkedLocation
	Phase       string             `json:"phase"` // archiving, archived, restoring, restored
	Transaction string             `json:"transaction"`
	ConfirmedAt time.Time          `json:"confirmedAt"`
	TaskOwner   string             `json:"taskOwner"`
	Users       []WorktreeMoveUser `json:"users,omitempty"`
	CanceledAt  time.Time          `json:"canceledAt,omitzero"`
}

// WorktreeMoveUser retains a worker generation verified at move admission.
// Its name continues to be reserved against live activity during recovery.
type WorktreeMoveUser struct {
	Name  string    `json:"name"`
	Since time.Time `json:"since"`
}

func (r WorktreeRecord) ReservedForArchive() bool {
	return r.Archive != nil && r.Archive.Phase != "restored"
}

// WorktreeRoot is a checkout directory's inode number (file index on
// Windows) and creation time in nanoseconds, measured through a handle of
// the directory right after the pool created it.
type WorktreeRoot struct {
	Ino   uint64 `json:"ino,string"`
	Birth int64  `json:"birthNs,string"`
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
	y, err := s.Get(name)
	if err != nil {
		return nil, err
	}
	var records []WorktreeRecord
	err = s.WorktreeMoveFile(y, func(rows []WorktreeRecord, _ func(int, WorktreeArchive) error) error {
		records = rows
		return nil
	})
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
