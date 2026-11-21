package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	// writeRun is asked before a run record or pruned.json is written: an
	// error fails the write, as a crash would; nil lets it go on.
	writeRun func(path string, data []byte) error
	// writePlans is asked before plans.json is written, once the change was
	// made from the file read: an error fails the write; nil lets it go on.
	writePlans func(path string, data []byte) error
	// plansLimit replaces maxPlansFile, the most of plans.json read and
	// written, so tests can check the bound with small files.
	plansLimit int64
}

// Errors a caller may show as they are.
var (
	ErrNotOwner = errors.New("another yesman app is running these scheduled tasks, so they are read-only here")
	ErrConflict = errors.New("this task was changed since you opened it: reload to see the latest version, then edit again")
	ErrClosed   = errors.New("yesman is quitting")
	errRetired  = errors.New("this yesman was dismissed")
	// ErrRequestUsed: the click's run was made, and its record has since
	// left the history; it is not taken for a new run.
	ErrRequestUsed = errors.New("this run was already made and its record has left the history, so it was not sent again: use Run now again for a new run")
	errCaughtUp    = errors.New("this missed run was caught up already")
	// ErrRequestAhead: a new click whose request ID carries a time more than
	// a day ahead of the scheduler's clock, which was set back since the page
	// made it (or an ID that does not start with its time). It is refused
	// before anything is recorded: a click taken so would have to be kept
	// exactly for as long, and a new click carries the right time.
	ErrRequestAhead = errors.New("this click carries a time more than a day ahead of this computer's clock, so nothing was sent: use Run now again")
	// ErrRunNowWaits: more clicks of the plan, taken before the computer's
	// clock was set back, left the history than are kept one by one, so
	// every click up to the newest of them may be one of them; Run now
	// sends nothing until the clock has passed it.
	ErrRunNowWaits = errors.New("Run now of this task waits until this computer's clock passes the clicks it took before the clock was set back, so nothing was sent")
	// ErrStaleCaller: the lead that asked was stopped, restarted or
	// dismissed, or the workspace replaced, before the change was made.
	ErrStaleCaller = errors.New("the lead that asked is no longer running in this yesman, so nothing was saved")
	// ErrReplaced: the yesman a request was made for was dismissed, and
	// maybe hired again under its name, since its tasks were shown. Nothing
	// was read, changed or sent.
	ErrReplaced = errors.New("this yesman was dismissed, maybe hired again under the same name, since its tasks were shown: nothing was changed")
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
	// work counts the delivery workers. One is counted only while the
	// service is open, so once Close set closed, work.Wait waits for every
	// worker there will be.
	work sync.WaitGroup
	// ops counts the operations that may write a workspace's files: a save,
	// a click, a check, a tick, a receipt update. Like a worker, one is
	// counted only while the service is open (begin), so once Close set
	// closed, ops.Wait waits for every one there will be.
	ops sync.WaitGroup
	// released is closed once Close let the lock go, after every worker and
	// operation ended.
	released chan struct{}
	// epoch counts the receipt updates missed: one dropped makes every run
	// waiting on a receipt to be checked again.
	epoch atomic.Uint64
}

// space is the scheduler's state of one workspace generation.
type space struct {
	name  string
	since time.Time // the workspace's HiredAt: a same-name hire is another space

	mu     sync.Mutex
	loaded bool
	err    error // the plans file or the runs cannot be read; nothing is written over them
	// runsErr says why the runs could not be read; they are read again.
	runsErr error
	stamp   fileStamp
	plans   plansFile
	runs    map[string]*record
	active  map[string]bool // runs whose pipeline is queued or running
	// fresh holds runs this scheduler claimed (or was asked to send again)
	// and queued at once: waiting their turn does not make them late. A run
	// picked up again later, after a crash, a quit or a step that failed,
	// is not fresh.
	fresh   map[string]bool
	problem map[string]string // plan ID -> why it could not run last time
	queue   []string
	working bool
	// checked holds, for runs whose receipt was read again since they were
	// loaded, the epoch it was read in, plus one.
	checked map[string]uint64
	// pruned is what pruning removed; prunedErr says why it cannot be read,
	// and then nothing is pruned and no click is taken for a new run.
	pruned    prunedFile
	prunedErr error
	// disk is the version of plans.json that plans was read from or written
	// as; reread makes the next load read the file whatever its stamp.
	disk   fileVersion
	reread bool

	retired atomic.Bool
}

