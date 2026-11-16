package yesmen

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
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
	// extra keeps the fields of the file this version does not know.
	extra map[string]json.RawMessage
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

const (
	continuationName = "continuation.json"
	// maxContinuationBytes bounds the record: a board holds 500 tasks.
	maxContinuationBytes = 1 << 20
)

var continuationKeys = jsonKeys(reflect.TypeFor[Continuation]())

// ContinuationFile is where a yesman's continuation record is kept.
func (y Yesman) ContinuationFile() string { return filepath.Join(y.MetaDir(), continuationName) }

// continuationLock orders one yesman's continuation reads and writes; a
// slow or damaged record holds up only its own yesman.
func (s *Store) continuationLock(name string) *sync.Mutex {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	if s.clocks == nil {
		s.clocks = map[string]*sync.Mutex{}
	}
	l := s.clocks[name]
	if l == nil {
		l = &sync.Mutex{}
		s.clocks[name] = l
	}
	return l
}

// Continuation reads a yesman's continuation record. One of an earlier hire
// of the same name, or none at all, reads as empty.
func (s *Store) Continuation(name string) (Continuation, error) {
	l := s.continuationLock(name)
	l.Lock()
	defer l.Unlock()
	f, c, err := s.openContinuation(name)
	f.close()
	return c, err
}

// continuationFile is a yesman's record as opened and read: through the
// Store's own folder, its workspace's .yesman, never a link, and the file
// itself only if it is a small regular one, opened without blocking.
type continuationFile struct {
	y     Yesman
	ws    *os.Root
	meta  *os.Root
	found os.FileInfo // nil: there was none
}

func (f *continuationFile) close() {
	if f == nil {
		return
	}
	if f.meta != nil {
		f.meta.Close()
	}
	if f.ws != nil {
		f.ws.Close()
	}
}

func (s *Store) openContinuation(name string) (*continuationFile, Continuation, error) {
	y, err := s.Get(name)
	if err != nil {
		return nil, Continuation{}, err
	}
	f := &continuationFile{y: y}
	f.ws, f.meta, err = openMeta(s.root, y, &s.metaMu)
	if err != nil {
		return f, Continuation{}, fmt.Errorf("%s: %w", y.ContinuationFile(), err)
	}
	c := Continuation{HiredAt: y.HiredAt}
	before, _ := f.meta.Lstat(continuationName)
	data, err := readRegular(f.meta, continuationName, maxContinuationBytes, &sync.Mutex{})
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return f, Continuation{}, fmt.Errorf("%s: %w; it was left as it is", y.ContinuationFile(), err)
	default:
		// The file read is the one found before and after: its identity is
		// what replace checks.
		after, err := f.meta.Lstat(continuationName)
		if err != nil || before == nil || !os.SameFile(before, after) {
			return f, Continuation{}, fmt.Errorf("%s changed while it was read; it was left as it is", y.ContinuationFile())
		}
		f.found = after
		var read Continuation
		if err := decodeContinuation(data, &read); err != nil {
			return f, Continuation{}, fmt.Errorf("%s: %w; it was left as it is", y.ContinuationFile(), err)
		}
		if read.HiredAt.Equal(y.HiredAt) {
			c = read
		} else {
			c.extra = read.extra // another hire's record: its own fields go
		}
	}
	if c.Tasks == nil {
		c.Tasks = map[string]TaskNotice{}
	}
	return f, c, nil
}

func decodeContinuation(data []byte, c *Continuation) error {
	if err := json.Unmarshal(data, c); err != nil {
		return err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	c.extra = unknownFields(obj, continuationKeys...)
	return nil
}

func encodeContinuation(c Continuation) ([]byte, error) {
	known, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(known, &obj); err != nil {
		return nil, err
	}
	for k, v := range c.extra {
		obj[k] = v
	}
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// replace writes the record in place of the one read: to a new temporary
// file in .yesman, synced, then renamed over it, only if the file there is
// still the one read (or still none). A record that would not fit, or a
// file replaced meanwhile, is refused and nothing changes.
func (f *continuationFile) replace(c Continuation) error {
	data, err := encodeContinuation(c)
	if err != nil {
		return err
	}
	if len(data) > maxContinuationBytes {
		return fmt.Errorf("%s: it would grow past %d bytes", f.y.ContinuationFile(), maxContinuationBytes)
	}
	if err := f.unchanged(); err != nil {
		return err
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := "." + continuationName + "." + hex.EncodeToString(nonce[:]) + ".tmp"
	file, err := f.meta.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = f.meta.Remove(tmp)
		}
	}()
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := f.unchanged(); err != nil {
		return err
	}
	if err := f.meta.Rename(tmp, continuationName); err != nil {
		return err
	}
	done = true
	return nil
}

// unchanged checks that the file there is still the one read, or still none.
func (f *continuationFile) unchanged() error {
	cur, err := f.meta.Lstat(continuationName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if f.found != nil {
			return fmt.Errorf("%s was removed after it was read; it was left as it is", f.y.ContinuationFile())
		}
		return nil
	case err != nil:
		return err
	case f.found == nil || !cur.Mode().IsRegular() || !os.SameFile(cur, f.found):
		return fmt.Errorf("%s was replaced after it was read; it was left as it is", f.y.ContinuationFile())
	}
	return nil
}

// UpdateContinuation changes a yesman's continuation record, for the hire
// started at hiredAt only, and writes it when fn reports a change.
func (s *Store) UpdateContinuation(name string, hiredAt time.Time, fn func(*Continuation) bool) (Continuation, error) {
	l := s.continuationLock(name)
	l.Lock()
	defer l.Unlock()
	f, c, err := s.openContinuation(name)
	defer f.close()
	if err != nil {
		return Continuation{}, err
	}
	if !f.y.HiredAt.Equal(hiredAt) {
		return Continuation{}, ErrRehired
	}
	c.HiredAt = hiredAt
	if !fn(&c) {
		return c, nil
	}
	return c, f.replace(c)
}

// ErrRehired refuses a change meant for an earlier hire of a workspace.
var ErrRehired = errors.New("the workspace was hired again")
