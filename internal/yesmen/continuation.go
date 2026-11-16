package yesmen

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Continuation is what yesman remembers of telling a yesman's lead about
// tasks that need a next step, so that neither a restart nor a repeated
// event tells it again about a task that has not changed, and the notices
// stay within a budget. It belongs to one hire of the workspace.
type Continuation struct {
	HiredAt time.Time `json:"hiredAt"`
	// LeadPausedAt is when the user stopped the lead or its run; yesman
	// starts no lead run by itself until the user writes to it again.
	LeadPausedAt time.Time `json:"leadPausedAt,omitzero"`
	// Attempts are when notices were sent, kept for the budget's window,
	// whether or not they were delivered.
	Attempts []time.Time `json:"attempts,omitempty"`
	// Tasks holds, by task ID, the last state the lead was told about.
	Tasks map[string]TaskNotice `json:"tasks,omitempty"`
}

// TaskNotice is what the lead was last told about one task.
type TaskNotice struct {
	// Sig identifies the task's state as told; Progress its status, owner
	// and wait only. Count counts notices since Progress last changed.
	Sig      string    `json:"sig"`
	Progress string    `json:"progress"`
	Count    int       `json:"count"`
	At       time.Time `json:"at"`
	// Delivered is set once pi accepted the notice, never before.
	Delivered bool `json:"delivered,omitempty"`
}

// ContinuationFile is where a yesman's continuation record is kept.
func (y Yesman) ContinuationFile() string { return filepath.Join(y.MetaDir(), "continuation.json") }

var continuationMu sync.Mutex

// Continuation reads a yesman's continuation record. One of an earlier hire
// of the same name, or none at all, reads as empty.
func (s *Store) Continuation(name string) (Continuation, error) {
	continuationMu.Lock()
	defer continuationMu.Unlock()
	_, c, err := s.continuation(name)
	return c, err
}

func (s *Store) continuation(name string) (Yesman, Continuation, error) {
	y, err := s.Get(name)
	if err != nil {
		return Yesman{}, Continuation{}, err
	}
	var c Continuation
	if err := readJSON(y.ContinuationFile(), &c); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Yesman{}, Continuation{}, err
	}
	if !c.HiredAt.Equal(y.HiredAt) {
		c = Continuation{HiredAt: y.HiredAt}
	}
	if c.Tasks == nil {
		c.Tasks = map[string]TaskNotice{}
	}
	return y, c, nil
}

// UpdateContinuation changes a yesman's continuation record, for the hire
// started at hiredAt only, and writes it when fn reports a change.
func (s *Store) UpdateContinuation(name string, hiredAt time.Time, fn func(*Continuation) bool) (Continuation, error) {
	continuationMu.Lock()
	defer continuationMu.Unlock()
	y, c, err := s.continuation(name)
	if err != nil {
		return Continuation{}, err
	}
	if !y.HiredAt.Equal(hiredAt) {
		return Continuation{}, ErrRehired
	}
	if !fn(&c) {
		return c, nil
	}
	return c, writeJSON(y.ContinuationFile(), c)
}

// ErrRehired refuses a change meant for an earlier hire of a workspace.
var ErrRehired = errors.New("the workspace was hired again")
