package server

import (
	"context"
	"errors"
	"log"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// WorktreeReclaimer consumes current task results for registered instances.
// Discovery is local. Branch candidates can require bounded GitHub checks.
type WorktreeReclaimer struct {
	s        *Server
	mu       sync.Mutex
	wake     chan struct{}
	done     chan struct{}
	cancel   context.CancelFunc
	closed   bool
	jobs     map[checkoutKey]checkoutJob // empty checkout ID schedules a local discovery
	minRetry time.Duration
}

type checkoutKey struct {
	workspace string
	hire      time.Time
	id        string
}

type checkoutJob struct {
	y                         yesmen.Yesman
	due                       time.Time
	delay                     time.Duration
	owner, delegate, assignee string
	waitForIdle               bool
}

func NewWorktreeReclaimer(s *Server) *WorktreeReclaimer {
	return &WorktreeReclaimer{s: s, wake: make(chan struct{}, 1), done: make(chan struct{}), jobs: map[checkoutKey]checkoutJob{}, minRetry: time.Minute}
}

// Start after restart/resume has admitted any interrupted work. Close also
// works before Start (an app can quit during recovery).
func (r *WorktreeReclaimer) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go r.run(ctx)
}

func (r *WorktreeReclaimer) Close() {
	r.mu.Lock()
	r.closed = true
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		<-r.done
	}
}

func (r *WorktreeReclaimer) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// BoardResult is called for a newly submitted disposition or saved proof.
// It coalesces bursts without moving an existing job's retry time.
func (r *WorktreeReclaimer) BoardResult(y yesmen.Yesman) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	key := checkoutKey{workspace: y.Name, hire: y.HiredAt}
	now := time.Now()
	if existing, queued := r.jobs[key]; !queued || existing.due.After(now) {
		r.jobs[key] = checkoutJob{y: y, due: now}
	}
	r.signal()
}

// OwnerRetired wakes only jobs held by that owner or delegate. Token usage,
// tool output, other workers, board reorders and general UI refreshes are
// independent of cleanup and never trigger a search for merged PRs.
func (r *WorktreeReclaimer) OwnerRetired(workspace, owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	for key, job := range r.jobs {
		if job.waitForIdle && key.workspace == workspace && (job.owner == owner || job.delegate == owner || job.assignee == owner) && job.due.After(time.Now()) {
			job.due = time.Now()
			r.jobs[key] = job
			r.signal()
		}
	}
}

func (r *WorktreeReclaimer) run(ctx context.Context) {
	defer close(r.done)
	workspaces, err := r.s.Yesmen.List()
	if err != nil {
		log.Print("worktree recovery: ", err)
	}
	for _, y := range workspaces {
		r.BoardResult(y)
	}
	for ctx.Err() == nil {
		r.mu.Lock()
		var key checkoutKey
		var next checkoutJob
		for k, j := range r.jobs {
			if key.workspace == "" || j.due.Before(next.due) {
				key, next = k, j
			}
		}
		if key.workspace != "" && !next.due.After(time.Now()) {
			// Repeated remote waits back off per instance. A fresh process
			// discovers each outstanding receipt once, then uses the same budget.
			if next.delay == 0 {
				next.delay = r.minRetry
			} else {
				next.delay = min(2*next.delay, time.Hour)
			}
			next.due = time.Now().Add(next.delay)
			if key.id == "" {
				// A board result arriving during discovery gets its own pass.
				delete(r.jobs, key)
				r.mu.Unlock()
				if err := r.scan(next.y); err != nil {
					log.Printf("worktree discovery for %s: %v", key.workspace, err)
					r.mu.Lock()
					if _, newer := r.jobs[key]; !newer {
						r.jobs[key] = next
					}
					r.mu.Unlock()
				}
				continue
			}
			// Remember an owner drain during the check. An unrelated drain
			// cannot accelerate a known merge, publication or file wait.
			next.waitForIdle = true
			r.jobs[key] = next
			r.mu.Unlock()
			attempt, cancel := context.WithTimeout(ctx, 45*time.Second)
			wait := r.s.reconcileCheckout(attempt, next.y, key.id)
			cancel()
			r.mu.Lock()
			switch wait {
			case collectedOrIneligible:
				delete(r.jobs, key)
			case retryLater:
				next.waitForIdle = false
				next.due = time.Now().Add(next.delay)
				r.jobs[key] = next
			}
			r.mu.Unlock()
			continue
		}
		r.mu.Unlock()
		var timer <-chan time.Time
		if key.workspace != "" {
			timer = time.After(time.Until(next.due))
		}
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-timer:
		}
	}
}

