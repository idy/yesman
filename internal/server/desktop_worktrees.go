package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/safefs"
	"github.com/idy/yesman/internal/yesmen"
)

// DesktopWorktrees belongs to the desktop service. Its single-use confirmation
// is never registered in the HTTP agent API or accepted by cleanupWorktree.
type DesktopWorktrees struct {
	Server   *Server
	mu       sync.Mutex
	pending  *desktopPreview
	checks   map[string]worktreeCheck
	sequence uint64
}

type WorktreeCleanupRow struct {
	pool.Worktree
	Reason           string `json:"reason,omitempty"`
	Detail           string `json:"detail,omitempty"`
	Removed          bool   `json:"removed,omitempty"`
	KeepWarning      string `json:"keepWarning,omitempty"`
	Task             string `json:"task,omitempty"`
	TaskStatus       string `json:"taskStatus,omitempty"`
	ConflictRecovery bool   `json:"conflictRecovery,omitempty"`
	Interrupted      bool   `json:"interrupted,omitempty"`
	observation      *pool.CheckoutObservation
}

type WorktreeCleanupPreview struct {
	Rows      []WorktreeCleanupRow `json:"rows"`
	Token     string               `json:"token,omitempty"`
	Partial   bool                 `json:"partial,omitempty"`
	Retryable bool                 `json:"retryable,omitempty"`
}

const (
	worktreePreviewBudget = 12 * time.Second
	worktreeLocalBudget   = 8 * time.Second // leave time for the remote checks
	worktreeSourceBudget  = 4 * time.Second
	worktreeSourceWorkers = 4
	worktreeCheckLease    = 30 * time.Second
	maxWorktreeChecks     = 16
)

type desktopPreview struct {
	workspace yesmen.Yesman
	expires   time.Time
	token     string
	bulk      bool
	eligible  []desktopCandidate
	timer     *time.Timer
	request   string // identifies cancellation of this check, never confirmation
	cancel    context.CancelFunc
}

type desktopCandidate struct {
	row         int // index in the non-destructive preview
	worktree    pool.Worktree
	record      yesmen.WorktreeRecord
	decision    WorktreeTaskDecision
	authority   checkoutAuthority
	observation *pool.CheckoutObservation
	orphan      bool
}

const (
	unverifiedMerge    = "The merged pull request could not be verified for this branch and commit."
	staleDecision      = "The task or its worktree decision changed. Open a new preview."
	keepWarning        = "The Board says to keep this worktree. Removing it here overrides that choice."
	boardKeep          = "The Board says to keep this worktree."
	staleReturn        = "The saved return no longer matches the Board. Review this worktree again."
	resolveUndo        = "Git has saved versions of a resolved conflict in this worktree."
	resolveUndoChanged = "Git conflict recovery data changed. Open a new preview."
	indexHold          = "The Git index could not be verified. The worktree will stay here."
	staleCheckout      = "The checkout changed. Refresh Worktrees and review it again."
	unknownOwner       = "Yes Man has no matching temporary checkout record for this directory."
	unknownInstance    = "The private Git marker or its link to this directory is missing or has changed."
	noRegistration     = "Git has no current registration for this recorded directory."
	unsavedFiles       = "The Files pane has unsaved edits in this worktree."
	unverified         = "Yes Man could not verify this checkout."
	previewTimedOut    = "This worktree could not be checked within the time limit."
	inventoryTimedOut  = "The worktree list could not be checked within the time limit. Try again."
	previewRemoteError = "Open pull requests could not be checked. Try again when GitHub is available."
)

func sameCheckout(a, b pool.Worktree) bool {
	return a.Dir == b.Dir && a.Path == b.Path && a.Repo == b.Repo && a.CleanupID == b.CleanupID && a.Head == b.Head && a.Branch == b.Branch && a.Detached == b.Detached
}

// A ticket is issued before the frontend sends a potentially delayed Check.
// Revocation deletes the ticket, so even arbitrarily late calls are inert.
// Only an admitted Check can replace the active plan.
type worktreeCheck struct {
	scope    [sha256.Size]byte
	sequence uint64
	expires  time.Time
}