type fileStamp struct {
	size int64
	mod  time.Time
	ok   bool
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
	if cfg.plansLimit <= 0 {
		cfg.plansLimit = maxPlansFile
	}
	return &Service{
		cfg: cfg, spaces: map[string]*space{}, id: randomHex(8),
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), released: make(chan struct{}),
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

// closeWait is how long Close waits for the deliveries under way.
const closeWait = 2 * time.Second

// Close stops scheduling: once it is called no run is claimed (beyond one
// being recorded at that moment), and no operation that may write starts. Runs on their way stay recorded as they
// are and are resumed or resolved on the next start. The lock is let go
// once the background check, every delivery and every operation under way
// have ended, and not before, so that no other app takes the tasks over
// while a run may still go out or a record still be written here: when they
// end, whether or not a Close still waits for them. Close waits a little
// for that, for the background check too: one held up by a disk or a file
// that does not answer does not hold up quitting. It may be called again,
// also at once from several goroutines; each call returns once the lock was
// let go, or after the same short wait.
func (s *Service) Close() {
	s.mu.Lock()
	first := !s.closed
	if first {
		s.closed = true
		close(s.stop)
	}
	started := s.started
	s.mu.Unlock()
	if first {
		go s.release(started)
	}
	select {
	case <-s.released:
	case <-time.After(closeWait):
		// A delivery still waits on pi, or a check on a file. The lock goes
		// when it ends; exiting the app drops it too.
	}
}

// stopped reports whether Close was called, without taking s.mu.
func (s *Service) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// release lets the lock go once the background loop, when it was started,
// every delivery worker and every operation have ended. None starts once
// the service is closed (Start, enqueue, begin), so none can start after.
func (s *Service) release(started bool) {
	if started {
		<-s.done
	}
	s.work.Wait()
	s.ops.Wait()
	s.mu.Lock()
	lock := s.lock
	s.lock, s.owner = nil, false
	s.mu.Unlock()
	if lock != nil {
		if err := lock.Close(); err != nil {
			logf("letting the scheduled tasks' lock go: %v", err)
		}
	}
	close(s.released)
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

// spaceFor returns the state of a workspace's current generation, y. Its
// callers hold the workspace (Store.BeginUse) that y came from, so that y
// is still its current generation: a generation it replaces is retired.
func (s *Service) spaceFor(y yesmen.Yesman) *space {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp := s.spaces[y.Name]
	if sp != nil && !sp.since.Equal(y.HiredAt) {
		sp.retired.Store(true)
		sp = nil
	}
	if sp == nil {
		sp = &space{name: y.Name, since: y.HiredAt, active: map[string]bool{}, fresh: map[string]bool{}, problem: map[string]string{}, checked: map[string]uint64{}}
		s.spaces[y.Name] = sp
	}
	return sp
}

// BeginStorageChange holds the data folder's scheduled-task ownership while
// a confirmed deletion retires the workspace. A concurrent Quit must wait
// for the operation before another process can take over scheduling.
func (s *Service) BeginStorageChange() (func(), error) { return s.begin(true) }

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
	// Every file is read through the folder opened here: a missing folder
	// holds nothing yet, a link on the way is an error.
	dir, err := openSchedules(y, false)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if dir != nil {
		defer dir.Close()
	}
	st := stampIn(dir, "plans.json")
	if sp.loaded && !sp.reread && st == sp.stamp && sp.err == nil && err == nil && sp.prunedErr == nil {
		return
	}
	sp.reread = false
	dirErr := err
	var data []byte
	var disk fileVersion
	if dir != nil {
		data, disk, err = readVersionIn(dir, "plans.json", s.cfg.plansLimit)
	}
	var f plansFile
	if err == nil && data != nil {
		f, err = decodePlans(data)
	} else if err == nil {
		f = plansFile{Version: plansVersion, planExtras: map[string]map[string]json.RawMessage{}}
	}
	var plansErr error
	if err != nil {
		plansErr = fmt.Errorf("the tasks file cannot be read, so yesman left it unchanged and runs none of its tasks: %w", err)
		if !sp.loaded {
			sp.plans = plansFile{Version: plansVersion}
		}
	} else {
		// What is saved next must replace this very file and content.
		sp.plans, sp.disk = f, disk
	}
	// The runs are read once, and again while they could not be: until
	// then nothing is claimed, saved or sent for the workspace.
	readRuns := !sp.loaded || sp.runs == nil || sp.runsErr != nil
	if readRuns {
		runs, rerr := readRunsIn(dir)
		if dirErr != nil {
			rerr = dirErr
		}
		sp.runsErr = nil
		if rerr != nil {
			sp.runsErr = fmt.Errorf("the run history cannot be read: %w", rerr)
			runs = map[string]*record{}
		}
		sp.runs = runs
		clear(sp.checked)
	}
	// What pruning removed is read with the runs, and again, alone, while
	// it could not be: a file repaired since is used without a restart.
	// Until then nothing is pruned and no manual run is admitted.
	if readRuns || sp.prunedErr != nil {
		var data []byte
		perr := dirErr
		if dir != nil && perr == nil {
			data, perr = readIn(dir, "pruned.json", maxPrunedFile)
		}
		if perr == nil {
			sp.pruned, perr = decodePruned(data)
		}
		sp.prunedErr = nil
		if perr != nil {
			sp.prunedErr = fmt.Errorf("what yesman keeps of pruned runs (pruned.json) cannot be read, so it left it unchanged: %w", perr)
		}
	}
	sp.err = errors.Join(plansErr, sp.runsErr)
	sp.loaded, sp.stamp = true, st
}

// errTasksTooLarge is why a change that would make plans.json larger than
// yesman reads back is refused.
var errTasksTooLarge = errors.New("the tasks file would be larger than yesman reads back")

// writePlansLocked replaces plans.json with f, within the size it is read
// at. A file that would be larger is not written: the one on disk, and what
// is known of it here, stay as they were. So is a file changed on disk since
// it was read (errChangedOnDisk, an ErrConflict), which the next load reads.
func (s *Service) writePlansLocked(sp *space, y yesmen.Yesman, f plansFile) error {
	data, err := f.encode()
	if err != nil {
		return err
	}
	if int64(len(data)) > s.cfg.plansLimit {
		return fmt.Errorf("%w (%d KiB, at most %d KiB), so nothing was saved: archived tasks and fields this version does not know take room too", errTasksTooLarge, (len(data)+1023)>>10, s.cfg.plansLimit>>10)
	}
	dir, err := openSchedules(y, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	if s.cfg.writePlans != nil {
		if err := s.cfg.writePlans(plansPath(y), data); err != nil {
			return err
		}
	}
	written, err := writeIn(dir, "plans.json", data, &sp.disk)
	if err != nil {
		if errors.Is(err, errChangedOnDisk) {
			sp.reread = true
		}
		return err
	}
	sp.plans, sp.disk, sp.stamp = f, written, written.stamp()
	return nil
}

// runsFolder opens a workspace's runs folder for a write, creating it.
func runsFolder(y yesmen.Yesman) (*os.Root, error) {
	dir, err := openSchedules(y, true)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return openRuns(dir, true)
}

// errRunTooLarge: a run record would be larger than yesman reads back; it
// is not written, and the one on disk stays.
var errRunTooLarge = fmt.Errorf("the run record would be larger than %d KiB, so it was not written", maxRunFile>>10)

func (s *Service) writeRunLocked(y yesmen.Yesman, r *record) error {
	data, err := encodeRun(*r)
	if err != nil {
		return err
	}
	if len(data) > maxRunFile {
		return errRunTooLarge
	}
	if s.cfg.writeRun != nil {
		if err := s.cfg.writeRun(runPath(y, r.ID), data); err != nil {
			return err
		}
	}
	rd, err := runsFolder(y)
	if err != nil {
		return err
	}
	defer rd.Close()
	_, err = writeIn(rd, r.ID+".json", data, nil)
	return err
}

// claimLocked creates a run's record, which must not exist yet.
func (s *Service) claimLocked(sp *space, y yesmen.Yesman, r Run) (*record, error) {
	// Nothing is claimed once Close was called: a check held up past its
	// wait would otherwise claim a run that no one sends now.
	if s.stopped() {
		return nil, ErrClosed
	}
	rec := &record{Run: r}
	data, err := encodeRun(*rec)
	if err != nil {
		return nil, err
	}
	if len(data) > maxRunFile {
		return nil, errRunTooLarge
	}
	rd, err := runsFolder(y)
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	if err := createIn(rd, r.ID+".json", data); err != nil {
		if errors.Is(err, errClaimed) {
			// Made by another tick or process: take theirs.
			sp.runs[r.ID] = readRecordIn(rd, r.ID)
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
	if callsForBoard(rec.Run, next.Run) {
		// Decided before the record is written, and written with it: a
		// quit or a failed board write after this leaves it to be made.
		next.BoardSync = s.decideBoardSync(y, next.Run)
	}
	next.UpdatedAt = s.now()
	if err := s.writeRunLocked(y, &next); err != nil {
		return rec.Run, err
	}
	*rec = next
	s.syncBoardLocked(y, rec)
	return rec.Run, nil
}

// notDelivered starts the note of a run's board task while the run was
// definitely not delivered, so that the lead, and its reminders of open
// tasks, do not take up a request it never got.
const notDelivered = "Scheduled run not delivered to the lead:"

// callsForBoard reports whether a run's change of state from before to
// after changes what its board task should say: it was definitely not
// delivered, or it is sent again.
func callsForBoard(before, after Run) bool {
	return after.Board != "" && after.fired() && (before.State == StateFailed) != (after.State == StateFailed)
}

// decideBoardSync decides the change of a run's board task its new state
// calls for (BoardSync): blocked with a note while it was definitely not
// delivered, and back to do when it is sent again, but only from the state
// the scheduler left it in. A task the lead or the user changed meanwhile,
// or no task, gets none.
func (s *Service) decideBoardSync(y yesmen.Yesman, r Run) *BoardSync {
	want := &BoardSync{Status: yesmen.TaskTodo, At: time.Now().UTC()}
	if r.State == StateFailed {
		reason := clip(strings.TrimSuffix(strings.TrimPrefix(r.Reason, "Not sent: "), "."), 300)
		want.Status, want.Note = yesmen.TaskBlocked, fmt.Sprintf("%s %s. Send it again from Tasks.", notDelivered, reason)
	}
	board, err := s.cfg.Yesmen.Board(y.Name)
	if err != nil {
		return want // From unknown: made only to a task unchanged since At
	}
	for _, t := range board.Tasks {
		if t.Schedule != nil && t.Schedule.Run == r.ID {
			if !want.fits(t) {
				return nil
			}
			want.From = stampOf(t)
			return want
		}
	}
	return nil
}

// fits reports whether a task is in the state the change is made from:
// to do before it is blocked, and blocked by the scheduler before it is to
// do again.
func (b *BoardSync) fits(t yesmen.Task) bool {
	if b.Status == yesmen.TaskBlocked {
		return t.Status == yesmen.TaskTodo
	}
	return t.Status == yesmen.TaskBlocked && strings.HasPrefix(t.Note, notDelivered)
}

// made reports whether the task says what the change makes it say.
func (b *BoardSync) made(t yesmen.Task) bool { return t.Status == b.Status && t.Note == b.Note }

// unchanged reports whether the task is as it stood when the change was
// decided.
func (b *BoardSync) unchanged(t yesmen.Task) bool {
	if b.From != nil {
		now := stampOf(t)
		return now.Status == b.From.Status && now.NoteSum == b.From.NoteSum && now.UpdatedAt.Equal(b.From.UpdatedAt)
	}
	return b.fits(t) && !t.UpdatedAt.After(b.At)
}

// syncBoardLocked makes the change of a run's board task its record still
// has to make (BoardSync), and then forgets it. A change made already (the
// app quit before forgetting it) is not made again, and a task changed or
// removed since is left as it is; both are forgotten too. When the board
// cannot be written, the change stays recorded and the next tick makes it.
func (s *Service) syncBoardLocked(y yesmen.Yesman, rec *record) {
	want := rec.BoardSync
	if want == nil {
		return
	}
	_, changed, err := s.cfg.Yesmen.UpdateScheduledTask(y.Name, rec.ID, func(t *yesmen.Task) bool {
		if want.made(*t) || !want.unchanged(*t) {
			return false
		}
		t.Status, t.Note = want.Status, want.Note
		return true
	})
	if err != nil {
		logf("board task of %s: %v", rec.ID, err)
		return
	}
	if changed {
		s.cfg.BoardChanged(y.Name)
	}
	next := *rec
	next.BoardSync = nil
	if err := s.writeRunLocked(y, &next); err != nil {
		logf("%s: forgetting its board change: %v", rec.ID, err)
		return // the next tick finds it made
	}
	*rec = next
}

// syncBoardsLocked makes the board changes the runs of a workspace still
// have to make. It reports whether it tried any.
func (s *Service) syncBoardsLocked(sp *space, y yesmen.Yesman) bool {
	tried := false
	for _, id := range sortedIDs(sp.runs) {
		if rec := sp.runs[id]; rec.BoardSync != nil {
			s.syncBoardLocked(y, rec)
			tried = true
		}
	}
	return tried
}

func (s *Service) changed(name string) { s.cfg.OnChange(name) }

// ---------- Ticks ----------

// Tick checks every workspace once: claims what is due, records what was
// missed or skipped, resumes runs left on their way and follows their board
// tasks. It returns the next time something is due, zero for none.
func (s *Service) Tick(ctx context.Context) time.Time {
	done, err := s.begin(true)
	if err != nil {
		return time.Time{}
	}
	defer done()
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
	now := s.now()

	sp.mu.Lock()
	// The board is read with the runs held: a run records its task only
	// after adding it, so a board read before a run changed never misses
	// the task of a run seen here, and never counts it removed or closed.
	board, boardErr := s.cfg.Yesmen.Board(y.Name)
	tasks := boardTasks(board, boardErr)
	s.loadLocked(sp, y)
	if sp.err == nil && s.syncBoardsLocked(sp, y) {
		board, boardErr = s.cfg.Yesmen.Board(y.Name)
		tasks = boardTasks(board, boardErr)
	}
	changed := s.followBoardLocked(sp, y, tasks, boardErr, now)
	var launch []string
	// Runs left on their way by a crash or a quit resume or resolve. Runs
	// waiting on a receipt, in the lead's queue or unconfirmed, have it read
	// again once after they were loaded, and after a receipt update was
	// missed: a quit may have ended the queue they were in. Reading sends
	// nothing.
	epoch, checks := s.epoch.Load()+1, 0
	for _, id := range sortedIDs(sp.runs) {
		r := sp.runs[id]
		if sp.active[id] || !s.mayDrive(r.Run) {
			continue
		}
		switch {
		case r.inFlight():
			sp.active[id] = true
			launch = append(launch, id)
		case needsCheck(r.Run) && sp.checked[id] != epoch && checks < maxChecks:
			sp.checked[id] = epoch
			sp.active[id] = true
			launch = append(launch, id)
			checks++
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

// maxChecks bounds the receipts one tick reads again per workspace; the
// rest wait for the next tick.
const maxChecks = 16

// needsCheck reports whether a run's receipt may say more than its record:
// it waits in pi, or its delivery is unknown.
func needsCheck(r Run) bool {
	a := r.attempt()
	return r.fired() && a != nil && (r.waiting() || r.State == StateUnknown && !a.Definite)
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
			prev := s.openRunLocked(sp, p.ID, tasks, boardErr)
			full := prev == nil && retainedCount(sp, p.ID, tasks, boardErr) >= maxOpenRuns
			if prev != nil || full {
				r.Kind, r.State = RunSkipped, StateSkipped
				switch {
				case full:
					r.Reason = fmt.Sprintf("%d of its runs were still open", maxOpenRuns)
				case prev.Board != "":
					r.Prev, r.Reason = prev.ID, fmt.Sprintf("its last run (board task %s) was still open", prev.Board)
				default:
					r.Prev, r.Reason = prev.ID, "its last run was still open"
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
		// A run recorded removed is followed again if its task is back on
		// the board, restored there: its task is open again.
		if !r.fired() || r.Board == "" {
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

// pruneLocked keeps each plan's closed history bounded. What a removed
// record still has to answer for is kept first: the request ID of a manual
// run in pruned.json, and the catch-up of a missed run in the missed
// record. Nothing is removed when that cannot be written.
func (s *Service) pruneLocked(sp *space, y yesmen.Yesman, tasks map[string]yesmen.Task, boardErr error) bool {
	if sp.prunedErr != nil {
		return false
	}
	byPlan := map[string]bool{}
	for _, r := range sp.runs {
		byPlan[r.Plan] = true
	}
	var remove []*record
	requests := map[string][]string{}
	for _, plan := range slices.Sorted(maps.Keys(byPlan)) {
		list := plansRuns(sp.runs, plan)
		if len(list) <= keepRuns {
			continue
		}
		// The record the cursor rests on stays, whatever its age: chosen as
		// cursor chooses it, or the plan's schedule would start over.
		anchor := cursorRecord(list)
		// A run being checked may go: its update then finds it gone.
		old := prune(list, func(r *record) bool { return r == anchor || retained(r.Run, tasks, boardErr) })
		for i := len(old) - 1; i >= 0; i-- { // the oldest first
			r := old[i]
			switch r.Kind {
			case RunCatchUp:
				// A catch-up left by a crash before its missed record
				// learned of it: the missed record learns now.
				if m := sp.runs[r.From]; m != nil && m.CaughtUp == "" {
					if _, err := s.updateLocked(sp, y, m.ID, func(m *Run) bool { m.CaughtUp = r.ID; return true }); err != nil {
						continue
					}
				}
			case RunManual:
				req, ok := strings.CutPrefix(r.ID, r.Plan+"-m-")
				if !ok || !requestRE.MatchString(req) {
					continue // not one this version made: keep it
				}
				requests[plan] = append(requests[plan], req)
			}
			remove = append(remove, r)
		}
	}
	if len(remove) == 0 {
		return false
	}
	if len(requests) > 0 {
		next := sp.pruned.clone()
		latest := latestRequest(s.now())
		for plan, reqs := range requests {
			next.add(plan, reqs, latest)
		}
		if err := s.writePrunedLocked(y, next); err != nil {
			logf("%s: keeping what pruning removes: %v", y.Name, err)
			remove = slices.DeleteFunc(remove, func(r *record) bool { return r.Kind == RunManual })
		} else {
			sp.pruned = next
		}
	}
	rd, err := runsFolder(y)
	if err != nil {
		return false
	}
	defer rd.Close()
	changed := false
	for _, r := range remove {
		if removeIn(rd, r.ID+".json") == nil {
			delete(sp.runs, r.ID)
			delete(sp.checked, r.ID)
			changed = true
		}
	}
	return changed
}

// writePrunedLocked replaces pruned.json, within the size it is read at.
func (s *Service) writePrunedLocked(y yesmen.Yesman, f prunedFile) error {
	data, err := f.encode()
	if err != nil {
		return err
	}
	if len(data) > maxPrunedFile {
		return fmt.Errorf("pruned.json would be larger than %d KiB", maxPrunedFile>>10)
	}
	if s.cfg.writeRun != nil {
		if err := s.cfg.writeRun(prunedPath(y), data); err != nil {
			return err
		}
	}
	dir, err := openSchedules(y, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	_, err = writeIn(dir, "pruned.json", data, nil)
	return err
}

// retainedCount counts a plan's runs that stay whatever their age.
func retainedCount(sp *space, plan string, tasks map[string]yesmen.Task, boardErr error) int {
	n := 0
	for _, r := range sp.runs {
		if r.Plan == plan && r.fired() && r.State != StateDamaged && retained(r.Run, tasks, boardErr) {
			n++
		}
	}
	return n
}

// errTooManyOpen is why a plan with maxOpenRuns runs still open makes no
// new run: its history keeps every open run, so new ones wait for some to
// close.
var errTooManyOpen = fmt.Errorf("this task has %d runs whose board tasks are still open: mark some done or dropped first", maxOpenRuns)

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
	// A worker is counted only while the service is open: Close lets the
	// lock go once the workers counted have ended, and none starts after.
	// (Its run stays recorded as it is, for the next start.)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		sp.mu.Lock()
		sp.working = false
		sp.mu.Unlock()
		return
	}
	s.work.Add(1)
	s.mu.Unlock()
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
		default:
			if !needsCheck(r) {
				return
			}
			if _, err := s.reconcile(sp, r); err != nil {
				logf("%s: reading the receipt of %s: %v", sp.name, r.ID, err)
			}
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
	if d.Revision < a.Rev {
		return false // read before an update applied already
	}
	before, state, reason := *a, r.State, r.Reason
	a.State, a.Error, a.Rev = d.State, clip(d.Error, 300), d.Revision
	unsettled := r.State == StateSending || r.State == StateUnknown
	queued := r.State == StateQueued
	// pi's own queue identifies the submission (its native receipt states)
	// as well as pi's answer to the command does (Ack), and may do so first,
	// or alone in a receipt kept from before. Either is acceptance; neither
	// says the work was done: the board task does.
	switch {
	case d.State == "sending":
		// Still being registered: a later update says how it went.
	case d.State == "failed":
		// pi refused it, or it never reached pi: definitely not delivered.
		if unsettled {
			a.Definite = true
			r.State, r.Reason = StateFailed, "Not sent: "+clip(firstNonEmpty(d.Error, "pi refused it"), 300)
		}
	case d.State == "dispatched" || d.State == "handled":
		// pi took it from its queue for the lead, or handled it.
		if unsettled || queued {
			r.State, r.Reason = StateSent, ""
		}
	case d.State == "cancelled":
		// Taken out of pi's queue before the lead read it.
		if unsettled || queued {
			a.Definite = true
			r.State, r.Reason = StateFailed, "Not sent: it was taken out of the lead's queue before the lead read it"
		}
	case d.State == "interrupted" && (d.Ack == "queued" || queued):
		// Accepted into pi's queue, then its process ended before the queue
		// was read: it may never have reached the lead.
		if unsettled || queued {
			r.State, r.Reason = StateUnknown, "The lead stopped while it was queued, so it may not have reached the lead; it was not sent again"
		}
	case d.State == "queued" || d.Ack == "queued":
		if unsettled {
			r.State, r.Reason = StateQueued, ""
		}
	case d.State == "steering" || d.Ack != "":
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
	default:
		// Dropped: the next tick reads the receipts of the runs waiting on
		// one again.
		s.epoch.Add(1)
		s.Wake()
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
	done, err := s.begin(false)
	if err != nil {
		return // the next start reads the receipt again
	}
	defer done()
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

// begin admits an operation that may write a workspace's files, owning the
// scheduled tasks when owned: it is counted until its done is called, and
// Close lets the lock go only after. Waiting happens without any lock
// held: an operation waits on pi or a file as it would anyway, and Close
// returns after its short wait while the lock stays held until it ends.
func (s *Service) begin(owned bool) (done func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if owned && !s.ownerLocked() {
		return nil, ErrNotOwner
	}
	s.ops.Add(1)
	return s.ops.Done, nil
}

// edit runs a change of a workspace's plans with it held and loaded.
func (s *Service) edit(name string, guard Admit, fn func(sp *space, y yesmen.Yesman, f *plansFile, now time.Time) error) error {
	return s.editThen(name, guard, func(sp *space, y yesmen.Yesman, f *plansFile, now time.Time) (func(), error) {
		return nil, fn(sp, y, f, now)
	})
}

// editThen is edit with a step to take, still held, once the plans file
// was written: what must not happen unless the change was saved.
// guard, when given, is asked with the workspace held before anything is
// changed: an error leaves everything as it was; what it admitted is held
// until the change was written or refused.
func (s *Service) editThen(name string, guard Admit, fn func(sp *space, y yesmen.Yesman, f *plansFile, now time.Time) (func(), error)) error {
	done, err := s.begin(true)
	if err != nil {
		return err
	}
	defer done()
	y, release, err := s.cfg.Yesmen.BeginUse(name)
	if err != nil {
		return err
	}
	defer release()
	if guard != nil {
		admitted, err := guard(y)
		if err != nil {
			return err
		}
		if admitted != nil {
			defer admitted()
		}
	}
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
	then, err := fn(sp, y, &f, s.now())
	if err != nil {
		return err
	}
	if err := s.writePlansLocked(sp, y, f); err != nil {
		return err
	}
	if then != nil {
		then()
	}
	return nil
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

// Admit admits a change asked for by an agent to the workspace generation
// y, held: it fails if the agent no longer runs there as it did when it
// asked. Until release, the agent cannot finish stopping, so the change
// that follows lands before its stop returns, or not at all. Draft calls it
// once, and holds it until the plan was saved or refused.
type Admit func(y yesmen.Yesman) (release func(), err error)

// Draft saves a plan for the lead, at the user's request; it is always off
// until the user turns it on in Tasks. since is the workspace generation the
// lead belongs to, and admit admits the lead that asked (nil: not checked);
// both are checked with the workspace held, before anything is changed, and
// the admission is held until the plan is saved. So a lead stopped,
// restarted or dismissed while its request was on its way, or a same-name
// workspace hired since, gets nothing saved, and a stop that returned saw
// every draft its lead's process made already saved.
func (s *Service) Draft(name string, since time.Time, admit Admit, in PlanInput) (Plan, error) {
	in.ID, in.Revision, in.Enabled = "", 0, false
	guard := func(y yesmen.Yesman) (func(), error) {
		if since.IsZero() || !y.HiredAt.Equal(since) {
			return nil, ErrStaleCaller
		}
		if admit == nil {
			return nil, nil
		}
		release, err := admit(y)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrStaleCaller, err)
		}
		return release, nil
	}
	return s.save(name, in, "lead", guard)
}

func (s *Service) save(name string, in PlanInput, by string, guard ...Admit) (Plan, error) {
	var saved Plan
	var check Admit
	if len(guard) > 0 {
		check = guard[0]
	}
	err := s.editThen(name, check, func(sp *space, y yesmen.Yesman, f *plansFile, now time.Time) (func(), error) {
		var then func()
		err := func() error {
			in, loc, err := in.normalize(now)
			if err != nil {
				return err
			}
			var p Plan
			if in.ID == "" {
				if activeCount(f.Plans) >= MaxActivePlans {
					return fmt.Errorf("a yesman keeps at most %d tasks: archive one first", MaxActivePlans)
				}
				if len(f.Plans) >= maxPlans {
					board, boardErr := s.cfg.Yesmen.Board(y.Name)
					forgotten, err := s.forgetPlansLocked(sp, f, len(f.Plans)-maxPlans+1, boardTasks(board, boardErr), boardErr)
					if err != nil {
						return err
					}
					then = func() { s.dropRunsLocked(sp, y, forgotten) }
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
			saved = p
			return nil
		}()
		if err != nil {
			return nil, err
		}
		// Only once the file was written: a refused change leaves even the
		// plan's last problem as it was.
		evict := then
		return func() {
			delete(sp.problem, saved.ID)
			if evict != nil {
				evict()
			}
		}, nil
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

// forgetPlansLocked removes n archived plans from the file, the oldest
// archive first, to keep it bounded. A plan with a run still retained (on
// its way, or with its board task open) is kept with its history; when n
// such plans cannot be found, nothing is removed. Their records are
// removed once the file was written (dropRunsLocked).
func (s *Service) forgetPlansLocked(sp *space, f *plansFile, n int, tasks map[string]yesmen.Task, boardErr error) ([]string, error) {
	var out []string
	for _, p := range archivedPlans(f.Plans) {
		if len(out) == n {
			break
		}
		if slices.ContainsFunc(plansRuns(sp.runs, p.ID), func(r *record) bool {
			return sp.active[r.ID] || retained(r.Run, tasks, boardErr)
		}) {
			continue
		}
		out = append(out, p.ID)
	}
	if len(out) < n {
		return nil, fmt.Errorf("a yesman keeps at most %d tasks, with the archived ones: the archived tasks it could clear away still have runs whose board tasks are open", maxPlans)
	}
	f.Plans = slices.DeleteFunc(f.Plans, func(p Plan) bool { return slices.Contains(out, p.ID) })
	for _, id := range out {
		delete(f.planExtras, id)
	}
	return out, nil
}

// dropRunsLocked removes the records of plans no longer in the file, and
// what pruned.json keeps of them. A record that cannot be removed stays;
// its plan's ID is never given again.
func (s *Service) dropRunsLocked(sp *space, y yesmen.Yesman, plans []string) {
	var rd *os.Root
	for _, id := range sortedIDs(sp.runs) {
		r := sp.runs[id]
		if !slices.Contains(plans, r.Plan) || sp.active[id] {
			continue
		}
		if rd == nil {
			var err error
			if rd, err = runsFolder(y); err != nil {
				break
			}
			defer rd.Close() // once: rd is opened once
		}
		if removeIn(rd, id+".json") == nil {
			delete(sp.runs, id)
			delete(sp.checked, id)
		}
	}
	if sp.prunedErr != nil {
		return
	}
	next := sp.pruned.clone()
	for _, id := range plans {
		delete(next.Plans, id)
	}
	if len(next.Plans) != len(sp.pruned.Plans) && s.writePrunedLocked(y, next) == nil {
		sp.pruned = next
	}
}

// SetEnabled turns a plan on or off. Off stops its future runs; a run
// already on its way goes on, and nothing in the lead's queue is touched.
func (s *Service) SetEnabled(name, id string, enabled bool, revision int) (Plan, error) {
	return s.setEnabled(name, nil, id, enabled, revision)
}

func (s *Service) setEnabled(name string, guard Admit, id string, enabled bool, revision int) (Plan, error) {
	var saved Plan
	err := s.edit(name, guard, func(sp *space, y yesmen.Yesman, f *plansFile, now time.Time) error {
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
	return s.archive(name, nil, id, revision)
}

func (s *Service) archive(name string, guard Admit, id string, revision int) error {
	err := s.edit(name, guard, func(sp *space, y yesmen.Yesman, f *plansFile, now time.Time) error {
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
	return s.runNow(name, time.Time{}, id, requestID, from)
}

func (s *Service) runNow(name string, since time.Time, id, requestID, from string) (Run, error) {
	if !requestRE.MatchString(requestID) {
		return Run{}, errors.New("invalid request ID")
	}
	done, err := s.begin(true)
	if err != nil {
		return Run{}, err
	}
	defer done()
	y, release, err := s.cfg.Yesmen.BeginUse(name)
	if err != nil {
		return Run{}, err
	}
	defer release()
	if err := hiredAt(y, since); err != nil {
		return Run{}, err
	}
	board, boardErr := s.cfg.Yesmen.Board(y.Name)
	tasks := boardTasks(board, boardErr)
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
		// The missed record names its catch-up for good: once that run's
		// record has left the history, it is still caught up.
		if missed.CaughtUp != "" && (missed.CaughtUp != r.ID || sp.runs[r.ID] == nil) {
			sp.mu.Unlock()
			return Run{}, errCaughtUp
		}
	}
	if existing := sp.runs[r.ID]; existing != nil {
		run := existing.Run
		sp.mu.Unlock()
		return run, nil // the same click again
	}
	if r.Kind == RunManual {
		// A click whose run was pruned is not a new one; nor is any click
		// while what was pruned cannot be read.
		if sp.prunedErr != nil {
			err := sp.prunedErr
			sp.mu.Unlock()
			return Run{}, err
		}
		if pp := sp.pruned.Plans[p.ID]; pp != nil && pp.Floor > fmt.Sprintf("%012x", now.UnixMilli()) && requestTime(requestID) <= pp.Floor && !slices.Contains(pp.Requests, requestID) {
			sp.mu.Unlock()
			return Run{}, fmt.Errorf("%w (%s)", ErrRunNowWaits, requestClock(pp.Floor).Format("2006-01-02 15:04 MST"))
		}
		if sp.pruned.used(p.ID, requestID) {
			sp.mu.Unlock()
			return Run{}, ErrRequestUsed
		}
	}
	if retainedCount(sp, p.ID, tasks, boardErr) >= maxOpenRuns {
		sp.mu.Unlock()
		return Run{}, errTooManyOpen
	}
	for _, other := range plansRuns(sp.runs, p.ID) {
		if other.fired() && other.inFlight() {
			sp.mu.Unlock()
			return Run{}, errors.New("this task's last run is still being sent: try again once it has been sent")
		}
	}
	// A new click: before it is claimed, its ID must carry a time pruning
	// can cover once its record goes (prunedFile.add). Clicks already taken
	// were answered above, whatever their IDs.
	if r.Kind == RunManual && requestTime(requestID) > latestRequest(now) {
		sp.mu.Unlock()
		return Run{}, ErrRequestAhead
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
	return s.retry(name, time.Time{}, runID)
}

func (s *Service) retry(name string, since time.Time, runID string) (Run, error) {
	done, err := s.begin(true)
	if err != nil {
		return Run{}, err
	}
	defer done()
	y, release, err := s.cfg.Yesmen.BeginUse(name)
	if err != nil {
		return Run{}, err
	}
	defer release()
	if err := hiredAt(y, since); err != nil {
		return Run{}, err
	}
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
	return s.check(name, time.Time{}, runID)
}

func (s *Service) check(name string, since time.Time, runID string) (Run, error) {
	// Counted until its record is written: Close keeps the lock until then,
	// so no other app writes the run meanwhile.
	done, err := s.begin(true)
	if err != nil {
		return Run{}, err
	}
	defer done()
	// The workspace is held while the run is found, as by View, and let go
	// before its receipt is read; what the receipt says is written only to
	// the generation still current then (update).
	y, release, err := s.cfg.Yesmen.BeginUse(name)
	if err != nil {
		return Run{}, err
	}
	if err := hiredAt(y, since); err != nil {
		release()
		return Run{}, err
	}
	sp := s.spaceFor(y)
	sp.mu.Lock()
	if sp.retired.Load() {
		sp.mu.Unlock()
		release()
		return Run{}, errRetired
	}
	s.loadLocked(sp, y)
	rec, active := sp.runs[runID], sp.active[runID]
	var r Run
	if rec != nil {
		// A copy, taken under the lock: updates replace the record whole.
		r = rec.Run
	}
	sp.mu.Unlock()
	release()
	if rec == nil {
		return Run{}, fmt.Errorf("no run %s", runID)
	}
	if r.attempt() == nil || r.inFlight() || active {
		return r, nil
	}
	r, err = s.reconcile(sp, r)
	s.changed(name)
	return r, err
}

// reconcile reads a run's receipt again and records what it says, without
// sending anything: a queue its lead lost with its process makes it
// unknown, and a submission never registered makes an unknown run failed.
func (s *Service) reconcile(sp *space, r Run) (Run, error) {
	a := r.attempt()
	if a == nil {
		return r, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	d, found, err := s.cfg.Agents.LookupDelivery(ctx, agent.ID(sp.name, yesmen.LeadName), a.ID, sp.since)
	cancel()
	if err != nil {
		return r, err
	}
	switch found {
	case agent.DeliveryFound:
		return s.update(sp, r.ID, func(r *Run) bool { return applyReceipt(r, a.ID, &d, "") })
	case agent.DeliveryAbsent:
		if r.State != StateUnknown {
			return r, nil
		}
		// It was never registered: definitely not sent.
		return s.update(sp, r.ID, func(r *Run) bool {
			at := r.attempt()
			if r.State != StateUnknown || at == nil || at.ID != a.ID {
				return false
			}
			at.Definite, at.State = true, "absent"
			r.State, r.Reason = StateFailed, "Not sent: the lead never received it"
			return true
		})
	default:
		if !r.waiting() {
			return r, nil
		}
		// It waited in pi, and its receipt is no longer kept: whether it
		// reached the lead cannot be told.
		return s.update(sp, r.ID, func(r *Run) bool {
			at := r.attempt()
			if !r.waiting() || at == nil || at.ID != a.ID {
				return false
			}
			at.State = "unknown"
			r.State, r.Reason = StateUnknown, "Its delivery record is no longer kept, so yesman cannot confirm it reached the lead; it was not sent again"
			return true
		})
	}
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