func (r *WorktreeReclaimer) scan(y yesmen.Yesman) error {
	current, err := r.s.Yesmen.Get(y.Name)
	if err != nil || y.HiredAt.IsZero() || !current.HiredAt.Equal(y.HiredAt) {
		return nil
	}
	records, board, workers, err := r.s.Yesmen.WorktreeState(y)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, rec := range records {
		if rec.Removed || !temporaryPurpose(rec.Purpose) || !checkoutID.MatchString(rec.ID) || rec.Root == nil && rec.CleanupHead == "" {
			continue
		}
		c, eligible := candidate(y, records, board, workers, rec.ID)
		key := checkoutKey{y.Name, y.HiredAt, rec.ID}
		if !eligible && rec.CleanupHead == "" && rec.Completion == nil {
			continue
		}
		r.mu.Lock()
		job, queued := r.jobs[key]
		if !queued {
			r.jobs[key] = checkoutJob{y: y, owner: rec.Owner, delegate: rec.CleanupOwner, assignee: c.authority.decision.owner, due: now}
		}
		r.mu.Unlock()
		if queued && job.waitForIdle && job.due.After(now) && !heldByUsers(rec, board, workers) {
			if err := r.s.withTaskWorktreeUsers(y, rec, c.authority.decision, func() error { return nil }); err == nil {
				r.mu.Lock()
				if waiting := r.jobs[key]; waiting.waitForIdle && waiting.due.After(now) {
					waiting.due = now
					r.jobs[key] = waiting
				}
				r.mu.Unlock()
			}
		}
	}
	return nil
}

var checkoutID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type checkoutCandidate struct {
	record    yesmen.WorktreeRecord
	authority checkoutAuthority
}

// Rejected proposals are audit history. A new worker with the same name
// cannot stand in for the original owner, including on cold start.
func candidate(y yesmen.Yesman, records []yesmen.WorktreeRecord, b yesmen.Board, workers []yesmen.Worker, id string) (checkoutCandidate, bool) {
	var none checkoutCandidate
	i := slices.IndexFunc(records, func(r yesmen.WorktreeRecord) bool { return r.ID == id })
	if i < 0 || !checkoutID.MatchString(id) {
		return none, false
	}
	rec := records[i]
	if rec.Removed || !temporaryPurpose(rec.Purpose) || !dirRE.MatchString(rec.Dir) || rec.Root == nil || !strings.HasPrefix(rec.Repo, "github.com/") || latestWorktree(records, rec.Dir) != i {
		return none, false
	}
	a, reason := bulkAuthority(y, rec, b, workers)
	return checkoutCandidate{record: rec, authority: a}, reason == ""
}

type reclaimWait int

const (
	collectedOrIneligible reclaimWait = iota
	retryLater
	waitForOwner
)

func heldByUsers(rec yesmen.WorktreeRecord, b yesmen.Board, workers []yesmen.Worker) bool {
	user := func(name string) bool { return name != "" && (name == rec.Owner || name == rec.CleanupOwner) }
	return slices.ContainsFunc(b.Tasks, func(t yesmen.Task) bool { return t.Open() && user(t.Owner) }) ||
		slices.ContainsFunc(workers, func(w yesmen.Worker) bool { return user(w.Name) && (!w.PausedAt.IsZero() || !w.FailedAt.IsZero()) })
}

// Remote reads run outside all reservations. After taking the commit lease,
// reread the current Board, generations and registry and reserve all users.
func (s *Server) reconcileCheckout(ctx context.Context, y yesmen.Yesman, id string) reclaimWait {
	if current, err := s.Yesmen.Get(y.Name); err != nil || !current.HiredAt.Equal(y.HiredAt) {
		return collectedOrIneligible
	}
	records, b, workers, err := s.Yesmen.WorktreeState(y)
	if err != nil {
		return retryLater
	}
	if w, handled := s.recoverMissingReturn(ctx, y, records, id); handled {
		return w
	}
	first, ok := candidate(y, records, b, workers, id)
	if !ok {
		return collectedOrIneligible
	}
	rec := first.record
	if heldByUsers(rec, b, workers) || s.worktreeUsersPaused(y, rec) {
		return waitForOwner
	}
	// Do not spend remote requests on a running/retiring worker. Release the
	// preflight reservation immediately. The final reservation is authoritative.
	if err := s.withTaskWorktreeUsers(y, rec, first.authority.decision, func() error { return nil }); err != nil {
		return waitForOwner
	}
	w, observation, err := s.observeReclaim(ctx, y, first)
	if err != nil {
		return retryLater
	}
	defer observation.Close()
	if hold := s.verifyCleanupSource(ctx, rec, first.authority, observation.Tracking); hold != nil {
		return retryLater
	}
	lock := s.worktreeLock(y.Name)
	if !lock.TryLock() {
		return retryLater
	}
	var retired bool
	err = s.Yesmen.TryWorktreeCommit(y, func() error {
		records, b, workers, err := s.Yesmen.WorktreeState(y)
		if err != nil {
			return err
		}
		current, ok := candidate(y, records, b, workers, id)
		if !ok || heldByUsers(current.record, b, workers) || !reflect.DeepEqual(first, current) {
			return errors.New("task or proof changed during verification")
		}
		i := latestWorktree(records, rec.Dir)
		return s.withTaskWorktreeUsers(y, current.record, current.authority.decision, func() error {
			if s.worktreeUsersPaused(y, rec) {
				return errors.New("a recorded owner is paused")
			}
			if err := s.Pool.TryReclaimTemporary(ctx, y.Path, w, func() error {
				return s.saveCleanupReceipt(ctx, y, &records[i], current.authority, w.Head)
			}); err != nil {
				return err
			}
			if err := s.recordWorktreeRemoval(y, records[i], "automatically reclaimed from task "+rec.Task); err != nil {
				return err
			}
			retired = true
			return nil
		})
	})
	lock.Unlock()
	if retired {
		s.changed()
	}
	if err != nil {
		return retryLater
	}
	return collectedOrIneligible
}