func worktreeCheckScope(name string, since time.Time, selected *pool.Worktree) ([sha256.Size]byte, error) {
	data, err := json.Marshal(struct {
		Workspace string
		HiredAt   time.Time
		Selected  *pool.Worktree
	}{name, since.UTC(), selected})
	return sha256.Sum256(data), err
}

var errPreviewExpired = errors.New("This preview expired or was already used. Open a new preview.")

func (d *DesktopWorktrees) Prepare(name string, since time.Time, selected *pool.Worktree) (string, error) {
	if err := yesmen.ValidName(name); err != nil {
		return "", err
	}
	scope, err := worktreeCheckScope(name, since, selected)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	maps.DeleteFunc(d.checks, func(_ string, c worktreeCheck) bool { return !now.Before(c.expires) })
	if len(d.checks) >= maxWorktreeChecks {
		return "", yesmen.ErrWorkspaceBusy
	}
	if d.checks == nil {
		d.checks = make(map[string]worktreeCheck)
	}
	d.sequence++
	id := "check-" + strconv.FormatUint(d.sequence, 10) + "-" + rand.Text()
	d.checks[id] = worktreeCheck{scope: scope, sequence: d.sequence, expires: now.Add(worktreeCheckLease)}
	return id, nil
}

// Preview is the in-process entry point. The desktop bridge reserves the
// ticket separately so it can cancel even before the check starts.
func (d *DesktopWorktrees) Preview(ctx context.Context, name string, since time.Time, selected *pool.Worktree, keep []string) (WorktreeCleanupPreview, error) {
	request, err := d.Prepare(name, since, selected)
	if err != nil {
		return WorktreeCleanupPreview{}, err
	}
	return d.PreviewRequest(ctx, name, since, selected, keep, request)
}

