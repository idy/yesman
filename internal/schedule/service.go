package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// Submitter is the agent manager's receipt path, which the scheduler sends
// runs through: *agent.Manager.
type Submitter interface {
	SubmitFollowUp(ctx context.Context, id, messageID, text string, origin agent.Origin, expectedSince time.Time) (agent.Delivery, error)
	LookupDelivery(ctx context.Context, id, messageID string, expectedSince time.Time) (agent.Delivery, agent.DeliveryLookup, error)
}

// Config configures a Service.
type Config struct {
	Yesmen *yesmen.Store
	Agents Submitter
	// LockPath is the file whose OS lock makes one app process the owner of
	// the scheduled tasks of a data directory. Empty: this one always is.
	LockPath string
	// Now is the scheduling clock. IDNow stamps submission IDs, which the
	// receipt store checks against the real clock. Both default to time.Now.
	Now, IDNow func() time.Time
	// OnChange is told when a workspace's plans or runs change.
	OnChange func(yesman string)
	// BoardChanged is told when a run added a board task.
	BoardChanged func(yesman string)
	// Interval between checks while nothing is due sooner; 30 s by default.
	Interval time.Duration
	// Concurrency of deliveries across workspaces; 4 by default. Each
	// workspace sends one run at a time, in the order they were claimed.
	Concurrency int

	// Fault injection for tests: an error stops a run's pipeline at that
	// point, as a crash would, leaving its records as they are.
	afterClaim, afterBoard, afterAttempt func(Run) error
	writeRun                             func(path string, data []byte) error
}

// Errors a caller may show as they are.
var (
	ErrNotOwner = errors.New("another yesman app is running these scheduled tasks, so they are read-only here")
	ErrConflict = errors.New("this task was changed since you opened it: reload to see the latest version, then edit again")
	ErrClosed   = errors.New("yesman is quitting")
	errRetired  = errors.New("this yesman was dismissed")
)

// Service runs the scheduled tasks of every workspace while the app runs.
type Service struct {
	cfg Config
	id  string // this scheduler instance, the claimer of its runs

	mu      sync.Mutex
	spaces  map[string]*space
	closed  bool
	owner   bool
	lock    io.Closer
	lockErr string

	started bool
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	events  chan agent.Event
	sem     chan struct{}
	work    sync.WaitGroup
}

// space is the scheduler's state of one workspace generation.
type space struct {
	name  string
	since time.Time // the workspace's HiredAt: a same-name hire is another space

	mu     sync.Mutex
	loaded bool
	err    error // the plans file cannot be read; nothing is written over it
	stamp  fileStamp
	plans  plansFile
	runs   map[string]*record
	active map[string]bool // runs whose pipeline is queued or running
	// fresh holds runs this scheduler claimed (or was asked to send again)
	// and queued at once: waiting their turn does not make them late. A run
	// picked up again later, after a crash, a quit or a step that failed,
	// is not fresh.
	fresh   map[string]bool
	problem map[string]string // plan ID -> why it could not run last time
	queue   []string
	working bool

	retired atomic.Bool
}

type fileStamp struct {
	size int64
	mod  time.Time
	ok   bool
}

func stampOf(path string) fileStamp {
	info, err := os.Lstat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{size: info.Size(), mod: info.ModTime(), ok: true}
}

// New returns a Service. Start begins scheduling.
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.IDNow == nil {
		cfg.IDNow = time.Now
	}
	if cfg.OnChange == nil {
		cfg.OnChange = func(string) {}
	}
	if cfg.BoardChanged == nil {
		cfg.BoardChanged = func(string) {}
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	return &Service{
		cfg: cfg, spaces: map[string]*space{}, id: randomHex(8),
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
		events: make(chan agent.Event, 256), sem: make(chan struct{}, cfg.Concurrency),
	}
}

func (s *Service) now() time.Time { return s.cfg.Now().UTC().Round(0) }

// Start begins checking for due runs in the background.
func (s *Service) Start() {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	go s.loop()
}

// Close stops scheduling: no run is claimed after it returns. Runs on their
// way stay recorded as they are and are resumed or resolved on the next
// start. It waits a little for deliveries under way, then releases the lock.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.stop)
	started := s.started
	s.mu.Unlock()
	if started {
		<-s.done
	}
	finished := make(chan struct{})
	go func() { s.work.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		// A delivery still waits on pi: keep the lock, so no other app takes
		// its run over while it may still go out. Exiting releases it.
		return
	}
	s.mu.Lock()
	if s.lock != nil {
		_ = s.lock.Close()
		s.lock, s.owner = nil, false
	}
	s.mu.Unlock()
}

// Wait waits for the deliveries under way, for tests and tools.
func (s *Service) Wait() { s.work.Wait() }

func (s *Service) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Wake asks for a check soon, after plans changed.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) loop() {
	defer close(s.done)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-s.stop:
			return
		case e := <-s.events:
			s.handleEvent(e)
			continue
		case <-s.wake:
		case <-timer.C:
		}
		next := s.Tick(context.Background())
		wait := s.cfg.Interval
		if !next.IsZero() {
			if until := time.Until(next); until < wait {
				wait = max(until, time.Second)
			}
		}
		timer.Reset(wait)
	}
}

// Owner reports whether this process runs the scheduled tasks, taking the
// lock if it is free.
func (s *Service) Owner() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownerLocked()
}

func (s *Service) ownerLocked() bool {
	if s.closed {
		return false
	}
	if s.owner {
		return true
	}
	if s.cfg.LockPath == "" {
		s.owner = true
		return true
	}
	f, err := lockFile(s.cfg.LockPath)
	if err != nil {
		s.lockErr = err.Error()
		return false
	}
	s.lock, s.owner, s.lockErr = f, true, ""
	// Another process may have run them until now: read everything again.
	for _, sp := range s.spaces {
		sp.retired.Store(true)
	}
	s.spaces = map[string]*space{}
	return true
}

// spaceFor returns the state of a workspace's current generation.
func (s *Service) spaceFor(y yesmen.Yesman) *space {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp := s.spaces[y.Name]
	if sp != nil && !sp.since.Equal(y.HiredAt) {
		sp.retired.Store(true)
		sp = nil
	}
	if sp == nil {
		sp = &space{name: y.Name, since: y.HiredAt, active: map[string]bool{}, fresh: map[string]bool{}, problem: map[string]string{}}
		s.spaces[y.Name] = sp
	}
	return sp
}

// RetireYesman forgets a workspace generation being dismissed: no run is
// claimed for it any more, and its pending writes find it gone. Archive
// calls it with the workspace held, before moving it.
func (s *Service) RetireYesman(name string, since time.Time) {
	s.mu.Lock()
	sp := s.spaces[name]
	if sp != nil && (since.IsZero() || sp.since.Equal(since)) {
		delete(s.spaces, name)
		sp.retired.Store(true)
	}
	s.mu.Unlock()
}

