package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// WorktreeReclaimer consumes task results. Its only work items are identities
// from the workspace registry with positive completion evidence. Discovery is
// local. Only a returned branch can cause a bounded GitHub verification.
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
	y               yesmen.Yesman
	due             time.Time
	delay           time.Duration
	owner, delegate string
	waitForIdle     bool
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
	r.jobs[checkoutKey{workspace: y.Name, hire: y.HiredAt}] = checkoutJob{y: y}
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
		if job.waitForIdle && key.workspace == workspace && (job.owner == owner || job.delegate == owner) {
			job.due = time.Time{}
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
	records, board, _, err := r.s.Yesmen.WorktreeState(y)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range records {
		if rec.Removed || !temporaryPurpose(rec.Purpose) || !checkoutID.MatchString(rec.ID) || rec.Root == nil {
			continue
		}
		hasProof := rec.Completion != nil || slices.ContainsFunc(board.Tasks, func(t yesmen.Task) bool {
			return t.ID == rec.Task && slices.ContainsFunc(t.Returns, func(v yesmen.WorktreeReturn) bool { return v.Checkout == rec.ID })
		})
		key := checkoutKey{y.Name, y.HiredAt, rec.ID}
		if _, queued := r.jobs[key]; hasProof && !queued {
			r.jobs[key] = checkoutJob{y: y, owner: rec.Owner, delegate: rec.CleanupOwner}
		}
	}
	return nil
}

var checkoutID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type checkoutCandidate struct {
	record yesmen.WorktreeRecord
	round  time.Time
}

// All authority comes from one exact registered instance and either an
// authenticated Board return or a previously certified completion. Rejected
// proposals are audit history. A new worker with the same name cannot stand
// in for the owner, including on cold start when no pi process exists.
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
	generation := func(name string, since time.Time) bool {
		if since.IsZero() {
			return false
		}
		if name == yesmen.LeadName {
			return y.HiredAt.Equal(since)
		}
		return slices.ContainsFunc(workers, func(w yesmen.Worker) bool {
			return w.Name == name && w.StartedAt.Equal(since)
		})
	}
	if !generation(rec.Owner, rec.OwnerSince) || rec.CleanupOwner != "" && !generation(rec.CleanupOwner, rec.CleanupSince) {
		return none, false
	}
	idx := slices.IndexFunc(b.Tasks, func(t yesmen.Task) bool { return t.ID == rec.Task })
	if idx < 0 {
		return none, false
	}
	t := b.Tasks[idx]
	if t.Status != yesmen.TaskDone || t.Paused() || t.Owner != rec.Owner || t.DoneOwner != rec.Owner || t.DoneAt.IsZero() || !t.OwnerSince.IsZero() && !rec.OwnerSince.Equal(t.OwnerSince) {
		return none, false
	}
	at := slices.IndexFunc(t.Returns, func(v yesmen.WorktreeReturn) bool { return v.Checkout == id })
	if t.Returns != nil && at < 0 {
		return none, false // explicit keep, including a returned empty list
	}
	if at >= 0 {
		v := t.Returns[at]
		if !(v.CertifiedBy == rec.Owner && v.CertifiedSince.Equal(rec.OwnerSince) || v.CertifiedBy == yesmen.LeadName && v.CertifiedSince.Equal(y.HiredAt)) {
			return none, false
		}
	}
	if rec.Completion == nil {
		if at < 0 {
			return none, false
		}
		rec.Completion = &t.Returns[at].WorktreeCompletion
	} else if !rec.BoardRound.IsZero() && (at < 0 || !rec.BoardRound.Equal(t.DoneAt) || !sameWorktreeCompletion(*rec.Completion, t.Returns[at].WorktreeCompletion)) {
		return none, false
	} else if at < 0 && rec.Completion.SavedAt.Before(t.DoneAt) {
		// Old evidence followed by a new close is not a return of that round.
		return none, false
	}
	if completionProblem(rec, *rec.Completion) != "" || rec.Purpose == "release" && strings.TrimSpace(rec.Completion.Publication) == "" {
		return none, false
	}
	return checkoutCandidate{rec, t.DoneAt}, true
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
	first, ok := candidate(y, records, b, workers, id)
	if !ok {
		return collectedOrIneligible
	}
	rec := first.record
	if heldByUsers(rec, b, workers) {
		return waitForOwner
	}
	// Do not spend remote requests on a running/retiring worker. Release the
	// preflight reservation immediately. The final reservation is authoritative.
	if err := s.withWorktreeUsers(y.Name, rec, "", func() error { return nil }); err != nil {
		return waitForOwner
	}
	path := filepath.Join(y.Path, rec.Dir)
	_, statErr := os.Lstat(path)
	missing := errors.Is(statErr, os.ErrNotExist)
	if missing && records[latestWorktree(records, rec.Dir)].Completion == nil {
		return collectedOrIneligible // never freeze a proposed return against an absent checkout
	}
	if !missing && rec.Purpose != "review" {
		if err := s.verifyMerged(ctx, rec); err != nil {
			return retryLater
		}
		if err := s.checkOpenPulls(ctx, rec); err != nil {
			return retryLater
		}
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
		if !ok || heldByUsers(current.record, b, workers) || !first.round.Equal(current.round) || !sameWorktreeCompletion(*rec.Completion, *current.record.Completion) {
			return errors.New("task or proof changed during verification")
		}
		i := latestWorktree(records, rec.Dir)
		if i < 0 || records[i].ID != id || current.record.Branch != rec.Branch || current.record.Repo != rec.Repo || current.record.Purpose != rec.Purpose || current.record.Owner != rec.Owner || !current.record.OwnerSince.Equal(rec.OwnerSince) {
			return fmt.Errorf("checkout identity changed")
		}
		return s.withWorktreeUsers(y.Name, current.record, "", func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if missing {
				// A crash after git removal needs only local bookkeeping. Do
				// not fall through to deletion if a path appeared meanwhile.
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) || records[i].Completion == nil {
					return errors.New("missing checkout was replaced")
				}
				if err := s.checkMissingInstance(ctx, records[i], path); err != nil {
					return err
				}
			} else if err := s.Pool.TryReclaimTemporary(ctx, y.Path, expectedWorktree(current.record), func() error {
				if records[i].Completion != nil {
					return nil
				}
				v := *current.record.Completion
				v.SavedAt = time.Now().UTC()
				before := records[i]
				records[i].Completion, records[i].BoardRound = &v, current.round
				return s.Yesmen.CommitWorktreeRecord(y, before, records[i])
			}); err != nil {
				return err
			}
			before := records[i]
			records[i].Removed = true
			records[i].RetiredReason = "automatically reclaimed after verified completion"
			if err := s.Yesmen.CommitWorktreeRecord(y, before, records[i]); err != nil {
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
