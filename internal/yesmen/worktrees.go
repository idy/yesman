package yesmen

import (
	"errors"
	"os"
	"time"
)

// WorktreeRecord tracks agent-created temporary resources, independently of
// worker sessions and board status. Removed records remain as recovery evidence.
// UI/user checkouts and older worktrees without a record are never adopted.
type WorktreeRecord struct {
	ID         string    `json:"id"`
	Dir        string    `json:"dir"`
	Repo       string    `json:"repo"`
	Owner      string    `json:"owner"`
	OwnerSince time.Time `json:"ownerSince"`
	Purpose    string    `json:"purpose"`
	Task       string    `json:"task,omitempty"`
	// CreatedFor binds Task to its workspace hire and immutable Board identity.
	CreatedFor *WorktreeCreation `json:"createdFor,omitempty"`
	// CleanupHead is recorded under the removal lease for recovery if the
	// post-Git registry write is interrupted. It grants no cleanup authority.
	CleanupHead   string              `json:"cleanupHead,omitempty"`
	Branch        string              `json:"branch,omitempty"`
	Head          string              `json:"head"`
	CleanupOwner  string              `json:"cleanupOwner,omitempty"`
	CleanupSince  time.Time           `json:"cleanupSince,omitzero"`
	Completion    *WorktreeCompletion `json:"completion,omitempty"`
	Attempts      []WorktreeAttempt   `json:"attempts,omitempty"`
	Removed       bool                `json:"removed,omitempty"`
	RetiredReason string              `json:"retiredReason,omitempty"`
	// BoardRound binds an automatic completion to the particular task close.
	BoardRound time.Time `json:"boardRound,omitzero"`
	// Superseded Board certifications remain available for audit and exact
	// recovery. Only the current Board disposition can authorize collection.
	PriorReturns []VerifiedWorktreeReturn `json:"priorReturns,omitempty"`
	// Root identifies the checkout's directory as yesman created it. It is
	// written once, with the record, and never filled in later: records
	// without it (older ones, or a file system with no creation time) get
	// no write/edit grant through the managed entry.
	Root *WorktreeRoot `json:"root,omitempty"`
	// DiscardRetry identifies a bulk-confirmed directory whose final unlink
	// may have been interrupted. It grants no managed write or auto cleanup.
	DiscardRetry *WorktreeDiscardRetry `json:"discardRetry,omitempty"`
}

// WorktreeDiscardRetry is written before unlinking the checkout's gitfile.
// A fresh desktop preview still verifies the path, owners and Git state.
type WorktreeDiscardRetry struct {
	Root    WorktreeRoot `json:"root"`
	Head    string       `json:"head"`
	Gitfile string       `json:"gitfile"`
	Orphan  bool         `json:"orphan,omitempty"`
}

type WorktreeCreation struct {
	Workspace string    `json:"workspace"`
	HiredAt   time.Time `json:"hiredAt"`
	TaskAt    time.Time `json:"taskCreatedAt"`
}

type VerifiedWorktreeReturn struct {
	Round      time.Time          `json:"round"`
	Completion WorktreeCompletion `json:"completion"`
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
	// SourceBranch is the explicitly reported PR head ref when this local
	// branch was pushed under a different name. The local branch itself is
	// still checked against the original checkout registration.
	SourceBranch string `json:"sourceBranch,omitempty"`
	// Publication identifies the publisher's external publication and verification receipt.
	Publication string `json:"publication,omitempty"`
	// The server sets the identity of the actor returning this exact instance.
	CertifiedBy    string    `json:"certifiedBy,omitempty"`
	CertifiedSince time.Time `json:"certifiedSince,omitzero"`
	// ReturnID distinguishes repeat submissions of the same Board result,
	// including when the system clock repeats an earlier time.
	ReturnID string `json:"returnId,omitempty"`
}

// WorktreeReturn accompanies the normal task result on the Board. It releases
// this exact temporary checkout for collection. A proposed return can wait
// for a merge. Only the verified Completion in the registry freezes the HEAD.
type WorktreeReturn struct {
	Checkout string `json:"checkout"`
	WorktreeCompletion
}

func (s *Store) WorktreeRecords(name string) ([]WorktreeRecord, error) {
	y, err := s.Get(name)
	if err != nil {
		return nil, err
	}
	var records []WorktreeRecord
	err = s.withWorktreeMeta(y, func(meta *os.Root) error {
		return worktreeJSON(meta, "worktrees.json", &records, true)
	})
	return records, err
}

// SaveCheckout binds the new instance to the Board task admitted before Git.
// Board and worker writers share this lock. If the assignment changed while
// Git was running, the directory stays protected as an unregistered checkout.
// The caller holds the server's worktree lock and process operation lease.
func (s *Store) SaveCheckout(y Yesman, records []WorktreeRecord, admitted *Task) error {
	defer s.lockReturnState(y.Name)()
	return s.withWorktreeMeta(y, func(meta *os.Root) error {
		if admitted != nil {
			var board Board
			s.mu.Lock()
			err := worktreeJSON(meta, "board.json", &board, false)
			s.mu.Unlock()
			if err != nil {
				return err
			}
			i := board.index(admitted.ID)
			if i < 0 || !sameCheckoutAssignment(*admitted, board.Tasks[i]) {
				return errors.New("the creating task changed during checkout")
			}
		}
		return writeWorktreeRecords(meta, records)
	})
}

func sameCheckoutAssignment(a, b Task) bool {
	return a.ID == b.ID && !b.CreatedAt.IsZero() && a.CreatedAt.Equal(b.CreatedAt) &&
		a.Owner == b.Owner && a.OwnerSince.Equal(b.OwnerSince) && b.Open() && !b.Paused() &&
		a.WorktreeGeneration == b.WorktreeGeneration
}

// SaveWorktreeRecords writes the serialized lifecycle registry without
// removing old evidence when a directory is reused for another checkout.
func (s *Store) SaveWorktreeRecords(name string, records []WorktreeRecord) error {
	y, err := s.Get(name)
	if err != nil {
		return err
	}
	return s.withWorktreeMeta(y, func(meta *os.Root) error { return writeWorktreeRecords(meta, records) })
}