// PreviewRequest binds the plan to the selected row and workspace hire. Bulk
// starts with all registrations and eligible retained registry records.
func (d *DesktopWorktrees) PreviewRequest(parent context.Context, name string, since time.Time, selected *pool.Worktree, keep []string, request string) (WorktreeCleanupPreview, error) {
	scope, err := worktreeCheckScope(name, since, selected)
	if err != nil {
		return WorktreeCleanupPreview{}, err
	}
	s := d.Server
	ctx, cancel := context.WithTimeout(parent, worktreePreviewBudget)
	defer cancel()
	local, stopLocal := context.WithTimeout(ctx, worktreeLocalBudget)
	defer stopLocal()
	plan := &desktopPreview{bulk: selected == nil, token: rand.Text(), request: request, cancel: cancel}
	out := WorktreeCleanupPreview{Rows: []WorktreeCleanupRow{}}
	d.mu.Lock()
	admission, ok := d.checks[request]
	if !ok || admission.scope != scope || !time.Now().Before(admission.expires) || parent.Err() != nil {
		d.mu.Unlock()
		return out, errPreviewExpired
	}
	maps.DeleteFunc(d.checks, func(_ string, c worktreeCheck) bool { return c.sequence <= admission.sequence })
	d.pending.close()
	d.pending = plan
	d.mu.Unlock()
	l := s.worktreeLock(name)
	var candidates []desktopCandidate
	var workspace yesmen.Yesman
	inventoryReady := false
	if !l.TryLock() {
		err = yesmen.ErrWorkspaceBusy
	} else {
		err = s.Yesmen.TryUse(name, func(y yesmen.Yesman) error {
			if since.IsZero() || !y.HiredAt.Equal(since) {
				return yesmen.ErrRehired
			}
			workspace = y
			records, board, workers, err := s.Yesmen.WorktreeState(y)
			if err != nil {
				return err
			}
			wts, err := s.Pool.Worktrees(local, y.Path)
			if err != nil {
				return err
			}
			inventoryReady = true
			if plan.bulk {
				local = pool.DesktopDiscardPreview(local, y.Path)
			}
			seen := map[string]int{}
			for _, w := range wts {
				seen[w.Dir]++
			}
			for _, w := range wts {
				if plan.bulk {
					i := latestWorktree(records, w.Dir)
					if i >= 0 && !records[i].Removed && records[i].DiscardRetry != nil {
						continue // review through the durable, identity-bound retry below
					}
				}
				if selected != nil && w.Dir != selected.Dir {
					continue
				}
				row := WorktreeCleanupRow{Worktree: w}
				switch {
				case seen[w.Dir] != 1:
					row.Reason = "Multiple Git registrations point to this directory."
				case selected != nil && !sameCheckout(*selected, w):
					row.Worktree, row.Reason = *selected, staleCheckout
				case slices.Contains(keep, w.Dir):
					row.Reason = unsavedFiles
				case local.Err() != nil:
					row.Reason = previewTimedOut
				default:
					if plan.bulk {
						row = s.desktopBulkWorktree(local, y, records, board, workers, w, false, false)
					} else {
						row = s.desktopWorktree(local, y, records, board, workers, w, false)
					}
					if local.Err() != nil {
						row.observation.Close()
						row.Reason, row.Detail, row.KeepWarning = previewTimedOut, "", ""
					}
				}
				if row.Reason == "" {
					rec := records[latestWorktree(records, w.Dir)]
					c := desktopCandidate{row: len(out.Rows), worktree: row.Worktree, record: rec,
						decision: WorktreeDecisionFor(board, rec), observation: row.observation}
					c.authority.decision = c.decision
					candidates = append(candidates, c)
				}
				if i := latestWorktree(records, w.Dir); i >= 0 && records[i].ID == w.CleanupID && !records[i].Removed {
					row.Task, row.TaskStatus = records[i].Task, WorktreeDecisionFor(board, records[i]).status
				}
				out.Rows = append(out.Rows, row)
			}
			if selected != nil && len(out.Rows) == 0 {
				out.Rows = append(out.Rows, WorktreeCleanupRow{Worktree: *selected, Reason: noRegistration})
			}
			if plan.bulk {
				for i, r := range records {
					if !r.Removed && (seen[r.Dir] == 0 || r.DiscardRetry != nil) && dirRE.MatchString(r.Dir) && i == latestWorktree(records, r.Dir) {
						if local.Err() != nil {
							out.Partial = true
							break // filesystem existence of remaining old records is still unknown
						}
						path := filepath.Join(y.Path, r.Dir)
						if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
							w := expectedWorktree(r)
							w.Path = path
							orphan := true
							if r.DiscardRetry != nil {
								orphan, w.Head = r.DiscardRetry.Orphan, r.DiscardRetry.Head
							}
							row := WorktreeCleanupRow{Worktree: w, Reason: unsavedFiles, Task: r.Task, TaskStatus: WorktreeDecisionFor(board, r).status}
							if !slices.Contains(keep, w.Dir) {
								row = s.desktopBulkWorktree(local, y, records, board, workers, w, orphan, false)
								if local.Err() != nil {
									row.observation.Close()
									row.Reason, row.Detail = previewTimedOut, ""
								}
							}
							if row.Reason == "" {
								decision := WorktreeDecisionFor(board, r)
								candidates = append(candidates, desktopCandidate{row: len(out.Rows), worktree: row.Worktree, record: r, decision: decision, authority: checkoutAuthority{decision: decision}, observation: row.observation, orphan: orphan})
							}
							out.Rows = append(out.Rows, row)
						}
					}
				}
			}
			return nil
		})
		l.Unlock()
	}
	// No workspace, owner or pool lease spans a network read. Keep the row
	// order from discovery and issue a token only for completed checks.
	var eligible []desktopCandidate
	if err == nil && inventoryReady {
		eligible = s.verifyPreviewSources(ctx, &out, candidates)
	}
	if parent.Err() != nil || errors.Is(ctx.Err(), context.Canceled) {
		err = context.Canceled
	} else if !inventoryReady && errors.Is(local.Err(), context.DeadlineExceeded) {
		err = errors.New(inventoryTimedOut)
	}
	out.Partial = out.Partial || slices.ContainsFunc(out.Rows, func(row WorktreeCleanupRow) bool { return row.Reason == previewTimedOut })
	out.Retryable = out.Partial || slices.ContainsFunc(out.Rows, func(row WorktreeCleanupRow) bool {
		return row.Reason == previewRemoteError || row.Reason == unverifiedMerge
	})
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending != plan || err != nil || len(eligible) == 0 {
		for _, c := range candidates {
			c.observation.Close()
		}
		if d.pending != plan {
			return out, errPreviewExpired
		}
		d.pending = nil
		return out, err
	}
	plan.cancel = nil
	plan.workspace = workspace
	plan.eligible = eligible
	plan.expires = time.Now().Add(10 * time.Minute)
	plan.timer = time.AfterFunc(time.Until(plan.expires), func() { d.Cancel(plan.token) })
	out.Token = plan.token
	return out, nil
}