// loadLocked reads a workspace's plans, again when the file changed, and
// its runs once.
func (s *Service) loadLocked(sp *space, y yesmen.Yesman) {
	path := plansPath(y)
	st := stampOf(path)
	if sp.loaded && st == sp.stamp && sp.err == nil {
		return
	}
	data, err := readLimited(path, maxPlansFile)
	var f plansFile
	if err == nil && data != nil {
		f, err = decodePlans(data)
	} else if err == nil {
		f = plansFile{Version: plansVersion, planExtras: map[string]map[string]json.RawMessage{}}
	}
	if err != nil {
		sp.err = fmt.Errorf("the tasks file cannot be read, so yesman left it unchanged and runs none of its tasks: %w", err)
		if !sp.loaded {
			sp.plans = plansFile{Version: plansVersion}
		}
	} else {
		sp.err, sp.plans = nil, f
	}
	if !sp.loaded || sp.runs == nil {
		runs, rerr := readRuns(runsDir(y))
		if rerr != nil {
			sp.err = errors.Join(sp.err, fmt.Errorf("the run history cannot be read: %w", rerr))
			runs = map[string]*record{}
		}
		sp.runs = runs
	}
	sp.loaded, sp.stamp = true, st
}

func (s *Service) writePlansLocked(sp *space, y yesmen.Yesman, f plansFile) error {
	if err := safeDir(schedulesDir(y)); err != nil {
		return err
	}
	data, err := f.encode()
	if err != nil {
		return err
	}
	if err := writeFile(plansPath(y), data); err != nil {
		return err
	}
	sp.plans, sp.stamp = f, stampOf(plansPath(y))
	return nil
}

func (s *Service) writeRunLocked(y yesmen.Yesman, r *record) error {
	data, err := encodeRun(*r)
	if err != nil {
		return err
	}
	if s.cfg.writeRun != nil {
		return s.cfg.writeRun(runPath(y, r.ID), data)
	}
	return writeFile(runPath(y, r.ID), data)
}

// claimLocked creates a run's record, which must not exist yet.
func (s *Service) claimLocked(sp *space, y yesmen.Yesman, r Run) (*record, error) {
	if err := safeDir(schedulesDir(y)); err != nil {
		return nil, err
	}
	if err := safeDir(runsDir(y)); err != nil {
		return nil, err
	}
	rec := &record{Run: r}
	data, err := encodeRun(*rec)
	if err != nil {
		return nil, err
	}
	if err := createFile(runPath(y, r.ID), data); err != nil {
		if errors.Is(err, errClaimed) {
			// Made by another tick or process: take theirs.
			if runs, rerr := readRuns(runsDir(y)); rerr == nil && runs[r.ID] != nil {
				sp.runs[r.ID] = runs[r.ID]
			}
		}
		return nil, err
	}
	sp.runs[r.ID] = rec
	return rec, nil
}

// lease holds a workspace generation for a write; it fails once it was
// dismissed or replaced.
func (s *Service) lease(sp *space) (yesmen.Yesman, func(), error) {
	y, release, err := s.cfg.Yesmen.BeginUse(sp.name)
	if err != nil {
		return yesmen.Yesman{}, nil, errRetired
	}
	if !y.HiredAt.Equal(sp.since) || sp.retired.Load() {
		release()
		return yesmen.Yesman{}, nil, errRetired
	}
	return y, release, nil
}

// update changes a run's record on disk and then in memory.
func (s *Service) update(sp *space, id string, fn func(*Run) bool) (Run, error) {
	y, release, err := s.lease(sp)
	if err != nil {
		return Run{}, err
	}
	defer release()
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return s.updateLocked(sp, y, id, fn)
}

func (s *Service) updateLocked(sp *space, y yesmen.Yesman, id string, fn func(*Run) bool) (Run, error) {
	rec := sp.runs[id]
	if rec == nil {
		return Run{}, fmt.Errorf("no run %s", id)
	}
	next := *rec
	next.Run.Attempts = slices.Clone(rec.Attempts)
	if rec.Outcome != nil {
		o := *rec.Outcome
		next.Outcome = &o
	}
	if !fn(&next.Run) {
		return rec.Run, nil
	}
	next.UpdatedAt = s.now()
	if err := s.writeRunLocked(y, &next); err != nil {
		return rec.Run, err
	}
	before := rec.Run
	*rec = next
	s.markBoardLocked(y, before, rec.Run)
	return rec.Run, nil
}

// notDelivered starts the note of a run's board task while the run was
// definitely not delivered, so that the lead, and its reminders of open
// tasks, do not take up a request it never got.
const notDelivered = "Scheduled run not delivered to the lead:"

// markBoardLocked keeps a run's board task in step with what is known of
// its delivery: blocked with a note while it was definitely not delivered,
// and back to do when it is sent again. A task the lead or the user changed
// in between is left as they left it.
func (s *Service) markBoardLocked(y yesmen.Yesman, before, after Run) {
	if after.Board == "" || !after.fired() {
		return
	}
	var fn func(*yesmen.Task) bool
	switch {
	case after.State == StateFailed && before.State != StateFailed:
		note := fmt.Sprintf("%s %s. Send it again from Tasks.", notDelivered, strings.TrimSuffix(strings.TrimPrefix(after.Reason, "Not sent: "), "."))
		fn = func(t *yesmen.Task) bool {
			if t.Status != yesmen.TaskTodo {
				return false
			}
			t.Status, t.Note = yesmen.TaskBlocked, note
			return true
		}
	case before.State == StateFailed && after.State != StateFailed:
		fn = func(t *yesmen.Task) bool {
			if t.Status != yesmen.TaskBlocked || !strings.HasPrefix(t.Note, notDelivered) {
				return false
			}
			t.Status, t.Note = yesmen.TaskTodo, ""
			return true
		}
	default:
		return
	}
	if _, changed, err := s.cfg.Yesmen.UpdateScheduledTask(y.Name, after.ID, fn); err != nil {
		logf("board task of %s: %v", after.ID, err)
	} else if changed {
		s.cfg.BoardChanged(y.Name)
	}
}

func (s *Service) changed(name string) { s.cfg.OnChange(name) }

// ---------- Ticks ----------

// Tick checks every workspace once: claims what is due, records what was
// missed or skipped, resumes runs left on their way and follows their board
// tasks. It returns the next time something is due, zero for none.
func (s *Service) Tick(ctx context.Context) time.Time {
	if !s.Owner() {
		return time.Time{}
	}
	ys, err := s.cfg.Yesmen.List()
	if err != nil {
		return time.Time{}
	}
	var next time.Time
	for _, y := range ys {
		if ctx.Err() != nil || s.isClosed() {
			break
		}
		if n := s.tickSpace(y); !n.IsZero() && (next.IsZero() || n.Before(next)) {
			next = n
		}
	}
	return next
}

