// Package yesmen keeps the workspaces of hired yesmen.
//
// A yesman is a directory. Its agents work in it, its worktrees are checked
// out into it, and .yesman/ holds what its agents share:
//
//	<workspace>/
//	  .yesman/
//	    yesman.json        when it was hired, and the model its lead starts as
//	    workers.json       the workers its lead started
//	    worker-starts.json when each worker started, removed ones too
//	    board.json         the task board its agents keep
//	    topics.json        questions and discussions worth keeping (topics.go)
//	    knowledge/         durable guidance shared by every agent; INDEX.md lists it
//	    task-log.md        short history TOC, maintained by the lead
//	    task-log/          dated per-task/PR/release Markdown details
//	    needs-you.json     what the lead handed to the user to do (Needs you)
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
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/idy/yesman/internal/tasklog"
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
	Name        string    `json:"name"` // Stable workspace and routing identity.
	DisplayName string    `json:"displayName,omitempty"`
	Path        string    `json:"path"`
	HiredAt     time.Time `json:"hiredAt,omitzero"`
	// Model is what its lead starts its first session as, chosen when it
	// was hired: provider/model:effort, or provider/model at pi's default
	// effort. Empty for yesmen hired before there was a choice.
	Model string `json:"model,omitempty"`
}

// Label is the user-visible name, including for workspaces hired before renaming.
func (y Yesman) Label() string {
	if y.DisplayName != "" {
		return y.DisplayName
	}
	return y.Name
}

// MetaDir is the workspace's .yesman directory.
func (y Yesman) MetaDir() string { return filepath.Join(y.Path, ".yesman") }

// KnowledgeDir holds the notes all of the yesman's agents share.
func (y Yesman) KnowledgeDir() string { return filepath.Join(y.MetaDir(), "knowledge") }

// TaskLogPath holds the task and PR history, read by agents on demand.
func (y Yesman) TaskLogPath() string { return filepath.Join(y.MetaDir(), "task-log.md") }

// SessionDir holds one agent's pi sessions.
func (y Yesman) SessionDir(agent string) string {
	return filepath.Join(y.MetaDir(), "sessions", agent)
}

// Worker is an agent a yesman's lead started for one task.
type Worker struct {
	Name string `json:"name"`
	// Number counts the yesman's workers in the order they started, from
	// 1, so the user can tell them apart at a glance as a1, a2, …
	Number int `json:"number,omitempty"`
	// Title is the subject of its work in a few words, as the user sees it.
	Title     string    `json:"title,omitempty"`
	Task      string    `json:"task"`
	StartedAt time.Time `json:"startedAt"`
	// FinishedAt is when its last run's report reached its lead, after
	// which its process was closed; zero while it has work.
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	// PausedAt is when the user stopped it or its run, zero once it is
	// given work again. yesman never starts a paused worker by itself.
	PausedAt time.Time `json:"pausedAt,omitzero"`
	// FailedAt is when its process last ended unexpectedly.
	FailedAt time.Time `json:"failedAt,omitzero"`
}

// Subject names a worker's work for the user: its title, else the title of
// the board task it owns, else the first line of its task.
func (w Worker) Subject(b Board) string {
	if w.Title != "" {
		return w.Title
	}
	var owned *Task
	for i := range b.Tasks {
		t := &b.Tasks[i]
		if t.Owner != w.Name {
			continue
		}
		// An open task it owns wins over finished ones; among the same, the
		// one changed last.
		if owned == nil || t.Open() && !owned.Open() || t.Open() == owned.Open() && t.UpdatedAt.After(owned.UpdatedAt) {
			owned = t
		}
	}
	if owned != nil {
		return owned.Title
	}
	line, _, _ := strings.Cut(strings.TrimSpace(w.Task), "\n")
	return line
}

// Store keeps the workspaces under a directory.
type Store struct {
	root, archive string
	mu            sync.Mutex
	metaMu        sync.Mutex // serialize metadata reads with atomic replacement on Windows

	lmu    sync.Mutex
	locks  map[string]*sync.RWMutex  // per yesman: Use reads, Archive writes
	hlocks map[string]*sync.Mutex    // per yesman: its handoffs, in turn
	clocks map[string]*sync.Mutex    // per yesman: its continuation record, in turn
	revs   map[string]*atomic.Uint64 // per yesman: changes to its board, workers and handoffs
	// topicLocks serialize each workspace's topic file; guarded by lmu.
	topicLocks map[string]chan struct{}
}

