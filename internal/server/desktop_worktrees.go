package server

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/safefs"
	"github.com/idy/yesman/internal/yesmen"
)

// DesktopWorktrees belongs to the desktop service. Its single-use confirmation
// is never registered in the HTTP agent API or accepted by cleanupWorktree.
type DesktopWorktrees struct {
	Server  *Server
	mu      sync.Mutex
	pending *desktopPreview
}

type WorktreeCleanupRow struct {
	pool.Worktree
	Reason      string `json:"reason,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Removed     bool   `json:"removed,omitempty"`
	KeepWarning string `json:"keepWarning,omitempty"`
	Task        string `json:"task,omitempty"`
	TaskStatus  string `json:"taskStatus,omitempty"`
	observation *pool.CheckoutObservation
}

type WorktreeCleanupPreview struct {
	Rows  []WorktreeCleanupRow `json:"rows"`
	Token string               `json:"token,omitempty"`
}

type desktopPreview struct {
	workspace yesmen.Yesman
	expires   time.Time
	token     string
	bulk      bool
	eligible  []desktopCandidate
	timer     *time.Timer
}

type desktopCandidate struct {
	row         int // index in the non-destructive preview
	worktree    pool.Worktree
	record      yesmen.WorktreeRecord
	decision    WorktreeTaskDecision
	authority   checkoutAuthority
	observation *pool.CheckoutObservation
}

const (
	unverifiedMerge = "The merged pull request could not be verified for this branch and commit."
	staleDecision   = "The task or its worktree decision changed. Open a new preview."
	keepWarning     = "The Board says to keep this worktree. Removing it here overrides that choice."
	boardKeep       = "The Board says to keep this worktree."
	staleReturn     = "The saved return no longer matches the Board. Review this worktree again."
	resolveUndo     = "Git has saved versions of a resolved conflict in this worktree."
	indexHold       = "The Git index could not be verified. The worktree will stay here."
	staleCheckout   = "The checkout changed. Refresh Worktrees and review it again."
	unknownOwner    = "Yes Man has no matching temporary checkout record for this directory."
	unknownInstance = "The private Git marker or its link to this directory is missing or has changed."
	noRegistration  = "Git has no current registration for this recorded directory."
	unsavedFiles    = "The Files pane has unsaved edits in this worktree."
	unverified      = "Yes Man could not verify this checkout."
)

func sameCheckout(a, b pool.Worktree) bool {
	return a.Dir == b.Dir && a.Path == b.Path && a.Repo == b.Repo && a.CleanupID == b.CleanupID && a.Head == b.Head && a.Branch == b.Branch && a.Detached == b.Detached
}

// Preview binds a single request to the row the user actually opened. Bulk
// starts with all registrations under this hire and includes retained records
// whose former Git registration vanished. No filesystem sweep supplies targets.
func (d *DesktopWorktrees) Preview(ctx context.Context, name string, since time.Time, selected *pool.Worktree, keep []string) (WorktreeCleanupPreview, error) {
	s := d.Server
	plan := &desktopPreview{bulk: selected == nil, token: rand.Text()}
	out := WorktreeCleanupPreview{Rows: []WorktreeCleanupRow{}}
	d.mu.Lock()
	d.pending.close()
	d.pending = plan
	d.mu.Unlock()
	l := s.worktreeLock(name)
	var candidates []desktopCandidate
	l.Lock()
	err := s.Yesmen.Use(name, func(y yesmen.Yesman) error {
		if since.IsZero() || !y.HiredAt.Equal(since) {
			return yesmen.ErrRehired
		}
		plan.workspace = y
		records, board, workers, err := s.Yesmen.WorktreeState(y)
		if err != nil {
			return err
		}
		wts, err := s.Pool.Worktrees(ctx, y.Path)
		if err != nil {
			return err
		}
		seen := map[string]int{}
		for _, w := range wts {
			seen[w.Dir]++
		}
		for _, w := range wts {
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
			default:
				row = s.desktopWorktree(ctx, y, records, board, workers, w, plan.bulk, false)
			}
			if row.Reason == "" {
				rec := records[latestWorktree(records, w.Dir)]
				c := desktopCandidate{row: len(out.Rows), worktree: row.Worktree, record: rec,
					decision: WorktreeDecisionFor(board, rec), observation: row.observation}
				if plan.bulk {
					c.authority, _ = bulkAuthority(y, rec, board, workers)
				}
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
				if !r.Removed && seen[r.Dir] == 0 && dirRE.MatchString(r.Dir) && i == latestWorktree(records, r.Dir) {
					path := filepath.Join(y.Path, r.Dir)
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						out.Rows = append(out.Rows, WorktreeCleanupRow{Worktree: pool.Worktree{Dir: r.Dir, Path: path, Repo: r.Repo}, Reason: noRegistration, Task: r.Task, TaskStatus: WorktreeDecisionFor(board, r).status})
					}
				}
			}
		}
		return ctx.Err()
	})
	l.Unlock()
	// GitHub may be slow. No workspace, agent or pool lock spans a network
	// read. Final admission compares the same certification under the lease.
	var eligible []desktopCandidate
	if err == nil {
		for _, c := range candidates {
			if hold := s.verifyCleanupSource(ctx, c.record, c.authority, c.observation.Tracking, c.decision.returned.SourceBranch); hold != nil {
				out.Rows[c.row].Reason, out.Rows[c.row].Detail, out.Rows[c.row].KeepWarning = hold.Reason, hold.Detail, ""
				c.observation.Close()
			} else {
				eligible = append(eligible, c)
			}
		}
		err = ctx.Err()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending != plan || err != nil || len(eligible) == 0 {
		for _, c := range candidates {
			c.observation.Close()
		}
		if d.pending != plan {
			return out, errors.New("This preview expired or was already used. Open a new preview.")
		}
		d.pending = nil
		return out, err
	}
	plan.eligible = eligible
	plan.expires = time.Now().Add(10 * time.Minute)
	plan.timer = time.AfterFunc(time.Until(plan.expires), func() { d.Cancel(plan.token) })
	out.Token = plan.token
	return out, nil
}

func (s *Server) verifyCleanupSource(ctx context.Context, rec yesmen.WorktreeRecord, a checkoutAuthority, also ...string) *pool.RemovalHold {
	if rec.Purpose == "review" {
		return nil
	}
	if a.completion != nil {
		rec.Completion = a.completion
		if err := s.verifyMerged(ctx, rec); err != nil {
			return &pool.RemovalHold{Reason: unverifiedMerge, Detail: err.Error()}
		}
	}
	if err := s.checkOpenPulls(ctx, rec, also...); err != nil {
		var open *openWorktreePull
		if errors.As(err, &open) {
			return &pool.RemovalHold{Reason: "This branch has an open pull request.", Detail: open.target + " · " + open.branch}
		}
		return &pool.RemovalHold{Reason: "Open pull requests could not be checked. Try again when GitHub is available.", Detail: err.Error()}
	}
	return nil
}

func (p *desktopPreview) close() {
	if p == nil {
		return
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	for _, c := range p.eligible {
		c.observation.Close()
	}
}

func (d *DesktopWorktrees) Cancel(token string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending != nil && d.pending.token == token {
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
		if hold := s.verifyCleanupSource(ctx, c.record, c.authority, c.observation.Tracking, c.decision.returned.SourceBranch); hold != nil {
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
			if decision := WorktreeDecisionFor(board, records[i]); decision != c.decision {
				row.TaskStatus = decision.status
				row.Reason, row.Detail = staleDecision, worktreeTaskDetail(decision, c.record.Task)
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
			if matches != 1 {
				row.Reason = staleCheckout
				return nil
			}
			row = s.desktopWorktree(ctx, y, records, board, workers, w, plan.bulk, true)
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

func (s *Server) desktopWorktree(ctx context.Context, y yesmen.Yesman, records []yesmen.WorktreeRecord, board yesmen.Board, workers []yesmen.Worker, w pool.Worktree, bulk, remove bool) WorktreeCleanupRow {
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
	taskDetail := worktreeTaskDetail(decision, rec.Task)
	var authority checkoutAuthority
	if bulk {
		var reason string
		authority, reason = bulkAuthority(y, rec, board, workers)
		if reason != "" {
			return retain(reason, taskDetail)
		}
		if c := authority.completion; c != nil && (c.SavedAt.IsZero() || c.Head != w.Head) {
			return retain("The saved completion does not cover this checkout and commit.", taskDetail)
		}
	}
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
			return s.saveCleanupReceipt(ctx, y, &rec, authority, w.Head)
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
	if !bulk && !remove && decision.KeepsCheckout() {
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
			detail := worktreeTaskDetail(WorktreeDecisionFor(board, rec), task.ID)
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
			detail := task.ID + " · " + task.Status
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
