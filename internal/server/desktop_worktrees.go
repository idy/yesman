package server

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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
	Reason  string `json:"reason,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Removed bool   `json:"removed,omitempty"`
}

type WorktreeCleanupPreview struct {
	Rows  []WorktreeCleanupRow `json:"rows"`
	Token string               `json:"token,omitempty"`
}

type desktopPreview struct {
	name     string
	since    time.Time
	expires  time.Time
	token    string
	bulk     bool
	eligible []desktopCandidate
}

type desktopCandidate struct {
	row      int // index in the non-destructive preview
	worktree pool.Worktree
	record   yesmen.WorktreeRecord
}

const (
	unverifiedMerge = "The merged pull request could not be verified for this branch and commit."
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
	plan := &desktopPreview{name: name, since: since, bulk: selected == nil, token: rand.Text()}
	out := WorktreeCleanupPreview{Rows: []WorktreeCleanupRow{}}
	d.mu.Lock()
	d.pending = plan
	d.mu.Unlock()
	l := s.worktreeLock(name)
	var candidates []desktopCandidate
	l.Lock()
	err := s.Yesmen.WithWorktreeState(name, since, false, func(y yesmen.Yesman, state *yesmen.WorktreeState) error {
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
				row = s.desktopWorktree(ctx, y, state, w, plan.bulk, false)
			}
			if row.Reason == "" {
				candidates = append(candidates, desktopCandidate{len(out.Rows), row.Worktree, state.Records[latestWorktree(state.Records, w.Dir)]})
			}
			out.Rows = append(out.Rows, row)
		}
		if selected != nil && len(out.Rows) == 0 {
			out.Rows = append(out.Rows, WorktreeCleanupRow{Worktree: *selected, Reason: noRegistration})
		}
		if plan.bulk {
			for i, r := range state.Records {
				if !r.Removed && seen[r.Dir] == 0 && dirRE.MatchString(r.Dir) && i == latestWorktree(state.Records, r.Dir) {
					path := filepath.Join(y.Path, r.Dir)
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						out.Rows = append(out.Rows, WorktreeCleanupRow{Worktree: pool.Worktree{Dir: r.Dir, Path: path, Repo: r.Repo}, Reason: noRegistration})
					}
				}
			}
		}
		return ctx.Err()
	})
	l.Unlock()
	// GitHub may be slow. No workspace, agent or pool lock spans a network
	// read. Final admission compares the same certification under the lease.
	if err == nil {
		for _, c := range candidates {
			if e := d.verifySource(ctx, plan, c); e != nil {
				out.Rows[c.row].Reason, out.Rows[c.row].Detail = unverifiedMerge, e.Error()
			} else {
				plan.eligible = append(plan.eligible, c)
			}
		}
		err = ctx.Err()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending != plan {
		return out, errors.New("This preview expired or was already used. Open a new preview.")
	}
	if err != nil || len(plan.eligible) == 0 {
		d.pending = nil
		return out, err
	}
	plan.expires = time.Now().Add(10 * time.Minute)
	out.Token = plan.token
	return out, nil
}

func (d *DesktopWorktrees) verifySource(ctx context.Context, plan *desktopPreview, c desktopCandidate) error {
	if plan.bulk && c.record.Purpose != "review" {
		return d.Server.verifyBranchCompletion(ctx, yesmen.Yesman{Name: plan.name, HiredAt: plan.since}, c.record)
	}
	return nil
}

func (d *DesktopWorktrees) Cancel(token string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending != nil && d.pending.token == token {
		d.pending = nil
	}
}

// Confirm consumes the server-side plan before waiting. Each Git result is
// independent. A later error, including a bookkeeping failure after removal,
// cannot undo or conceal an earlier successful directory removal.
func (d *DesktopWorktrees) Confirm(ctx context.Context, name string, since time.Time, token string, keep []string) ([]WorktreeCleanupRow, error) {
	d.mu.Lock()
	plan := d.pending
	if plan == nil || token != plan.token || name != plan.name || !since.Equal(plan.since) || time.Now().After(plan.expires) {
		d.mu.Unlock()
		return nil, errors.New("This preview expired or was already used. Open a new preview.")
	}
	d.pending = nil
	d.mu.Unlock()
	s := d.Server
	var results []WorktreeCleanupRow
	changed := false
	for _, c := range plan.eligible {
		w := c.worktree
		row := WorktreeCleanupRow{Worktree: w}
		if slices.Contains(keep, w.Dir) {
			row.Reason = unsavedFiles
			results = append(results, row)
			continue
		}
		if err := d.verifySource(ctx, plan, c); err != nil {
			row.Reason, row.Detail = unverifiedMerge, err.Error()
			results = append(results, row)
			continue
		}
		l := s.worktreeLock(name)
		l.Lock()
		err := s.Yesmen.WithWorktreeState(name, since, true, func(y yesmen.Yesman, state *yesmen.WorktreeState) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			i := latestWorktree(state.Records, w.Dir)
			if i < 0 || !reflect.DeepEqual(c.record, state.Records[i]) {
				row.Reason = staleCheckout
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
			row = s.desktopWorktree(ctx, y, state, w, plan.bulk, true)
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

func (s *Server) desktopWorktree(ctx context.Context, y yesmen.Yesman, state *yesmen.WorktreeState, w pool.Worktree, bulk, remove bool) WorktreeCleanupRow {
	row := WorktreeCleanupRow{Worktree: w}
	retain := func(reason, detail string) WorktreeCleanupRow { row.Reason, row.Detail = reason, detail; return row }
	i := latestWorktree(state.Records, w.Dir)
	if !dirRE.MatchString(w.Dir) || i < 0 {
		return retain(unknownOwner, "")
	}
	rec := state.Records[i]
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
	if reason, detail := worktreeTaskHold(y, state, rec); reason != "" {
		return retain(reason, detail)
	}
	if bulk {
		c := rec.Completion
		if c == nil {
			return retain("No completion was saved for bulk cleanup. Review this worktree individually.", "")
		}
		if !c.Unused || c.SavedAt.IsZero() || c.Head != w.Head || strings.TrimSpace(c.Ref) == "" || strings.TrimSpace(c.Evidence) == "" {
			return retain("The saved completion does not cover this checkout and commit.", "")
		}
		if rec.Purpose == "review" {
			if !slices.Contains([]string{"pass", "findings-handed-off", "waiting-release", "audit-complete"}, c.Outcome) {
				return retain("The review has no recorded conclusion.", "")
			}
		} else {
			if c.Outcome != "done" {
				return retain("The branch has no verified completion.", "")
			}
			if rec.Purpose == "release" && !slices.ContainsFunc(state.Board.Tasks, func(t yesmen.Task) bool { return t.ID == rec.Task && t.Status == yesmen.TaskDone }) {
				return retain("The release task has no record of completed publication.", rec.Task)
			}
		}
	}
	entered := false
	err := s.withWorktreeUsers(y.Name, rec, "", func() error {
		entered = true
		if s.Agents.WorktreeUserPaused(y, rec.Owner, rec.OwnerSince) || (rec.CleanupOwner != "" && s.Agents.WorktreeUserPaused(y, rec.CleanupOwner, rec.CleanupSince)) {
			return &pool.RemovalHold{Reason: "The owner is paused."}
		}
		if !remove {
			return nil
		} // preview only samples owner activity
		if err := s.Pool.RemoveTemporaryWorktree(ctx, y.Path, w); err != nil {
			return err
		}
		row.Removed = true
		return state.RecordRemoval(i, w.Head)
	})
	if err == nil && !remove {
		row.Root, err = s.Pool.InspectTemporaryWorktree(ctx, y.Path, w)
	}
	if err != nil {
		var hold *pool.RemovalHold
		switch {
		case row.Removed:
			return retain("Git removed the directory and saved the recovery reference. The checkout record could not be updated.", err.Error())
		case !entered:
			return retain("An owner is active or another cleanup is running.", err.Error())
		case errors.As(err, &hold):
			return retain(hold.Reason, hold.Detail)
		default:
			return retain(unverified, err.Error())
		}
	}
	return row
}

func worktreeTaskHold(y yesmen.Yesman, state *yesmen.WorktreeState, rec yesmen.WorktreeRecord) (string, string) {
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
		i := slices.IndexFunc(state.Workers, func(w yesmen.Worker) bool { return w.Name == name })
		if i < 0 || !state.Workers[i].StartedAt.Equal(since) {
			return "The recorded owner can no longer be verified.", name
		}
		if !state.Workers[i].PausedAt.IsZero() {
			return "The owner is paused.", name
		}
	}
	matched := false
	for _, task := range state.Board.Tasks {
		if task.ID == rec.Task {
			if task.Owner != rec.Owner || !task.OwnerSince.IsZero() && !task.OwnerSince.Equal(rec.OwnerSince) {
				return "The task belongs to a different owner.", task.ID
			}
			matched = true
		}
		if _, owns := owners[task.Owner]; owns {
			if task.Paused() {
				return "The owner has a paused task.", task.ID
			}
			if task.Open() {
				return "The owner has an unfinished task.", task.ID
			}
		}
	}
	if !matched {
		return "The checkout has no matching task on the board.", rec.Task
	}
	return "", ""
}