// Revision counts the changes to a yesman's board, workers and handoffs
// made through this Store. Each change counts before it is written, so one
// who read the same revision before and after reading them read no change
// that a later write overtook.
func (s *Store) Revision(name string) uint64 { return s.rev(name).Load() }

func (s *Store) rev(name string) *atomic.Uint64 {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	if s.revs == nil {
		s.revs = map[string]*atomic.Uint64{}
	}
	r := s.revs[name]
	if r == nil {
		r = &atomic.Uint64{}
		s.revs[name] = r
	}
	return r
}

// changing counts a change to a yesman's tasks, workers or handoffs.
func (s *Store) changing(name string) { s.rev(name).Add(1) }

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
	y, release, err := s.BeginUse(name)
	if err != nil {
		return err
	}
	defer release()
	return fn(y)
}

// BeginUse protects a multi-phase workspace operation until release. The
// release function is idempotent so callers can end the lease before waiting
// for an RPC response while still deferring cleanup of early-return paths.
func (s *Store) BeginUse(name string) (Yesman, func(), error) {
	if err := ValidName(name); err != nil {
		return Yesman{}, nil, err
	}
	l := s.lockFor(name)
	l.RLock()
	y, err := s.Get(name)
	if err != nil {
		l.RUnlock()
		return Yesman{}, nil, err
	}
	var once sync.Once
	return y, func() { once.Do(l.RUnlock) }, nil
}

const indexTemplate = `# Knowledge

Durable guidance shared by every agent of this yesman: reusable development
conventions, user preferences, architecture decisions and known pitfalls.
One note per file; list each here as ` + "`- [Title](file.md) — when it matters`" + `.
Update an existing note rather than duplicating it. Finishing a task alone
is not a reason to add a note.

Good notes: "Use the project's Phosphor icon helper and local font assets";
"Keep stable internal IDs when display names change."
Task and PR history, commit/head hashes, CI and validation results, release
timelines and temporary blockers belong in the working log or board, not here.
The working log's short TOC is separate from this index at ` + "`.yesman/task-log.md`" + `,
linking to per-task/PR/release details in ` + "`.yesman/task-log/`" + `.
Read only relevant detail files on demand; the lead alone maintains history.
`

const taskLogTemplate = tasklog.Template

// ensureTaskLog creates only a missing log. Exclusive creation also leaves
// user-owned files, directories and symbolic links at this path untouched.
func ensureTaskLog(y Yesman) error {
	return tasklog.Bootstrap(y.Path)
}