func (s *Server) verifyPreviewSources(ctx context.Context, out *WorktreeCleanupPreview, candidates []desktopCandidate) []desktopCandidate {
	var workers sync.WaitGroup
	jobs := make(chan desktopCandidate)
	for range min(worktreeSourceWorkers, len(candidates)) {
		workers.Go(func() {
			for c := range jobs {
				check, stop := context.WithTimeout(ctx, worktreeSourceBudget)
				var hold *pool.RemovalHold
				if check.Err() == nil {
					hold = s.verifyCleanupSource(check, c.record, c.authority, c.observation.Tracking)
				}
				if check.Err() != nil {
					hold = &pool.RemovalHold{Reason: previewTimedOut}
				}
				stop()
				if hold != nil {
					out.Rows[c.row].Reason, out.Rows[c.row].Detail, out.Rows[c.row].KeepWarning = hold.Reason, hold.Detail, ""
					c.observation.Close()
				}
			}
		})
	}
	for _, c := range candidates {
		jobs <- c
	}
	close(jobs)
	workers.Wait()
	return slices.DeleteFunc(slices.Clone(candidates), func(c desktopCandidate) bool { return out.Rows[c.row].Reason != "" })
}

func (s *Server) verifyCleanupSource(ctx context.Context, rec yesmen.WorktreeRecord, a checkoutAuthority, tracking string) *pool.RemovalHold {
	if rec.Purpose == "review" {
		return nil
	}
	if a.completion != nil {
		rec.Completion = a.completion
		if err := s.verifyMerged(ctx, rec); err != nil {
			return &pool.RemovalHold{Reason: unverifiedMerge, Detail: err.Error()}
		}
	}
	branches := append([]string{tracking, a.decision.returned.SourceBranch}, a.decision.sourceBranches...)
	if err := s.checkOpenPulls(ctx, rec, branches...); err != nil {
		var open *openWorktreePull
		if errors.As(err, &open) {
			return &pool.RemovalHold{Reason: "This branch has an open pull request.", Detail: open.target + " · " + open.branch}
		}
		return &pool.RemovalHold{Reason: previewRemoteError, Detail: err.Error()}
	}
	return nil
}

func (p *desktopPreview) close() {
	if p == nil {
		return
	}
	if p.cancel != nil {
		p.cancel()
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	for _, c := range p.eligible {
		c.observation.Close()
	}
}

func (d *DesktopWorktrees) Cancel(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.checks, id)
	if d.pending != nil && id != "" && (d.pending.token == id || d.pending.request == id) {
		d.pending.close()
		d.pending = nil
	}
}

