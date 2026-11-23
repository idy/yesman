package server

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

// Only the explicit desktop batch may discard local data. The Board supplies
// provenance and retention decisions. The Manager and pool leases check use.
func desktopBulkHold(y yesmen.Yesman, board yesmen.Board, workers []yesmen.Worker, rec yesmen.WorktreeRecord) (string, string) {
	d := WorktreeDecisionFor(board, rec)
	if rec.Owner == "" {
		return unknownOwner, ""
	}
	if d.KeepsCheckout() {
		return boardKeep, ""
	}
	if d.paused {
		return "The creating task is paused.", rec.Task
	}
	associated := false
	for _, task := range board.Tasks {
		if task.ID == rec.Task {
			if rec.CreatedFor != nil && !boundCreatingTask(y, rec, task) || rec.CreatedFor == nil && task.Owner != rec.Owner {
				return unprovenCreation, task.ID
			}
			associated = true
		}
		if rec.Task == "" && rec.CreatedFor == nil && task.Owner == rec.Owner && (task.OwnerSince.IsZero() || task.OwnerSince.Equal(rec.OwnerSince)) && !task.Open() {
			associated = true
		}
	}
	for _, worker := range workers {
		if worker.Name == rec.Owner || worker.Name == rec.CleanupOwner || worker.Name == d.owner {
			if !worker.PausedAt.IsZero() {
				return "The owner is paused.", worker.Name
			}
			if rec.Task == "" && rec.CreatedFor == nil && worker.Name == rec.Owner && !rec.OwnerSince.IsZero() && rec.OwnerSince.Equal(worker.StartedAt) && !worker.FinishedAt.IsZero() {
				associated = true
			}
		}
	}
	if !associated {
		return missingTask, rec.Task
	}
	return "", ""
}

// inspectBulkRows runs local read-only checks concurrently and joins them
// before source verification. A row always writes to its inventory slot.
func (s *Server) inspectBulkRows(ctx context.Context, y yesmen.Yesman, records []yesmen.WorktreeRecord, board yesmen.Board, workers []yesmen.Worker, rows []WorktreeCleanupRow, indexes []int) {
	const localWorkers = 4
	type job struct {
		row    int
		owners []chan struct{}
	}
	// The Manager's exclusive reservation still protects against startup or
	// input. Serialize rows that share an owner within this preview so it
	// doesn't report contention caused by the preview itself.
	owners := map[string]chan struct{}{}
	queue := make([]job, 0, len(indexes))
	for _, i := range indexes {
		j := job{row: i}
		if n := latestWorktree(records, rows[i].Dir); n >= 0 {
			rec := records[n]
			names := []string{rec.Owner, rec.CleanupOwner, WorktreeDecisionFor(board, rec).owner}
			slices.Sort(names)
			for _, name := range slices.Compact(names) {
				if name == "" {
					continue
				}
				if owners[name] == nil {
					owners[name] = make(chan struct{}, 1)
				}
				j.owners = append(j.owners, owners[name])
			}
		}
		queue = append(queue, j)
	}
	inspect := func(j job) {
		row := &rows[j.row]
		for _, owner := range j.owners {
			select {
			case owner <- struct{}{}:
				defer func() { <-owner }()
			case <-ctx.Done():
				row.Reason = previewTimedOut
				return
			}
		}
		if ctx.Err() == nil {
			*row = s.desktopBulkWorktree(ctx, y, records, board, workers, row.Worktree, false, false)
		}
		if ctx.Err() != nil {
			row.observation.Close()
			row.Reason, row.Detail = previewTimedOut, ""
		}
	}
	var group sync.WaitGroup
	jobs := make(chan job)
	for range min(localWorkers, len(queue)) {
		group.Go(func() {
			for j := range jobs {
				inspect(j)
			}
		})
	}
	for _, j := range queue {
		jobs <- j
	}
	close(jobs)
	group.Wait()
}