func (s *Service) tickSpace(listed yesmen.Yesman) time.Time {
	y, release, err := s.cfg.Yesmen.BeginUse(listed.Name)
	if err != nil {
		return time.Time{}
	}
	defer release()
	sp := s.spaceFor(y)
	if sp.retired.Load() {
		return time.Time{}
	}
	board, boardErr := s.cfg.Yesmen.Board(y.Name)
	tasks := boardTasks(board, boardErr)
	now := s.now()

	sp.mu.Lock()
	s.loadLocked(sp, y)
	changed := s.followBoardLocked(sp, y, tasks, boardErr, now)
	var launch []string
	// Runs left on their way by a crash or a quit resume or resolve.
	for _, id := range sortedIDs(sp.runs) {
		if r := sp.runs[id]; r.inFlight() && !sp.active[id] && s.mayDrive(r.Run) {
			sp.active[id] = true
			launch = append(launch, id)
		}
	}
	var next time.Time
	if sp.err == nil {
		for _, p := range slices.Clone(sp.plans.Plans) {
			ids, n, ch := s.dueLocked(sp, y, p, tasks, boardErr, now)
			launch = append(launch, ids...)
			changed = changed || ch
			if !n.IsZero() && (next.IsZero() || n.Before(next)) {
				next = n
			}
		}
		if s.pruneLocked(sp, y, tasks, boardErr) {
			changed = true
		}
	}
	sp.mu.Unlock()
	release()
	for _, id := range launch {
		s.enqueue(sp, id)
	}
	if changed || len(launch) > 0 {
		s.changed(y.Name)
	}
	return next
}

func sortedIDs(runs map[string]*record) []string {
	ids := make([]string, 0, len(runs))
	for id := range runs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := runs[ids[i]], runs[ids[j]]
		if !a.ClaimedAt.Equal(b.ClaimedAt) {
			return a.ClaimedAt.Before(b.ClaimedAt)
		}
		return ids[i] < ids[j]
	})
	return ids
}

// boardTasks indexes a board's tasks by ID; nil when it could not be read.
func boardTasks(b yesmen.Board, err error) map[string]yesmen.Task {
	if err != nil {
		return nil
	}
	tasks := make(map[string]yesmen.Task, len(b.Tasks))
	for _, t := range b.Tasks {
		tasks[t.ID] = t
	}
	return tasks
}

// taskOf returns a run's board task, if the board still has it.
func taskOf(r Run, tasks map[string]yesmen.Task) (yesmen.Task, bool) {
	t, ok := tasks[r.Board]
	if !ok || t.Schedule == nil || t.Schedule.Run != r.ID {
		return yesmen.Task{}, false
	}
	return t, true
}

// open reports whether a run still has work: on its way, or its board task
// not done or dropped. A board that cannot be read counts as open.
func open(r Run, tasks map[string]yesmen.Task, boardErr error) bool {
	if !r.fired() {
		return false
	}
	switch r.State {
	case StateClaimed, StateBoarded, StateSending:
		return true
	case StateSent, StateQueued, StateUnknown:
		if boardErr != nil {
			return true
		}
		t, ok := taskOf(r, tasks)
		return ok && t.Open()
	}
	return false
}

// cursor is where a plan's schedule goes on from: the newest occurrence it
// recorded, else when its schedule took effect. Records the clock has not
// reached again, after it was set back, keep it there too, so no occurrence
// runs twice.
func cursor(p Plan, records []*record) time.Time {
	c := p.ActiveSince
	if r := cursorRecord(records); r != nil && r.latest().After(c) {
		c = r.latest()
	}
	return c
}

// cursorRecord is the record of a plan's newest recorded occurrence: its
// newest scheduled, missed or skipped record. Manual runs and catch-ups are
// not occurrences of its schedule.
func cursorRecord(records []*record) *record {
	var newest *record
	for _, r := range records {
		if r.State == StateDamaged || r.Kind != RunScheduled && r.Kind != RunMissed && r.Kind != RunSkipped {
			continue
		}
		if newest == nil || r.latest().After(newest.latest()) {
			newest = r
		}
	}
	return newest
}