// EnsureTaskLogs adds the working log to existing workspaces on app startup.
// It never rewrites Knowledge or other metadata, and repeated calls are safe.
func (s *Store) EnsureTaskLogs() error {
	ys, err := s.List()
	var errs []error
	if err != nil {
		var metadataErrors MetadataErrors
		if !errors.As(err, &metadataErrors) {
			return err
		}
		errs = append(errs, err)
	}
	for _, y := range ys {
		if err := s.Use(y.Name, ensureTaskLog); err != nil {
			errs = append(errs, fmt.Errorf("%s working log: %w", y.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Get returns a hired yesman.
func (s *Store) Get(name string) (Yesman, error) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	return s.get(name)
}

func (s *Store) get(name string) (Yesman, error) {
	if err := ValidName(name); err != nil {
		return Yesman{}, err
	}
	y := Yesman{Name: name, Path: filepath.Join(s.root, name)}
	if _, err := os.Stat(y.MetaDir()); err != nil {
		return Yesman{}, fmt.Errorf("no yesman named %s", name)
	}
	var meta struct {
		HiredAt     time.Time `json:"hiredAt"`
		DisplayName string    `json:"displayName"`
		Model       string    `json:"model"`
	}
	if err := readYesmanMetadata(filepath.Join(y.MetaDir(), "yesman.json"), &meta); err != nil {
		var issue *MetadataError
		if errors.As(err, &issue) {
			issue.Name = name
			return Yesman{}, issue
		}
	}
	y.HiredAt, y.Model = meta.HiredAt, meta.Model
	y.DisplayName = meta.DisplayName
	return y, nil
}

// normalizeDisplayName is shared by creation and rename. Labels have no slug
// length limit; only stable internal names are limited to 32 ASCII characters.
func normalizeDisplayName(label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" || !utf8.ValidString(label) || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return "", errors.New("enter a name without control characters")
	}
	return label, nil
}

// Rename changes only the visible label. Workspace paths, agent IDs and all
// agent state keep their original identity. Duplicate labels are allowed.
func (s *Store) Rename(name, label string) (Yesman, error) {
	label, err := normalizeDisplayName(label)
	if err != nil {
		return Yesman{}, err
	}
	var renamed Yesman
	err = s.Use(name, func(Yesman) error {
		s.metaMu.Lock()
		defer s.metaMu.Unlock()
		y, err := s.get(name)
		if err != nil {
			return err
		}
		path := filepath.Join(y.MetaDir(), "yesman.json")
		meta := map[string]json.RawMessage{}
		if err := readYesmanMetadata(path, &meta); err != nil {
			return err
		}
		if meta == nil {
			return errors.New("invalid yesman metadata")
		}
		meta["displayName"], _ = json.Marshal(label)
		if err := writeYesmanMetadata(path, meta); err != nil {
			return err
		}
		y.DisplayName = label
		renamed = y
		return nil
	})
	return renamed, err
}

// List returns the healthy hired yesmen, oldest first. Oversized metadata is
// reported separately as MetadataErrors alongside these results; an affected
// workspace is never returned with a fabricated zero-metadata identity.
func (s *Store) List() ([]Yesman, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Yesman
	var issues MetadataErrors
	for _, e := range entries {
		if !e.IsDir() || ValidName(e.Name()) != nil {
			continue
		}
		if y, err := s.Get(e.Name()); err == nil {
			out = append(out, y)
		} else {
			var issue *MetadataError
			if errors.As(err, &issue) {
				issues = append(issues, *issue)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].HiredAt.Equal(out[j].HiredAt) {
			return out[i].HiredAt.Before(out[j].HiredAt)
		}
		return out[i].Name < out[j].Name
	})
	if len(issues) > 0 {
		return out, issues
	}
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
	// Windows readers do not share delete access, so a reader must close the
	// old file before an update can atomically replace it.
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workers(name)
}

// workers reads worker metadata with s.mu held by the caller.
func (s *Store) workers(name string) ([]Worker, error) {
	y, err := s.Get(name)
	if err != nil {
		return nil, err
	}
	var ws []Worker
	if err := readJSON(filepath.Join(y.MetaDir(), "workers.json"), &ws); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	number(ws)
	return ws, nil
}

// number numbers workers recorded before workers had numbers, after the
// others, in the order they started.
func number(ws []Worker) {
	next := 0
	var missing []int
	for i, w := range ws {
		next = max(next, w.Number)
		if w.Number == 0 {
			missing = append(missing, i)
		}
	}
	sort.SliceStable(missing, func(a, b int) bool { return ws[missing[a]].StartedAt.Before(ws[missing[b]].StartedAt) })
	for _, i := range missing {
		next++
		ws[i].Number = next
	}
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
	ws, err := s.workers(name)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(ws, func(x Worker) bool { return x.Name == w.Name }) {
		return fmt.Errorf("%s already has a worker named %s", name, w.Name)
	}
	w.Number = 1
	for _, x := range ws {
		w.Number = max(w.Number, x.Number+1)
	}
	y, _ := s.Get(name)
	ws = append(ws, w)
	s.changing(name)
	if err := writeJSON(filepath.Join(y.MetaDir(), "workers.json"), ws); err != nil {
		return err
	}
	s.noteStarts(y, ws)
	return nil
}

// FinishWorker records that a worker finished: its report reached its lead.
func (s *Store) FinishWorker(name, worker string, at time.Time) error {
	_, err := s.updateWorker(name, worker, func(w *Worker) bool {
		w.FinishedAt = at
		return true
	})
	return err
}

// FinishWorkerOf is FinishWorker for the worker started at since only: a
// worker of the same name started later is left alone.
func (s *Store) FinishWorkerOf(name, worker string, since, at time.Time) error {
	_, err := s.updateWorker(name, worker, func(w *Worker) bool {
		if !w.StartedAt.Equal(since) {
			return false
		}
		w.FinishedAt = at
		return true
	})
	return err
}

// ReopenWorker records that a worker was given work again: it is neither
// finished, paused nor failed any more. It reports whether the worker was finished.
func (s *Store) ReopenWorker(name, worker string) (bool, error) {
	var reopened bool
	_, err := s.updateWorker(name, worker, func(w *Worker) bool {
		reopened = !w.FinishedAt.IsZero()
		if !reopened && w.PausedAt.IsZero() && w.FailedAt.IsZero() {
			return false
		}
		w.FinishedAt, w.PausedAt, w.FailedAt = time.Time{}, time.Time{}, time.Time{}
		return true
	})
	return reopened, err
}

// ReopenWorkerIf is ReopenWorker for the worker started at since only: a
// worker of the same name started later is left wholly alone. Its pause is
// cleared only if unpause, called under the lock PauseWorkerIf writes under,
// reports so for the record: a Stop made since the worker was given work
// stays.
func (s *Store) ReopenWorkerIf(name, worker string, since time.Time, unpause func(Worker) bool) (bool, error) {
	var reopened bool
	_, err := s.updateWorker(name, worker, func(w *Worker) bool {
		if !w.StartedAt.Equal(since) {
			return false // another worker of that name
		}
		reopened = !w.FinishedAt.IsZero()
		clear := !w.PausedAt.IsZero() && unpause(*w)
		if !reopened && w.FailedAt.IsZero() && !clear {
			return false
		}
		w.FinishedAt, w.FailedAt = time.Time{}, time.Time{}
		if clear {
			w.PausedAt = time.Time{}
		}
		return true
	})
	return reopened, err
}

// ResumeWorkerIf clears the user's pause of the worker started at since, if
// current reports true when the record is written, under the lock
// PauseWorkerIf writes under. It changes nothing else.
func (s *Store) ResumeWorkerIf(name, worker string, since time.Time, current func() bool) error {
	_, err := s.updateWorker(name, worker, func(w *Worker) bool {
		if w.PausedAt.IsZero() || !w.StartedAt.Equal(since) || !current() {
			return false
		}
		w.PausedAt = time.Time{}
		return true
	})
	return err
}

// PauseWorker records that the user stopped the worker started at since,
// and FailWorker that its process ended unexpectedly. A worker of another
// generation, hired again under the same name, is left alone.
func (s *Store) PauseWorker(name, worker string, since, at time.Time) error {
	_, err := s.updateWorker(name, worker, func(w *Worker) bool {
		if !w.StartedAt.Equal(since) {
			return false
		}
		w.PausedAt = at
		return true
	})
	return err
}

// PauseWorkerIf is PauseWorker, made only if current reports true when
// the record is written, under the lock ReopenWorker writes under: a pause
// decided before the worker was given work again is not written after.
func (s *Store) PauseWorkerIf(name, worker string, since, at time.Time, current func() bool) error {
	_, err := s.updateWorker(name, worker, func(w *Worker) bool {
		if !w.StartedAt.Equal(since) || !current() {
			return false
		}
		w.PausedAt = at
		return true
	})
	return err
}

// FailWorker: see PauseWorker.
func (s *Store) FailWorker(name, worker string, since, at time.Time) error {
	_, err := s.updateWorker(name, worker, func(w *Worker) bool {
		if !w.StartedAt.Equal(since) {
			return false
		}
		w.FailedAt = at
		return true
	})
	return err
}

// updateWorker changes a worker's record, writing it only when fn reports a
// change. A worker no longer listed is left alone.
func (s *Store) updateWorker(name, worker string, fn func(*Worker) bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, err := s.workers(name)
	if err != nil {
		return false, err
	}
	i := slices.IndexFunc(ws, func(x Worker) bool { return x.Name == worker })
	if i < 0 || !fn(&ws[i]) {
		return false, nil
	}
	y, _ := s.Get(name)
	s.changing(name)
	return true, writeJSON(filepath.Join(y.MetaDir(), "workers.json"), ws)
}

// RemoveWorker forgets a worker. Its session log stays, and so does when
// it started (see WorkerStarts).
func (s *Store) RemoveWorker(name, worker string) error {
	return s.RemoveWorkerIf(name, worker, time.Time{})
}

// RemoveWorkerIf rolls back a failed start only while its exact record is
// still present. A zero since is an ordinary explicit removal by name.
func (s *Store) RemoveWorkerIf(name, worker string, since time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, err := s.workers(name)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(ws, func(x Worker) bool { return x.Name == worker })
	if !since.IsZero() && (i < 0 || !ws[i].StartedAt.Equal(since)) {
		return fmt.Errorf("the worker was removed or replaced")
	}
	y, _ := s.Get(name)
	s.noteStarts(y, ws)
	// Its tasks stay its own: a worker of the same name hired later does
	// not own them, whatever it works on. Bound first, under the same lock,
	// so that no reader sees the name free with its tasks unbound.
	if i >= 0 {
		b, err := s.board(name)
		if err != nil {
			return err
		}
		bound := false
		for j := range b.Tasks {
			if t := &b.Tasks[j]; t.Owner == worker && t.OwnerSince.IsZero() {
				t.OwnerSince, bound = ws[i].StartedAt, true
			}
		}
		if bound {
			s.changing(name)
			if err := writeJSON(y.BoardFile(), b); err != nil {
				return err
			}
		}
	}
	ws = slices.DeleteFunc(ws, func(x Worker) bool { return x.Name == worker })
	s.changing(name)
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