func (s *Server) desktopBulkWorktree(ctx context.Context, y yesmen.Yesman, records []yesmen.WorktreeRecord, board yesmen.Board, workers []yesmen.Worker, w pool.Worktree, orphan, remove bool) WorktreeCleanupRow {
	row := WorktreeCleanupRow{Worktree: w}
	retain := func(reason, detail string) WorktreeCleanupRow { row.Reason, row.Detail = reason, detail; return row }
	i := latestWorktree(records, w.Dir)
	if !dirRE.MatchString(w.Dir) || i < 0 {
		return retain(unknownOwner, "")
	}
	rec := records[i]
	decision := WorktreeDecisionFor(board, rec)
	row.Task, row.TaskStatus = rec.Task, decision.status
	if rec.Removed || rec.Repo != w.Repo || !checkoutID.MatchString(rec.ID) || w.CleanupID != rec.ID {
		return retain(unknownInstance, "")
	}
	if !temporaryPurpose(rec.Purpose) {
		return retain("This checkout is primary or has no recorded temporary purpose.", rec.Purpose)
	}
	if rec.Branch != w.Branch || (rec.Purpose == "review") != w.Detached || !exactHeadRE.MatchString(w.Head) || w.Detached && rec.Head != w.Head {
		return retain(staleCheckout, "")
	}
	if orphan && rec.Root == nil {
		return retain(noRegistration, "")
	}
	if rec.Root != nil {
		if w.Root != nil && (w.Root.Ino != rec.Root.Ino || w.Root.Birth != rec.Root.Birth) {
			return retain(staleCheckout, "")
		}
		if w.Root == nil {
			w.Root = recordedWorktreeRoot(rec)
		}
	}
	if reason, detail := desktopBulkHold(y, board, workers, rec); reason != "" {
		return retain(reason, detail)
	}
	entered := false
	err := s.withTaskWorktreeUsers(y, rec, decision, func() error {
		entered = true
		names := []string{rec.Owner, rec.CleanupOwner, decision.owner}
		if slices.Contains(names, yesmen.LeadName) && s.Agents.WorktreeUserPaused(y, yesmen.LeadName, y.HiredAt) {
			return &pool.RemovalHold{Reason: "The owner is paused."}
		}
		for _, worker := range workers {
			if slices.Contains(names, worker.Name) && s.Agents.WorktreeUserPaused(y, worker.Name, worker.StartedAt) {
				return &pool.RemovalHold{Reason: "The owner is paused.", Detail: worker.Name}
			}
		}
		var saveRetry func(yesmen.WorktreeDiscardRetry) error
		if remove {
			saveRetry = func(retry yesmen.WorktreeDiscardRetry) error {
				if err := s.Yesmen.SaveWorktreeDiscardRetry(y, rec, retry); err != nil {
					return err
				}
				rec.DiscardRetry = &retry
				return nil
			}
		}
		observation, removed, err := s.Pool.DesktopDiscard(ctx, y.Path, w, orphan, rec.DiscardRetry, saveRetry)
		row.Removed = removed
		if err != nil {
			return err
		}
		if !remove {
			row.observation = observation
			row.Worktree = observation.Bind(w)
			row.ConflictRecovery = observation.DiscardHasResolveUndo()
			row.Interrupted = rec.DiscardRetry != nil
			return nil
		}
		after := rec
		after.Removed = true
		after.DiscardRetry = nil
		after.RetiredReason = "Permanently removed from the Worktrees panel without a new recovery reference."
		return s.Yesmen.CommitWorktreeRecord(y, rec, after)
	})
	if err != nil {
		var hold *pool.RemovalHold
		switch {
		case row.Removed:
			return retain("The directory was removed. Its Git registration or checkout record may still need attention.", err.Error())
		case !entered:
			return retain("The recorded owner could not be reserved for cleanup.", err.Error())
		case errors.Is(err, pool.ErrWorktreeResolveUndo):
			return retain(resolveUndoChanged, "")
		case errors.Is(err, pool.ErrWorktreeIndex):
			return retain(indexHold, "")
		case errors.As(err, &hold):
			return retain(hold.Reason, hold.Detail)
		default:
			return retain(unverified, err.Error())
		}
	}
	return row
}