// dueLocked records what is due of a plan: the newest occurrence, if seen
// on time, is claimed (or skipped while the last run is open); every older
// one since the cursor joins one missed record. It returns runs to send and
// the plan's next due time.
func (s *Service) dueLocked(sp *space, y yesmen.Yesman, p Plan, tasks map[string]yesmen.Task, boardErr error, now time.Time) ([]string, time.Time, bool) {
	if !p.Enabled || p.Archived() {
		return nil, time.Time{}, false
	}
	loc, err := LoadZone(p.Timezone)
	if err != nil {
		sp.problem[p.ID] = err.Error()
		return nil, time.Time{}, false
	}
	spec, err := p.Schedule.normalize()
	if err != nil {
		sp.problem[p.ID] = err.Error()
		return nil, time.Time{}, false
	}
	records := plansRuns(sp.runs, p.ID)
	from := cursor(p, records)
	occs, total, capped := spec.between(from, now, loc, p.ActiveSince, 64)
	var launch []string
	changed := false
	if total > 0 {
		newest := occs[len(occs)-1]
		onTime := now.Sub(newest.At) <= spec.window()
		missed := total
		if onTime {
			missed--
		}
		if missed > 0 {
			last := occs[len(occs)-1]
			if onTime {
				last = occs[len(occs)-2]
			}
			first := occs[0]
			r := s.snapshot(y, p, spec, now)
			r.ID, r.Kind, r.Key, r.State = p.ID+"-x-"+first.Key, RunMissed, first.Key, StateMissed
			r.At, r.Due, r.First, r.Last, r.Count, r.CountCapped = first.At, first.At, first.At, last.At, missed, capped
			r.Reason = "yesman was not running, or the computer was asleep, when it was due"
			if _, err := s.claimLocked(sp, y, r); err != nil && !errors.Is(err, errClaimed) {
				sp.problem[p.ID] = "Could not record a missed run: " + err.Error()
				return nil, time.Time{}, false
			}
			changed = true
		}
		if onTime {
			r := s.snapshot(y, p, spec, now)
			r.ID, r.Key, r.At, r.Due = p.ID+"-"+newest.Key, newest.Key, newest.At, newest.At
			if prev := s.openRunLocked(sp, p.ID, tasks, boardErr); prev != nil {
				r.Kind, r.State, r.Prev = RunSkipped, StateSkipped, prev.ID
				r.Reason = "its last run was still open"
				if prev.Board != "" {
					r.Reason = fmt.Sprintf("its last run (board task %s) was still open", prev.Board)
				}
				if _, err := s.claimLocked(sp, y, r); err != nil && !errors.Is(err, errClaimed) {
					sp.problem[p.ID] = "Could not record a skipped run: " + err.Error()
					return nil, time.Time{}, false
				}
			} else {
				r.Kind, r.State = RunScheduled, StateClaimed
				rec, err := s.claimLocked(sp, y, r)
				switch {
				case errors.Is(err, errClaimed):
				case err != nil:
					// Nothing was claimed: the next check tries again while on time.
					sp.problem[p.ID] = "Could not record the run: " + err.Error()
					return nil, now.Add(s.cfg.Interval), false
				default:
					delete(sp.problem, p.ID)
					if s.cfg.afterClaim != nil && s.cfg.afterClaim(rec.Run) != nil {
						break
					}
					sp.active[rec.ID], sp.fresh[rec.ID] = true, true
					launch = append(launch, rec.ID)
				}
			}
			changed = true
		}
		records = plansRuns(sp.runs, p.ID)
		from = cursor(p, records)
	}
	next, ok := spec.next(maxTime(from, now), loc, p.ActiveSince)
	if !ok {
		return launch, time.Time{}, changed
	}
	return launch, next.At, changed
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// openRunLocked returns a plan's newest run that still has work.
func (s *Service) openRunLocked(sp *space, plan string, tasks map[string]yesmen.Task, boardErr error) *record {
	for _, r := range plansRuns(sp.runs, plan) {
		if open(r.Run, tasks, boardErr) {
			return r
		}
	}
	return nil
}

// mayDrive reports whether this scheduler may carry a run on its way: one it
// claimed itself, or any once it holds the data directory's lock, which no
// other live app then holds. Without a lock, two schedulers that both claim
// to own the tasks never carry each other's runs.
func (s *Service) mayDrive(r Run) bool {
	return r.Claimer == s.id || s.cfg.LockPath != ""
}

// snapshot starts a run of a plan as it is now.
func (s *Service) snapshot(y yesmen.Yesman, p Plan, spec Spec, now time.Time) Run {
	return Run{
		Claimer: s.id,
		Plan:    p.ID, Revision: p.Revision, ClaimedAt: now, UpdatedAt: now,
		Title: p.Title, Instructions: p.Instructions, Schedule: spec, Timezone: p.Timezone,
		Agent: agent.ID(y.Name, yesmen.LeadName), AgentSince: y.HiredAt,
	}
}

// followBoardLocked mirrors each run's board task status into its record,
// so its history keeps the outcome after the task is removed.
func (s *Service) followBoardLocked(sp *space, y yesmen.Yesman, tasks map[string]yesmen.Task, boardErr error, now time.Time) bool {
	if boardErr != nil {
		return false
	}
	changed := false
	for _, id := range sortedIDs(sp.runs) {
		r := sp.runs[id]
		if !r.fired() || r.Board == "" || r.Outcome != nil && r.Outcome.Removed {
			continue
		}
		var outcome Outcome
		if t, ok := taskOf(r.Run, tasks); ok {
			outcome = Outcome{Status: t.Status, Note: clip(t.Note, 300), At: t.UpdatedAt}
		} else {
			// Its task was removed: keep how it last stood.
			outcome = Outcome{Status: "removed", At: now}
			if r.Outcome != nil {
				outcome = *r.Outcome
			}
			outcome.Removed = true
		}
		if r.Outcome != nil && *r.Outcome == outcome {
			continue
		}
		if _, err := s.updateLocked(sp, y, id, func(r *Run) bool { r.Outcome = &outcome; return true }); err == nil {
			changed = true
		}
	}
	return changed
}

// pruneLocked keeps each plan's history bounded.
func (s *Service) pruneLocked(sp *space, y yesmen.Yesman, tasks map[string]yesmen.Task, boardErr error) bool {
	byPlan := map[string][]*record{}
	for _, r := range sp.runs {
		byPlan[r.Plan] = append(byPlan[r.Plan], r)
	}
	changed := false
	for plan := range byPlan {
		list := plansRuns(sp.runs, plan)
		if len(list) <= keepRuns {
			continue
		}
		// The record the cursor rests on stays, whatever its age: chosen as
		// cursor chooses it, or the plan's schedule would start over.
		anchor := cursorRecord(list)
		for _, r := range prune(list, func(r *record) bool { return open(r.Run, tasks, boardErr) }) {
			if r == anchor || sp.active[r.ID] {
				continue
			}
			if err := os.Remove(runPath(y, r.ID)); err == nil || errors.Is(err, os.ErrNotExist) {
				delete(sp.runs, r.ID)
				changed = true
			}
		}
	}
	return changed
}

// ---------- Delivery ----------

// enqueue adds a run to its workspace's delivery queue.
func (s *Service) enqueue(sp *space, id string) {
	sp.mu.Lock()
	sp.queue = append(sp.queue, id)
	start := !sp.working
	sp.working = true
	sp.mu.Unlock()
	if !start {
		return
	}
	s.work.Add(1)
	go func() {
		defer s.work.Done()
		for {
			sp.mu.Lock()
			if len(sp.queue) == 0 {
				sp.working = false
				sp.mu.Unlock()
				return
			}
			id := sp.queue[0]
			sp.queue = sp.queue[1:]
			sp.mu.Unlock()
			select {
			case s.sem <- struct{}{}:
				s.pipeline(sp, id)
				<-s.sem
			case <-s.stop:
			}
			sp.mu.Lock()
			delete(sp.active, id)
			sp.mu.Unlock()
		}
	}()
}

func (s *Service) current(sp *space, id string) (Run, bool) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	r := sp.runs[id]
	if r == nil {
		return Run{}, false
	}
	return r.Run, true
}

// late reports whether a run picked up again is past the window in which it
// may still be sent by itself: a run left on its way that long ago, by a
// crash or a quit, is not sent after all, so a closed app or a long sleep
// never floods the lead. A fresh run, claimed on time and queued since, is
// never late, however long it waits for its turn.
func (s *Service) late(r Run, fresh bool) bool {
	if fresh {
		return false
	}
	window := OnTime
	if r.Kind == RunScheduled {
		window = r.Schedule.window()
	}
	return s.now().Sub(r.Due) > window
}

// pipeline carries a claimed run to the lead: its board task, a stable
// submission ID, then the submission. Every step is recorded before the
// next, so a crash leaves a state the next start resumes from.
func (s *Service) pipeline(sp *space, id string) {
	sp.mu.Lock()
	fresh := sp.fresh[id]
	delete(sp.fresh, id)
	sp.mu.Unlock()
	for range 6 {
		if s.isClosed() || sp.retired.Load() {
			return
		}
		r, ok := s.current(sp, id)
		if !ok {
			return
		}
		var more bool
		switch r.State {
		case StateClaimed:
			more = s.stepBoard(sp, r, fresh)
		case StateBoarded:
			more = s.stepAttempt(sp, r, fresh)
		case StateSending:
			s.stepSend(sp, r, fresh)
		}
		s.changed(sp.name)
		if !more {
			return
		}
	}
}