// Confirm consumes the server-side plan before waiting. Each Git result is
// independent. A later error, including a bookkeeping failure after removal,
// cannot undo or conceal an earlier successful directory removal.
func (d *DesktopWorktrees) Confirm(ctx context.Context, name string, since time.Time, token string, keep []string) ([]WorktreeCleanupRow, error) {
	d.mu.Lock()
	plan := d.pending
	if plan == nil || token != plan.token || name != plan.workspace.Name || !since.Equal(plan.workspace.HiredAt) || time.Now().After(plan.expires) {
		d.mu.Unlock()
		return nil, errors.New("This preview expired or was already used. Open a new preview.")
	}
	d.pending = nil
	d.mu.Unlock()
	defer plan.close()
	s := d.Server
	var results []WorktreeCleanupRow
	changed := false
	for _, c := range plan.eligible {
		w := c.worktree
		row := WorktreeCleanupRow{Worktree: w, Task: c.record.Task, TaskStatus: c.decision.status}
		if slices.Contains(keep, w.Dir) {
			row.Reason = unsavedFiles
			results = append(results, row)
			continue
		}
		if hold := s.verifyCleanupSource(ctx, c.record, c.authority, c.observation.Tracking); hold != nil {
			row.Reason, row.Detail = hold.Reason, hold.Detail
			results = append(results, row)
			continue
		}
		l := s.worktreeLock(name)
		l.Lock()
		y := plan.workspace
		err := s.Yesmen.TryWorktreeCommit(y, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			records, board, workers, err := s.Yesmen.WorktreeState(y)
			if err != nil {
				return err
			}
			i := latestWorktree(records, w.Dir)
			if i < 0 || !reflect.DeepEqual(c.record, records[i]) {
				row.Reason = staleCheckout
				return nil
			}
			if decision := WorktreeDecisionFor(board, records[i]); !reflect.DeepEqual(decision, c.decision) {
				row.TaskStatus = decision.status
				row.Reason = staleDecision
				return nil
			}
			// A retry can lack a gitfile and therefore a display registration.
			// The pool verifies the complete registration list and both pointers
			// independently before it mutates anything.
			if plan.bulk && c.record.DiscardRetry != nil {
				row = s.desktopBulkWorktree(ctx, y, records, board, workers, w, c.orphan, true)
				return nil
			}
			// A second registration may have appeared during confirmation. A
			// fresh complete inventory also rejects a missing or changed row.
			wts, err := s.Pool.Worktrees(ctx, y.Path)
			if err != nil {
				return err
			}
			matches := 0
			for _, actual := range wts {
				if actual.Dir == w.Dir {
					if !sameCheckout(actual, w) {
						row.Reason = staleCheckout
						return nil
					}
					matches++
				}
			}
			if c.orphan && matches != 0 || !c.orphan && matches != 1 {
				row.Reason = staleCheckout
				return nil
			}
			if plan.bulk {
				row = s.desktopBulkWorktree(ctx, y, records, board, workers, w, c.orphan, true)
			} else {
				row = s.desktopWorktree(ctx, y, records, board, workers, w, true)
			}
			return nil
		})
		l.Unlock()
		if err != nil {
			row.Reason, row.Detail = unverified, err.Error()
		}
		changed = changed || row.Removed
		results = append(results, row)
	}
	if changed {
		s.changed()
	}
	return results, nil
}

