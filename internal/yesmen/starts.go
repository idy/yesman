package yesmen

import (
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

// maxStarts bounds the starts kept; the oldest go first.
const maxStarts = 10000

// startsFile keeps every worker start, including removed workers'.
func (y Yesman) startsFile() string { return filepath.Join(y.MetaDir(), "worker-starts.json") }

// WorkerStarts returns the recorded starts of a yesman's workers, oldest
// first: those of workers started or removed since yesman kept them, and
// of the workers there were then.
func (s *Store) WorkerStarts(name string) ([]WorkerStart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	y, err := s.Get(name)
	if err != nil {
		return nil, err
	}
	return readStarts(y)
}

func readStarts(y Yesman) ([]WorkerStart, error) {
	var starts []WorkerStart
	if err := readJSON(y.startsFile(), &starts); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("worker starts: %w", err)
	}
	return starts, nil
}

// noteStarts adds the starts of workers not recorded yet, with s.mu held.
// It is best effort: a worker starts or is removed whether or not this
// succeeds, and an unreadable file is left as it is rather than replaced.
func (s *Store) noteStarts(y Yesman, ws []Worker) {
	starts, err := readStarts(y)
	if err != nil {
		log.Printf("yesman: %s: %v", y.Name, err)
		return
	}
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
		starts = starts[len(starts)-maxStarts:]
	}
	if err := writeJSON(y.startsFile(), starts); err != nil {
		log.Printf("yesman: %s: record worker starts: %v", y.Name, err)
	}
}