func (s *Service) stepBoard(sp *space, r Run, fresh bool) bool {
	y, release, err := s.lease(sp)
	if err != nil {
		return false
	}
	defer release()
	link := yesmen.TaskSchedule{Plan: r.Plan, Run: r.ID}
	if s.late(r, fresh) {
		// Left claimed by a crash or quit long ago: add no task now. One added
		// before the crash is still found.
		board, _ := s.cfg.Yesmen.Board(y.Name)
		for _, t := range board.Tasks {
			if t.Schedule != nil && t.Schedule.Run == r.ID {
				_, err := s.updateLeased(sp, y, r.ID, func(r *Run) bool { r.Board, r.State = t.ID, StateBoarded; return true })
				return err == nil
			}
		}
		_, _ = s.updateLeased(sp, y, r.ID, func(r *Run) bool {
			if r.Kind == RunScheduled {
				r.Kind, r.State, r.First, r.Last, r.Count = RunMissed, StateMissed, r.At, r.At, 1
				r.Reason = "yesman stopped before this run started"
			} else {
				r.State, r.Reason = StateFailed, "Not sent: yesman stopped before it could send it"
			}
			return true
		})
		return false
	}
	task, added, err := s.cfg.Yesmen.AddScheduledTask(y.Name, link, r.Title, boardDetail(r))
	if err != nil {
		_, _ = s.updateLeased(sp, y, r.ID, func(r *Run) bool {
			r.State, r.Reason = StateFailed, "Not sent: its board task could not be added: "+clip(err.Error(), 300)
			return true
		})
		return false
	}
	if added {
		s.cfg.BoardChanged(y.Name)
	}
	next, err := s.updateLeased(sp, y, r.ID, func(r *Run) bool { r.Board, r.State = task.ID, StateBoarded; return true })
	if err != nil {
		return false
	}
	if s.cfg.afterBoard != nil && s.cfg.afterBoard(next) != nil {
		return false
	}
	return true
}