func (s *Server) desktopWorktree(ctx context.Context, y yesmen.Yesman, records []yesmen.WorktreeRecord, board yesmen.Board, workers []yesmen.Worker, w pool.Worktree, remove bool) WorktreeCleanupRow {
	row := WorktreeCleanupRow{Worktree: w}
	retain := func(reason, detail string) WorktreeCleanupRow { row.Reason, row.Detail = reason, detail; return row }
	i := latestWorktree(records, w.Dir)
	if !dirRE.MatchString(w.Dir) || i < 0 {
		return retain(unknownOwner, "")
	}
	rec := records[i]
	if rec.Removed || rec.Repo != w.Repo || rec.ID == "" || rec.ID != w.CleanupID {
		return retain(unknownInstance, "")
	}
	if rec.Purpose != "review" && rec.Purpose != "author" && rec.Purpose != "release" {
		return retain("This checkout is primary or has no recorded temporary purpose.", rec.Purpose)
	}
	if rec.Branch != w.Branch || (rec.Purpose == "review") != w.Detached || !exactHeadRE.MatchString(w.Head) || w.Detached && rec.Head != w.Head {
		return retain(staleCheckout, "")
	}
	if rec.Root != nil {
		if w.Root != nil && (rec.Root.Ino != w.Root.Ino || rec.Root.Birth != w.Root.Birth) {
			return retain(staleCheckout, "")
		}
		if w.Root == nil {
			w.Root = &safefs.Identity{Ino: rec.Root.Ino, Birth: rec.Root.Birth, HasBirth: true}
		}
	}
	decision := WorktreeDecisionFor(board, rec)
	row.Task, row.TaskStatus = rec.Task, decision.status
	if reason, detail := worktreeTaskHold(y, board, workers, rec); reason != "" {
		return retain(reason, detail)
	}
	entered := false
	err := s.withTaskWorktreeUsers(y, rec, decision, func() error {
		entered = true
		if s.worktreeUsersPaused(y, rec) {
			return &pool.RemovalHold{Reason: "The owner is paused."}
		}
		if !remove {
			return nil
		}
		if err := s.Pool.TryReclaimTemporary(ctx, y.Path, w, func() error {
			return s.saveCleanupReceipt(ctx, y, &rec, checkoutAuthority{}, w.Head)
		}); err != nil {
			return err
		}
		row.Removed = true
		return s.recordWorktreeRemoval(y, rec, "Removed from the Worktrees panel. Recover "+w.Head+" from refs/yesman/worktrees/"+rec.ID)
	})
	if err == nil && !remove {
		row.observation, err = s.Pool.InspectTemporaryWorktree(ctx, y.Path, w)
		if err == nil {
			row.Worktree = row.observation.Bind(w)
		}
	}
	if err != nil {
		var hold *pool.RemovalHold
		switch {
		case row.Removed:
			return retain("Git removed the directory and saved the recovery reference. The checkout record could not be updated.", err.Error())
		case !entered:
			return retain("The recorded owner could not be reserved for cleanup.", err.Error())
		case errors.Is(err, pool.ErrWorktreeResolveUndo):
			return retain(resolveUndo, "")
		case errors.Is(err, pool.ErrWorktreeIndex):
			return retain(indexHold, "")
		case errors.As(err, &hold):
			return retain(hold.Reason, hold.Detail)
		default:
			return retain(unverified, err.Error())
		}
	}
	if !remove && decision.KeepsCheckout() {
		row.KeepWarning = keepWarning
	}
	return row
}

func worktreeTaskHold(y yesmen.Yesman, board yesmen.Board, workers []yesmen.Worker, rec yesmen.WorktreeRecord) (string, string) {
	owners := map[string]time.Time{rec.Owner: rec.OwnerSince}
	if rec.CleanupOwner != "" {
		if rec.CleanupOwner == rec.Owner && !rec.CleanupSince.Equal(rec.OwnerSince) {
			return unknownOwner, rec.Owner
		}
		owners[rec.CleanupOwner] = rec.CleanupSince
	}
	for name, since := range owners {
		if name == "" || since.IsZero() {
			return unknownOwner, name
		}
		if name == yesmen.LeadName && since.Equal(y.HiredAt) {
			continue
		}
		i := slices.IndexFunc(workers, func(w yesmen.Worker) bool { return w.Name == name })
		if i < 0 || !workers[i].StartedAt.Equal(since) {
			return "The recorded owner can no longer be verified.", name
		}
		if !workers[i].PausedAt.IsZero() {
			return "The owner is paused.", name
		}
	}
	matched := false
	for _, task := range board.Tasks {
		if task.ID == rec.Task {
			detail := task.ID
			if rec.CreatedFor != nil {
				if !boundCreatingTask(y, rec, task) {
					return unprovenCreation, detail
				}
			} else if task.Owner != rec.Owner || !task.OwnerSince.IsZero() && !task.OwnerSince.Equal(rec.OwnerSince) {
				return "The task belongs to a different owner.", detail
			}
			if task.Open() {
				if task.Paused() {
					return "The creating task is paused.", detail
				}
				return unfinishedTask, detail
			}
			matched = true
		}
		if _, owns := owners[task.Owner]; owns {
			detail := task.ID
			if task.Paused() {
				return "The owner has a paused task.", detail
			}
			if task.Open() {
				return "The owner has an unfinished task.", detail
			}
		}
	}
	if heldByUsers(rec, board, workers) {
		return "A recorded owner has work that needs attention.", rec.Task
	}
	if !matched {
		return "The checkout has no matching task on the board.", rec.Task
	}
	return "", ""
}
