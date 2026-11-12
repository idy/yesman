// Package yesmen keeps the workspaces of hired yesmen.
//
// A yesman is a directory. Its agents work in it, its worktrees are checked
// out into it, and .yesman/ holds what its agents share:
//
//	<workspace>/
//	  .yesman/
//	    yesman.json        when it was hired
//	    workers.json       the workers its lead started
//	    knowledge/         notes every agent reads and writes; INDEX.md lists them
//	    sessions/<agent>/  each agent's pi session log
//	  <repo>--<topic>/     worktrees
package yesmen

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"sync"
	"time"
)

// LeadName is the agent name of every yesman's lead.
const LeadName = "lead"

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidName reports whether a name can name a yesman or an agent: 1 to 32
// lowercase letters, digits and dashes, starting with a letter or digit.
func ValidName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("%q is not a valid name: use 1-32 lowercase letters, digits and dashes", name)
	}
	return nil
}

// Yesman is a hired yesman.
type Yesman struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	HiredAt time.Time `json:"hiredAt,omitzero"`
}

// MetaDir is the workspace's .yesman directory.
func (y Yesman) MetaDir() string { return filepath.Join(y.Path, ".yesman") }

// KnowledgeDir holds the notes all of the yesman's agents share.
func (y Yesman) KnowledgeDir() string { return filepath.Join(y.MetaDir(), "knowledge") }

// SessionDir holds one agent's pi sessions.
func (y Yesman) SessionDir(agent string) string {
	return filepath.Join(y.MetaDir(), "sessions", agent)
}

// Worker is an agent a yesman's lead started for one task.
type Worker struct {
	Name      string    `json:"name"`
	Task      string    `json:"task"`
	StartedAt time.Time `json:"startedAt"`
}

// Store keeps the workspaces under a directory.
type Store struct {
	root, archive string
	mu            sync.Mutex

	lmu   sync.Mutex
	locks map[string]*sync.RWMutex // per yesman: Use reads, Archive writes
}

// New returns the store of workspaces under root, archiving into archive.
func New(root, archive string) *Store {
	return &Store{root: root, archive: archive, locks: map[string]*sync.RWMutex{}}
}

func (s *Store) lockFor(name string) *sync.RWMutex {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	l := s.locks[name]
	if l == nil {
		l = &sync.RWMutex{}
		s.locks[name] = l
	}
	return l
}

// Use runs fn with a yesman's workspace, which is not archived until fn
// returns. It fails when the yesman does not exist, or no longer does.
// Work that writes into a workspace, such as a checkout or starting an
// agent, runs inside Use so that it cannot outlive a dismissal.
func (s *Store) Use(name string, fn func(Yesman) error) error {
	if err := ValidName(name); err != nil {
		return err
	}
	l := s.lockFor(name)
	l.RLock()
	defer l.RUnlock()
	y, err := s.Get(name)
	if err != nil {
		return err
	}
	return fn(y)
}

const indexTemplate = `# Knowledge

Notes shared by every agent of this yesman. One note per file in this
directory; list each here as ` + "`- [Title](file.md) — when it matters`" + `.
`

// Hire creates a yesman's workspace.
func (s *Store) Hire(name string) (Yesman, error) {
	if err := ValidName(name); err != nil {
		return Yesman{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	y := Yesman{Name: name, Path: filepath.Join(s.root, name), HiredAt: time.Now().UTC()}
	if _, err := os.Stat(y.Path); err == nil {
		return Yesman{}, fmt.Errorf("a yesman named %s already exists", name)
	}
	for _, dir := range []string{y.KnowledgeDir(), filepath.Join(y.MetaDir(), "sessions")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Yesman{}, err
		}
	}
	if err := os.WriteFile(filepath.Join(y.KnowledgeDir(), "INDEX.md"), []byte(indexTemplate), 0o644); err != nil {
		return Yesman{}, err
	}
	if err := writeJSON(filepath.Join(y.MetaDir(), "yesman.json"), map[string]any{"hiredAt": y.HiredAt}); err != nil {
		return Yesman{}, err
	}
	return y, nil
}

// Get returns a hired yesman.
func (s *Store) Get(name string) (Yesman, error) {
	if err := ValidName(name); err != nil {
		return Yesman{}, err
	}
	y := Yesman{Name: name, Path: filepath.Join(s.root, name)}
	if _, err := os.Stat(y.MetaDir()); err != nil {
		return Yesman{}, fmt.Errorf("no yesman named %s", name)
	}
	var meta struct {
		HiredAt time.Time `json:"hiredAt"`
	}
	_ = readJSON(filepath.Join(y.MetaDir(), "yesman.json"), &meta)
	y.HiredAt = meta.HiredAt
	return y, nil
}

// List returns the hired yesmen, oldest first.
func (s *Store) List() ([]Yesman, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Yesman
	for _, e := range entries {
		if !e.IsDir() || ValidName(e.Name()) != nil {
			continue
		}
		if y, err := s.Get(e.Name()); err == nil {
			out = append(out, y)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].HiredAt.Equal(out[j].HiredAt) {
			return out[i].HiredAt.Before(out[j].HiredAt)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Archive moves a yesman's workspace into the archive and returns where it
// went. It waits for work inside Use to finish, and runs before, when not
// nil, while none can start, just before the move.
func (s *Store) Archive(name string, before func(Yesman) error) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	l := s.lockFor(name)
	l.Lock()
	defer l.Unlock()
	y, err := s.Get(name)
	if err != nil {
		return "", err
	}
	if before != nil {
		if err := before(y); err != nil {
			return "", err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.archive, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(s.archive, name+"-"+time.Now().UTC().Format("20060102-150405"))
	if err := os.Rename(y.Path, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// Workers returns the workers a yesman's lead started.
func (s *Store) Workers(name string) ([]Worker, error) {
	y, err := s.Get(name)
	if err != nil {
		return nil, err
	}
	var ws []Worker
	if err := readJSON(filepath.Join(y.MetaDir(), "workers.json"), &ws); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return ws, nil
}

// AddWorker records a new worker.
func (s *Store) AddWorker(name string, w Worker) error {
	if err := ValidName(w.Name); err != nil {
		return err
	}
	if w.Name == LeadName {
		return fmt.Errorf("%q is the lead's name", LeadName)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, err := s.Workers(name)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(ws, func(x Worker) bool { return x.Name == w.Name }) {
		return fmt.Errorf("%s already has a worker named %s", name, w.Name)
	}
	y, _ := s.Get(name)
	return writeJSON(filepath.Join(y.MetaDir(), "workers.json"), append(ws, w))
}

// RemoveWorker forgets a worker. Its session log stays.
func (s *Store) RemoveWorker(name, worker string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, err := s.Workers(name)
	if err != nil {
		return err
	}
	y, _ := s.Get(name)
	ws = slices.DeleteFunc(ws, func(x Worker) bool { return x.Name == worker })
	return writeJSON(filepath.Join(y.MetaDir(), "workers.json"), ws)
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