// updateLeased is update for a caller that holds the workspace already.
func (s *Service) updateLeased(sp *space, y yesmen.Yesman, id string, fn func(*Run) bool) (Run, error) {
	if sp.retired.Load() {
		return Run{}, errRetired
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return s.updateLocked(sp, y, id, fn)
}

// newSubmissionID makes a submission ID: a 12-hex millisecond timestamp,
// as the receipt store requires, and random.
func (s *Service) newSubmissionID() string {
	return fmt.Sprintf("%012x%s", s.cfg.IDNow().UnixMilli(), randomHex(18))
}

func (s *Service) stepAttempt(sp *space, r Run, fresh bool) bool {
	y, release, err := s.lease(sp)
	if err != nil {
		return false
	}
	defer release()
	if s.late(r, fresh) {
		_, _ = s.updateLeased(sp, y, r.ID, func(r *Run) bool {
			r.State, r.Reason = StateFailed, "Not sent: yesman stopped before it could send it"
			return true
		})
		return false
	}
	next, err := s.updateLeased(sp, y, r.ID, func(r *Run) bool {
		r.Attempts = append(r.Attempts, Attempt{ID: s.newSubmissionID(), At: s.now()})
		r.State, r.Reason = StateSending, ""
		return true
	})
	if err != nil {
		return false
	}
	if s.cfg.afterAttempt != nil && s.cfg.afterAttempt(next) != nil {
		return false
	}
	return true
}

// stepSend submits a run's current attempt, unless its receipt shows it was
// submitted already. An ID is submitted only when it is proven never to have
// been registered; anything else is resolved from its receipt or shown as
// unknown, never sent under a new ID by itself.
func (s *Service) stepSend(sp *space, r Run, fresh bool) {
	a := r.attempt()
	if a == nil {
		_, _ = s.update(sp, r.ID, func(r *Run) bool { r.State = StateBoarded; return true })
		return
	}
	lead := agent.ID(sp.name, yesmen.LeadName)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	d, found, err := s.cfg.Agents.LookupDelivery(ctx, lead, a.ID, sp.since)
	cancel()
	if s.isClosed() || sp.retired.Load() {
		return
	}
	switch {
	case err != nil:
		s.resolve(sp, r.ID, a.ID, nil, "yesman could not read its delivery record ("+clip(err.Error(), 200)+"), so it was not sent again")
		return
	case found == agent.DeliveryFound:
		s.resolve(sp, r.ID, a.ID, &d, "")
		return
	case found == agent.DeliveryUnknown:
		s.resolve(sp, r.ID, a.ID, nil, "Its delivery record is no longer kept, so yesman cannot confirm it reached the lead; it was not sent again")
		return
	}
	if s.late(r, fresh) {
		_, _ = s.update(sp, r.ID, func(r *Run) bool {
			if at := r.attempt(); at != nil && at.ID == a.ID {
				at.Definite, at.Error = true, "never sent"
			}
			r.State, r.Reason = StateFailed, "Not sent: yesman stopped before it could send it"
			return true
		})
		return
	}
	text := message(r)
	origin := agent.Origin{Source: agent.OriginSchedule, Plan: r.Plan, Run: r.ID}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	d, err = s.cfg.Agents.SubmitFollowUp(ctx, lead, a.ID, text, origin, sp.since)
	cancel()
	if s.isClosed() || sp.retired.Load() {
		return // a quit: the next start resolves it from its receipt
	}
	if err != nil {
		// Submit returns errors only before registering; check, in case an
		// earlier call registered it.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		d2, found, lerr := s.cfg.Agents.LookupDelivery(ctx, lead, a.ID, sp.since)
		cancel()
		switch {
		case lerr == nil && found == agent.DeliveryFound:
			s.resolve(sp, r.ID, a.ID, &d2, "")
		case lerr == nil && found == agent.DeliveryAbsent:
			_, _ = s.update(sp, r.ID, func(r *Run) bool {
				if at := r.attempt(); at != nil && at.ID == a.ID {
					at.Definite, at.Error, at.State = true, clip(err.Error(), 300), "failed"
				}
				r.State, r.Reason = StateFailed, "Not sent: "+clip(err.Error(), 300)
				return true
			})
		default:
			s.resolve(sp, r.ID, a.ID, nil, "yesman cannot confirm whether it reached the lead ("+clip(err.Error(), 200)+"); it was not sent again")
		}
		return
	}
	s.resolve(sp, r.ID, a.ID, &d, "")
}

// resolve records what a receipt says of a run's attempt; nil records an
// attempt whose fate cannot be known.
func (s *Service) resolve(sp *space, id, attemptID string, d *agent.Delivery, reason string) {
	_, _ = s.update(sp, id, func(r *Run) bool { return applyReceipt(r, attemptID, d, reason) })
}

// applyReceipt maps a native receipt onto a run. Acceptance by pi is a fact
// that later uncertainty does not undo; a queued message that pi lost when
// its process ended becomes unknown.
func applyReceipt(r *Run, attemptID string, d *agent.Delivery, reason string) bool {
	a := r.attempt()
	if a == nil || a.ID != attemptID {
		return false
	}
	if d == nil {
		if r.State != StateSending && r.State != StateUnknown {
			return false
		}
		a.State, a.Error = "unknown", clip(reason, 300)
		r.State, r.Reason = StateUnknown, reason
		return true
	}
	before, state, reason := *a, r.State, r.Reason
	a.State, a.Error = d.State, clip(d.Error, 300)
	unsettled := r.State == StateSending || r.State == StateUnknown
	switch {
	case d.State == "sending":
		// Still being registered: a later update says how it went.
	case d.State == "failed":
		// pi refused it, or it never reached pi: definitely not delivered.
		if unsettled {
			a.Definite = true
			r.State, r.Reason = StateFailed, "Not sent: "+clip(firstNonEmpty(d.Error, "pi refused it"), 300)
		}
	case d.Ack == "queued" && d.State == "interrupted":
		// Accepted into pi's queue, then its process ended before the queue
		// was read: it may never have reached the lead.
		if unsettled || r.State == StateQueued {
			r.State, r.Reason = StateUnknown, "The lead stopped while it was queued, so it may not have reached the lead; it was not sent again"
		}
	case d.Ack == "queued":
		if unsettled {
			r.State, r.Reason = StateQueued, ""
		}
	case d.Ack != "":
		if unsettled {
			r.State, r.Reason = StateSent, ""
		}
	case d.State == "unconfirmed" || d.State == "interrupted":
		// No answer from pi: it may or may not have it.
		if r.State == StateSending {
			r.State, r.Reason = StateUnknown, "pi did not confirm it received the message; it was not sent again"
		}
	}
	return *a != before || r.State != state || r.Reason != reason
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// OnAgentEvent follows the receipts of scheduled runs. It never blocks the
// agent's reader: updates are applied by the scheduler's own goroutine.
func (s *Service) OnAgentEvent(e agent.Event) {
	if !json.Valid(e.Data) || !strings.Contains(string(e.Data), `"origin"`) {
		return
	}
	select {
	case s.events <- e:
	default: // the next start or a check resolves it from the receipt
	}
}

func (s *Service) handleEvent(e agent.Event) {
	var ev struct {
		Type     string         `json:"type"`
		Delivery agent.Delivery `json:"delivery"`
	}
	if json.Unmarshal(e.Data, &ev) != nil || ev.Type != "delivery_update" || ev.Delivery.Origin == nil || ev.Delivery.Origin.Source != agent.OriginSchedule {
		return
	}
	yesman, name, err := agent.SplitID(e.Agent)
	if err != nil || name != yesmen.LeadName {
		return
	}
	s.mu.Lock()
	sp := s.spaces[yesman]
	s.mu.Unlock()
	if sp == nil || !sp.since.Equal(e.AgentSince) {
		return
	}
	d := ev.Delivery
	if _, err := s.update(sp, d.Origin.Run, func(r *Run) bool { return applyReceipt(r, d.ID, &d, "") }); err == nil {
		s.changed(yesman)
	}
}

// ---------- Plans ----------

// PlanInput is a plan as the form or the lead gives it.
type PlanInput struct {
	ID           string `json:"id,omitempty"`
	Revision     int    `json:"revision,omitempty"`
	Title        string `json:"title"`
	Instructions string `json:"instructions"`
	Schedule     Spec   `json:"schedule"`
	Timezone     string `json:"timezone"`
	Enabled      bool   `json:"enabled"`
}

func (in PlanInput) normalize(now time.Time) (PlanInput, *time.Location, error) {
	in.Title = oneLine(in.Title)
	if in.Title == "" {
		return in, nil, fieldError("title", "Give the task a title.")
	}
	if !utf8.ValidString(in.Title) || utf8.RuneCountInString(in.Title) > MaxTitle {
		return in, nil, fieldError("title", "Keep the title to %d characters.", MaxTitle)
	}
	in.Instructions = strings.TrimSpace(strings.ReplaceAll(in.Instructions, "\r\n", "\n"))
	if in.Instructions == "" {
		return in, nil, fieldError("instructions", "Say what yesman should do.")
	}
	if !utf8.ValidString(in.Instructions) || utf8.RuneCountInString(in.Instructions) > MaxInstructions {
		return in, nil, fieldError("instructions", "Keep the instructions to %d characters.", MaxInstructions)
	}
	if strings.ContainsFunc(in.Title+in.Instructions, func(r rune) bool { return r < 0x20 && r != '\n' && r != '\t' || r == 0x7f }) {
		return in, nil, fieldError("instructions", "Remove control characters.")
	}
	spec, err := in.Schedule.normalize()
	if err != nil {
		return in, nil, err
	}
	in.Schedule = spec
	loc, err := LoadZone(in.Timezone)
	if err != nil {
		return in, nil, err
	}
	return in, loc, nil
}

func (s *Service) ownerError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if !s.ownerLocked() {
		return ErrNotOwner
	}
	return nil
}

// edit runs a change of a workspace's plans with it held and loaded.
func (s *Service) edit(name string, fn func(sp *space, y yesmen.Yesman, f *plansFile, now time.Time) error) error {
	if err := s.ownerError(); err != nil {
		return err
	}
	y, release, err := s.cfg.Yesmen.BeginUse(name)
	if err != nil {
		return err
	}
	defer release()
	sp := s.spaceFor(y)
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.retired.Load() {
		return errRetired
	}
	s.loadLocked(sp, y)
	if sp.err != nil {
		return sp.err
	}
	f := sp.plans
	f.Plans = slices.Clone(f.Plans)
	f.planExtras = maps.Clone(f.planExtras)
	if err := fn(sp, y, &f, s.now()); err != nil {
		return err
	}
	return s.writePlansLocked(sp, y, f)
}

func findPlan(f *plansFile, id string) (int, error) {
	i := slices.IndexFunc(f.Plans, func(p Plan) bool { return p.ID == id })
	if i < 0 {
		return -1, fmt.Errorf("no task %s", id)
	}
	return i, nil
}

func activeCount(plans []Plan) int {
	n := 0
	for _, p := range plans {
		if !p.Archived() {
			n++
		}
	}
	return n
}

// dueAhead checks that an enabled once plan still has its time ahead.
func dueAhead(p Plan, loc *time.Location, now time.Time) error {
	if !p.Enabled || p.Schedule.Kind != KindOnce {
		return nil
	}
	if _, ok := p.Schedule.next(now, loc, p.ActiveSince); !ok {
		return fieldError("time", "That time has passed. Choose a later time, or save it turned off and use Run now.")
	}
	return nil
}

// Save creates a plan, or changes one saved at the revision given. A plan
// saved on is the user's authorization to send its request at its times.
func (s *Service) Save(name string, in PlanInput) (Plan, error) {
	return s.save(name, in, "user")
}

// Draft saves a plan for the lead, at the user's request; it is always off
// until the user turns it on in Tasks.
func (s *Service) Draft(name string, in PlanInput) (Plan, error) {
	in.ID, in.Revision, in.Enabled = "", 0, false
	return s.save(name, in, "lead")
}

func (s *Service) save(name string, in PlanInput, by string) (Plan, error) {
	var saved Plan
	err := s.edit(name, func(sp *space, y yesmen.Yesman, f *plansFile, now time.Time) error {
		in, loc, err := in.normalize(now)
		if err != nil {
			return err
		}
		var p Plan
		if in.ID == "" {
			if activeCount(f.Plans) >= MaxActivePlans {
				return fmt.Errorf("a yesman keeps at most %d tasks: archive one first", MaxActivePlans)
			}
			for len(f.Plans) >= maxPlans {
				old := archivedPlans(f.Plans)
				if len(old) == 0 {
					return fmt.Errorf("a yesman keeps at most %d tasks", maxPlans)
				}
				s.forgetPlanLocked(sp, y, f, old[0].ID)
			}
			f.Next++
			p = Plan{ID: fmt.Sprintf("s%d", f.Next), CreatedAt: now, CreatedBy: by, Revision: 0}
		} else {
			i, err := findPlan(f, in.ID)
			if err != nil {
				return err
			}
			p = f.Plans[i]
			if p.Archived() {
				return errors.New("this task is archived")
			}
			if p.Revision != in.Revision {
				return ErrConflict
			}
		}
		reset := in.ID == "" || !p.Enabled && in.Enabled || !specEqual(p.Schedule, in.Schedule) || p.Timezone != in.Timezone
		p.Title, p.Instructions, p.Schedule, p.Timezone, p.Enabled = in.Title, in.Instructions, in.Schedule, in.Timezone, in.Enabled
		if reset {
			p.ActiveSince = now
		}
		if err := dueAhead(p, loc, now); err != nil {
			return err
		}
		p.Revision++
		p.UpdatedAt = now
		if in.ID == "" {
			f.Plans = append(f.Plans, p)
		} else {
			i, _ := findPlan(f, in.ID)
			f.Plans[i] = p
		}
		delete(sp.problem, p.ID)
		saved = p
		return nil
	})
	if err == nil {
		s.changed(name)
		s.Wake()
	}
	return saved, err
}

func specEqual(a, b Spec) bool {
	return a.Kind == b.Kind && a.Date == b.Date && a.Time == b.Time && a.Every == b.Every && slices.Equal(a.Days, b.Days)
}

// forgetPlanLocked removes an archived plan and its history, the oldest
// first, to keep the file bounded.
func (s *Service) forgetPlanLocked(sp *space, y yesmen.Yesman, f *plansFile, id string) {
	f.Plans = slices.DeleteFunc(f.Plans, func(p Plan) bool { return p.ID == id })
	delete(f.planExtras, id)
	for rid, r := range sp.runs {
		if r.Plan == id && !r.inFlight() {
			if err := os.Remove(runPath(y, rid)); err == nil || errors.Is(err, os.ErrNotExist) {
				delete(sp.runs, rid)
			}
		}
	}
}

// SetEnabled turns a plan on or off. Off stops its future runs; a run
// already on its way goes on, and nothing in the lead's queue is touched.
func (s *Service) SetEnabled(name, id string, enabled bool, revision int) (Plan, error) {
	var saved Plan
	err := s.edit(name, func(sp *space, y yesmen.Yesman, f *plansFile, now time.Time) error {
		i, err := findPlan(f, id)
		if err != nil {
			return err
		}
		p := f.Plans[i]
		if p.Archived() {
			return errors.New("this task is archived")
		}
		if p.Revision != revision {
			return ErrConflict
		}
		if p.Enabled == enabled {
			saved = p
			return nil
		}
		p.Enabled = enabled
		if enabled {
			loc, err := LoadZone(p.Timezone)
			if err != nil {
				return err
			}
			if _, err := p.Schedule.normalize(); err != nil {
				return err
			}
			p.ActiveSince = now
			if err := dueAhead(p, loc, now); err != nil {
				return err
			}
		}
		p.Revision++
		p.UpdatedAt = now
		f.Plans[i] = p
		saved = p
		return nil
	})
	if err == nil {
		s.changed(name)
		s.Wake()
	}
	return saved, err
}

// Archive puts a plan away: it never runs again, and its history stays.
func (s *Service) Archive(name, id string, revision int) error {
	err := s.edit(name, func(sp *space, y yesmen.Yesman, f *plansFile, now time.Time) error {
		i, err := findPlan(f, id)
		if err != nil {
			return err
		}
		p := f.Plans[i]
		if p.Archived() {
			return nil
		}
		if p.Revision != revision {
			return ErrConflict
		}
		p.Enabled, p.ArchivedAt, p.UpdatedAt = false, now, now
		p.Revision++
		f.Plans[i] = p
		return nil
	})
	if err == nil {
		s.changed(name)
	}
	return err
}

var requestRE = regexp.MustCompile(`^[a-f0-9]{16,32}$`)

// RunNow sends a plan's request now, for the user: a manual run, or with
// from a missed record, catching that up. requestID names the click, so
// that the same request repeated returns the same run.
func (s *Service) RunNow(name, id, requestID, from string) (Run, error) {
	if !requestRE.MatchString(requestID) {
		return Run{}, errors.New("invalid request ID")
	}
	if err := s.ownerError(); err != nil {
		return Run{}, err
	}
	y, release, err := s.cfg.Yesmen.BeginUse(name)
	if err != nil {
		return Run{}, err
	}
	defer release()
	sp := s.spaceFor(y)
	sp.mu.Lock()
	if sp.retired.Load() {
		sp.mu.Unlock()
		return Run{}, errRetired
	}
	s.loadLocked(sp, y)
	if sp.err != nil {
		sp.mu.Unlock()
		return Run{}, sp.err
	}
	i := slices.IndexFunc(sp.plans.Plans, func(p Plan) bool { return p.ID == id })
	if i < 0 {
		sp.mu.Unlock()
		return Run{}, fmt.Errorf("no task %s", id)
	}
	p := sp.plans.Plans[i]
	if p.Archived() {
		sp.mu.Unlock()
		return Run{}, errors.New("this task is archived")
	}
	now := s.now()
	r := s.snapshot(y, p, p.Schedule, now)
	r.At, r.Due, r.State = now, now, StateClaimed
	var missed *record
	if from == "" {
		r.ID, r.Kind = p.ID+"-m-"+requestID, RunManual
	} else {
		missed = sp.runs[from]
		if missed == nil || missed.Plan != p.ID || missed.Kind != RunMissed {
			sp.mu.Unlock()
			return Run{}, errors.New("there is no such missed run of this task")
		}
		r.ID, r.Kind, r.From = from+"-c", RunCatchUp, from
		r.Key, r.First, r.Last, r.Count, r.CountCapped = missed.Key, missed.First, missed.Last, missed.Count, missed.CountCapped
		if missed.CaughtUp != "" && missed.CaughtUp != r.ID {
			sp.mu.Unlock()
			return Run{}, errors.New("this missed run was caught up already")
		}
	}
	if existing := sp.runs[r.ID]; existing != nil {
		run := existing.Run
		sp.mu.Unlock()
		return run, nil // the same click again
	}
	for _, other := range plansRuns(sp.runs, p.ID) {
		if other.fired() && other.inFlight() {
			sp.mu.Unlock()
			return Run{}, errors.New("this task's last run is still being sent: try again once it has been sent")
		}
	}
	rec, err := s.claimLocked(sp, y, r)
	if errors.Is(err, errClaimed) && sp.runs[r.ID] != nil {
		run := sp.runs[r.ID].Run
		sp.mu.Unlock()
		return run, nil
	}
	if err != nil {
		sp.mu.Unlock()
		return Run{}, fmt.Errorf("the run could not be recorded: %w", err)
	}
	if missed != nil {
		_, _ = s.updateLocked(sp, y, missed.ID, func(m *Run) bool { m.CaughtUp = rec.ID; return true })
	}
	sp.active[rec.ID], sp.fresh[rec.ID] = true, true
	run := rec.Run
	sp.mu.Unlock()
	release()
	s.enqueue(sp, run.ID)
	s.changed(name)
	return run, nil
}

// Retry sends a run that was definitely not sent again, under a new
// submission ID, keeping its board task.
func (s *Service) Retry(name, runID string) (Run, error) {
	if err := s.ownerError(); err != nil {
		return Run{}, err
	}
	y, release, err := s.cfg.Yesmen.BeginUse(name)
	if err != nil {
		return Run{}, err
	}
	defer release()
	sp := s.spaceFor(y)
	sp.mu.Lock()
	s.loadLocked(sp, y)
	rec := sp.runs[runID]
	if rec == nil || sp.retired.Load() {
		sp.mu.Unlock()
		return Run{}, fmt.Errorf("no run %s", runID)
	}
	if !canRetry(rec.Run) || sp.active[runID] {
		sp.mu.Unlock()
		return Run{}, errors.New("only a run that was definitely not sent can be sent again: use Run now for a new run")
	}
	if p := planOf(sp.plans, rec.Plan); p == nil || p.Archived() {
		sp.mu.Unlock()
		return Run{}, errors.New("this task is archived")
	}
	now := s.now()
	run, err := s.updateLocked(sp, y, runID, func(r *Run) bool {
		r.Due, r.Reason, r.Claimer = now, "", s.id
		r.State = StateClaimed
		if r.Board != "" {
			r.State = StateBoarded
		}
		return true
	})
	if err != nil {
		sp.mu.Unlock()
		return Run{}, err
	}
	sp.active[runID], sp.fresh[runID] = true, true
	sp.mu.Unlock()
	release()
	s.enqueue(sp, runID)
	s.changed(name)
	return run, nil
}

// Check reads a run's receipt again, without sending anything.
func (s *Service) Check(name, runID string) (Run, error) {
	if err := s.ownerError(); err != nil {
		return Run{}, err
	}
	y, err := s.cfg.Yesmen.Get(name)
	if err != nil {
		return Run{}, err
	}
	sp := s.spaceFor(y)
	sp.mu.Lock()
	s.loadLocked(sp, y)
	rec, active := sp.runs[runID], sp.active[runID]
	var r Run
	if rec != nil {
		// A copy, taken under the lock: updates replace the record whole.
		r = rec.Run
	}
	sp.mu.Unlock()
	if rec == nil {
		return Run{}, fmt.Errorf("no run %s", runID)
	}
	a := r.attempt()
	if a == nil || r.inFlight() || active {
		return r, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	d, found, err := s.cfg.Agents.LookupDelivery(ctx, agent.ID(name, yesmen.LeadName), a.ID, sp.since)
	cancel()
	if err != nil {
		return r, err
	}
	switch found {
	case agent.DeliveryFound:
		r, err = s.update(sp, runID, func(r *Run) bool { return applyReceipt(r, a.ID, &d, "") })
	case agent.DeliveryAbsent:
		if r.State == StateUnknown {
			// It was never registered: definitely not sent.
			r, err = s.update(sp, runID, func(r *Run) bool {
				if at := r.attempt(); at != nil && at.ID == a.ID {
					at.Definite, at.State = true, "absent"
				}
				r.State, r.Reason = StateFailed, "Not sent: the lead never received it"
				return true
			})
		}
	}
	s.changed(name)
	return r, err
}

func canRetry(r Run) bool {
	if !r.fired() || r.State != StateFailed || len(r.Attempts) >= maxAttempts {
		return false
	}
	a := r.attempt()
	return a == nil || a.Definite
}

func planOf(f plansFile, id string) *Plan {
	for i := range f.Plans {
		if f.Plans[i].ID == id {
			return &f.Plans[i]
		}
	}
	return nil
}

// ---------- The message ----------

func zoneOrUTC(name string) *time.Location {
	if loc, err := LoadZone(name); err == nil {
		return loc
	}
	return time.UTC
}

// message is what the lead reads for a run: what the user asked and how to
// account for it. It is built only from the run's record, so the same run
// always sends the same text under its submission ID.
func message(r Run) string {
	loc := zoneOrUTC(r.Timezone)
	at := func(t time.Time) string { return t.In(loc).Format("Mon 2 Jan 2006 15:04 MST") }
	var b strings.Builder
	switch r.Kind {
	case RunManual:
		fmt.Fprintf(&b, "[yesman] The user ran scheduled task %s %q now, from Tasks.", r.Plan, r.Title)
	case RunCatchUp:
		times := "once"
		if r.Count != 1 {
			times = fmt.Sprintf("%d times", r.Count)
			if r.CountCapped {
				times = fmt.Sprintf("more than %d times", r.Count)
			}
		}
		fmt.Fprintf(&b, "[yesman] The user is catching up on scheduled task %s %q, which was missed %s between %s and %s while yesman was not running. Run it once, for now.", r.Plan, r.Title, times, at(r.First), at(r.Last))
	default:
		fmt.Fprintf(&b, "[yesman] Scheduled task %s %q is due: it runs %s (%s), and this run was planned for %s.", r.Plan, r.Title, r.Schedule.Describe(), r.Timezone, at(r.At))
	}
	fmt.Fprintf(&b, " Board task %s tracks this run.\n\nThe user's instructions:\n\n%s\n\n", r.Board, r.Instructions)
	fmt.Fprintf(&b, "Handle it as a request from the user: plan it, hand the work to a worker when it needs one, and keep %[1]s current. When the work ends, mark %[1]s done, or blocked saying why, with the outcome in its note, and record it in the task log (the Working log) as usual. Being scheduled does not widen what the user has authorized: ask before anything else.", r.Board)
	return b.String()
}

// boardDetail is the detail of a run's board task.
func boardDetail(r Run) string {
	loc := zoneOrUTC(r.Timezone)
	var head string
	switch r.Kind {
	case RunManual:
		head = fmt.Sprintf("Scheduled task %s, run by the user from Tasks at %s.", r.Plan, r.At.In(loc).Format("2006-01-02 15:04 MST"))
	case RunCatchUp:
		head = fmt.Sprintf("Scheduled task %s, caught up by the user at %s after %d missed run(s).", r.Plan, r.At.In(loc).Format("2006-01-02 15:04 MST"), r.Count)
	default:
		head = fmt.Sprintf("Scheduled task %s (%s, %s): run planned for %s.", r.Plan, r.Schedule.Describe(), r.Timezone, r.At.In(loc).Format("2006-01-02 15:04 MST"))
	}
	return head + "\n\n" + r.Instructions
}

func logf(format string, args ...any) { log.Printf("yesman schedules: "+format, args...) }
