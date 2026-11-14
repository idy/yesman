package yesmen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// WorkerStart is one start of a worker under a name. Removing a worker
// forgets its record but keeps its session, which a worker started later
// under the name continues; the starts kept here tell what each start did
// in that session apart, for Settings › Model usage.
type WorkerStart struct {
	Name      string    `json:"name"`
	Number    int       `json:"number,omitempty"`
	StartedAt time.Time `json:"startedAt"`
}

// StartHistory is the worker starts a yesman keeps, oldest first, and
// how far back they are complete.
type StartHistory struct {
	Starts []WorkerStart
	// Forgotten is when the latest start dropped to bound the history
	// started: starts at or before it may be missing, those after it are
	// all kept. Zero when none was dropped.
	Forgotten time.Time
}

// maxStarts bounds the starts kept; the oldest go first.
const maxStarts = 10000

// startsFile keeps every worker start, including removed workers', up to
// maxStarts: a list of starts, until one is dropped; from then on
// {"starts": [...], "forgotten": time}, keeping any other fields as they
// are. A full list may have been written without saying a start was
// dropped, so it counts as having dropped starts up to its oldest.
func (y Yesman) startsFile() string { return filepath.Join(y.MetaDir(), "worker-starts.json") }

// WorkerStarts returns the recorded starts of a yesman's workers, oldest
// first: those of workers started or removed since yesman kept them, and
// of the workers there were then.
func (s *Store) WorkerStarts(name string) ([]WorkerStart, error) {
	h, err := s.StartHistory(name)
	return h.Starts, err
}

// StartHistory returns the recorded starts of a yesman's workers, as
// WorkerStarts, with how far back they are complete.
func (s *Store) StartHistory(name string) (StartHistory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	y, err := s.Get(name)
	if err != nil {
		return StartHistory{}, err
	}
	h, _, err := readStarts(y)
	return h, err
}

// readStarts reads the starts file, with its fields for writing it again:
// nil for a list.
func readStarts(y Yesman) (StartHistory, map[string]json.RawMessage, error) {
	var h StartHistory
	data, err := os.ReadFile(y.startsFile())
	if errors.Is(err, os.ErrNotExist) {
		return h, nil, nil
	}
	if err != nil {
		return h, nil, fmt.Errorf("worker starts: %w", err)
	}
	if data = bytes.TrimSpace(data); len(data) > 0 && data[0] == '[' {
		if err := json.Unmarshal(data, &h.Starts); err != nil {
			return StartHistory{}, nil, fmt.Errorf("worker starts: %w", err)
		}
		if len(h.Starts) >= maxStarts {
			h.Forgotten = h.Starts[0].StartedAt
			for _, x := range h.Starts {
				h.Forgotten = earlierTime(h.Forgotten, x.StartedAt)
			}
		}
		return h, nil, nil
	}
	fields := map[string]json.RawMessage{}
	err = json.Unmarshal(data, &fields)
	if err == nil && fields == nil {
		err = errors.New("not an object")
	}
	if raw, ok := fields["starts"]; ok && err == nil {
		err = json.Unmarshal(raw, &h.Starts)
	}
	if raw, ok := fields["forgotten"]; ok && err == nil {
		err = json.Unmarshal(raw, &h.Forgotten)
	}
	if err != nil {
		return StartHistory{}, nil, fmt.Errorf("worker starts: %w", err)
	}
	return h, fields, nil
}

func earlierTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// noteStarts adds the starts of workers not recorded yet, with s.mu held.
// It is best effort: a worker starts or is removed whether or not this
// succeeds, and an unreadable file is left as it is rather than replaced.
// Starts dropped to bound the file move its Forgotten time on, never back.
func (s *Store) noteStarts(y Yesman, ws []Worker) {
	h, fields, err := readStarts(y)
	if err != nil {
		log.Printf("yesman: %s: %v", y.Name, err)
		return
	}
	starts := h.Starts
	n := len(starts)
	for _, w := range ws {
		if w.StartedAt.IsZero() || slices.ContainsFunc(starts, func(x WorkerStart) bool { return x.Name == w.Name && x.StartedAt.Equal(w.StartedAt) }) {
			continue
		}
		starts = append(starts, WorkerStart{Name: w.Name, Number: w.Number, StartedAt: w.StartedAt.UTC()})
	}
	if len(starts) == n {
		return
	}
	slices.SortStableFunc(starts, func(a, b WorkerStart) int { return a.StartedAt.Compare(b.StartedAt) })
	if len(starts) > maxStarts {
		for _, x := range starts[:len(starts)-maxStarts] {
			if x.StartedAt.After(h.Forgotten) {
				h.Forgotten = x.StartedAt
			}
		}
		starts = starts[len(starts)-maxStarts:]
	}
	var v any = starts
	if fields != nil || !h.Forgotten.IsZero() {
		if fields == nil {
			fields = map[string]json.RawMessage{}
		}
		if fields["starts"], err = json.Marshal(starts); err == nil && !h.Forgotten.IsZero() {
			fields["forgotten"], err = json.Marshal(h.Forgotten.UTC())
		}
		v = fields
	}
	if err == nil {
		err = writeJSON(y.startsFile(), v)
	}
	if err != nil {
		log.Printf("yesman: %s: record worker starts: %v", y.Name, err)
	}
}
