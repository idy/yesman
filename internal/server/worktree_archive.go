package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

// WorktreeMove is available only on the human desktop bridge. There is no
// agent HTTP route. The window returns Confirm only after showing the paths,
// actual branch and full HEAD and asking the user to confirm external non-use.
type WorktreeMove struct {
	Workspace string    `json:"workspace"`
	Since     time.Time `json:"since"`
	Dir       string    `json:"dir"`
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	Confirm   string    `json:"confirm,omitempty"`
}

type MovePreview struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Physical string `json:"physical"`
	Branch   string `json:"branch"`
	Head     string `json:"head"`
	Stamp    string `json:"stamp"`
	Warning  string `json:"warning,omitempty"` // the physical move finished but its final record needs attention
}

// The desktop has one confirmation dialog. A new preview supersedes the old
// one. Consume the token before rechecking eligibility, even on a refusal.
type moveConfirmation struct {
	token       string
	fingerprint [sha256.Size]byte
	expires     time.Time
}

func (s *Server) offerMove(fingerprint [sha256.Size]byte) string {
	s.worktreeMu.Lock()
	defer s.worktreeMu.Unlock()
	s.movePreview = moveConfirmation{rand.Text(), fingerprint, time.Now().Add(10 * time.Minute)}
	return s.movePreview.token
}

func (s *Server) consumeMove(token string) (moveConfirmation, error) {
	s.worktreeMu.Lock()
	defer s.worktreeMu.Unlock()
	v := s.movePreview
	if token != v.token || time.Now().After(v.expires) {
		return moveConfirmation{}, errors.New("confirmation expired or was replaced; open a fresh preview")
	}
	s.movePreview = moveConfirmation{}
	return v, nil
}

const archiveReserved = "This checkout is reserved for archive or restore."

func (s *Server) WorktreeMove(ctx context.Context, req WorktreeMove) (MovePreview, error) {
	var out MovePreview
	if !dirRE.MatchString(req.Dir) || req.ID == "" || req.Since.IsZero() || req.Action != "archive" && req.Action != "restore" && req.Action != "cancel" {
		return out, errors.New("select one registered worktree to archive or restore")
	}
	var approval moveConfirmation
	if req.Confirm != "" {
		var err error
		approval, err = s.consumeMove(req.Confirm)
		if err != nil {
			return out, err
		}
	}
	l := s.worktreeLock(req.Workspace)
	l.Lock()
	defer l.Unlock()
	err := s.Yesmen.UseWorktreeMove(req.Workspace, func(y yesmen.Yesman) error {
		if !y.HiredAt.Equal(req.Since) {
			return errors.New("workspace was replaced")
		}
		return s.Yesmen.WorktreeMoveFile(y, func(records []yesmen.WorktreeRecord, publish func(int, yesmen.WorktreeArchive) error) error {
			i := latestWorktree(records, req.Dir)
			if i < 0 {
				return errors.New("no original lifecycle record for this directory")
			}
			r := records[i]
			if r.ID != req.ID || r.Removed || r.Root == nil || r.Purpose != "author" || r.Branch == "" || !exactHeadRE.MatchString(r.Head) || yesmen.ValidName(r.Owner) != nil || r.OwnerSince.IsZero() || r.Task == "" {
				return errors.New("only the original registered author checkout is eligible")
			}
			for n, other := range records {
				if n != i && (other.ID == r.ID || !other.Removed && strings.EqualFold(other.Dir, r.Dir)) {
					return errors.New("ambiguous original checkout record")
				}
			}
			return s.withArchiveUsers(y, r, func(task yesmen.Task, users []yesmen.WorktreeMoveUser) error {
				active, stored, err := s.Yesmen.WorktreePaths(y, r.Dir, r.ID)
				if err != nil {
					return err
				}
				move := pool.MoveRequest{Repo: r.Repo, ID: r.ID, Active: active, Stored: stored, Ino: r.Root.Ino, Birth: r.Root.Birth, OriginalBranch: r.Branch, Restore: req.Action == "restore", Cancel: req.Action == "cancel"}
				if err := selectMove(r.Archive, &move); err != nil {
					return err
				}
				return s.Pool.WithLinkedMove(ctx, move, func(actual *pool.LinkedMove) error {
					if (move.Restore || move.Pending) && r.Archive.LinkedLocation != actual.LinkedLocation {
						return errors.New("archived checkout no longer matches the saved move")
					}
					out = MovePreview{From: active, To: stored, Physical: actual.Physical, Branch: actual.Branch, Head: actual.Head}
					if move.Restore {
						out.From, out.To = stored, active
					}
					if move.Cancel {
						out.To = out.Physical
					}
					// Include the previous transaction in the digest: returning to
					// identical files after restore never revives an old confirmation.
					data, err := json.Marshal([]any{y.Name, y.HiredAt, req.Action, r, task, users, actual.LinkedLocation, actual.Physical, actual.Registered})
					if err != nil {
						return err
					}
					fingerprint := sha256.Sum256(data)
					if req.Confirm == "" {
						out.Stamp = s.offerMove(fingerprint)
						return nil
					}
					if approval.fingerprint != fingerprint {
						return errors.New("worktree or task changed since the preview; open it again")
					}
					var journal yesmen.WorktreeArchive
					if move.Pending {
						journal = *r.Archive
					} else {
						journal = yesmen.WorktreeArchive{LinkedLocation: actual.LinkedLocation, Phase: "archiving", ConfirmedAt: time.Now().UTC(), Transaction: rand.Text(), TaskOwner: task.Owner}
						if move.Restore {
							journal.Phase = "restoring"
						}
					}
					journal.Users = users
					// A pending record may be visible after its previous directory
					// sync failed. Durably republish the authenticated intent on
					// every retry before touching the worktree or its registration.
					if err := publish(i, journal); err != nil {
						return fmt.Errorf("could not sync the pending move at %s: %w", actual.Physical, err)
					}
					if move.Cancel {
						if err := actual.Cancel(ctx); err != nil {
							return err
						}
						journal.CanceledAt = time.Now().UTC()
					} else if err := actual.Finish(ctx, "yesman move "+journal.Transaction); err != nil {
						return fmt.Errorf("move pending in Worktrees: %w", err)
					}
					out.Physical = actual.Physical
					journal.Phase = "restored"
					for _, receipt := range task.Returns {
						if receipt.Checkout == r.ID {
							journal.PreviousReturnID = receipt.ReturnID
						}
					}
					if out.Physical == stored {
						journal.Phase = "archived"
					}
					if err := publish(i, journal); err != nil {
						// The durable intent or the final state will be readable on restart.
						retry := "Finish"
						if move.Cancel {
							retry = "Cancel pending move"
						}
						out.Warning = fmt.Sprintf("The worktree is at %s and Git points there. Could not verify the final move record (%v). Refresh Worktrees. If the move is pending, choose %s.", out.Physical, err, retry)
					}
					return nil
				})
			})
		})
	})
	return out, err
}

func selectMove(j *yesmen.WorktreeArchive, m *pool.MoveRequest) error {
	phase := "restored"
	if j != nil {
		phase = j.Phase
		if j.Transaction == "" || j.ConfirmedAt.IsZero() {
			return errors.New("unrecognized move record")
		}
	}
	if m.Cancel {
		if phase != "archiving" && phase != "restoring" {
			return errors.New("there is no pending move to cancel")
		}
		m.Restore = phase == "restoring"
	}
	switch {
	case !m.Restore && phase == "restored", m.Restore && phase == "archived":
		return nil
	case !m.Restore && phase == "archiving", m.Restore && phase == "restoring":
		m.Pending, m.LockReason = true, "yesman move "+j.Transaction
		return nil
	default:
		return errors.New("move state changed; refresh Worktrees")
	}
}

// A terminal board task can legitimately have changed hands. Verify both
// worker generations and reserve both, including any recorded delegate.
// Other workers/tasks using this tree are covered by the user's confirmation.
func (s *Server) withArchiveUsers(y yesmen.Yesman, r yesmen.WorktreeRecord, fn func(yesmen.Task, []yesmen.WorktreeMoveUser) error) error {
	b, workers, err := s.Yesmen.WorktreeMoveActors(y)
	if err != nil {
		return err
	}
	var task yesmen.Task
	for _, t := range b.Tasks {
		if t.ID == r.Task {
			task = t
			break
		}
	}
	if task.ID == "" || task.Open() || yesmen.ValidName(task.Owner) != nil {
		return errors.New("the associated board task must have a known owner and be finished")
	}
	var users []yesmen.WorktreeMoveUser
	var names []string
	taskSince := task.OwnerSince
	if taskSince.IsZero() && task.Owner == r.Owner {
		// A task handed over before its worker started can lack OwnerSince.
		// The original recorded owner and the live generation must still match.
		taskSince = r.OwnerSince
	}
	current := []yesmen.WorktreeMoveUser{{Name: r.Owner, Since: r.OwnerSince}, {Name: task.Owner, Since: taskSince}, {Name: r.CleanupOwner, Since: r.CleanupSince}}
	if r.ReservedForArchive() {
		users = slices.Clone(r.Archive.Users)
		// Early moves saved the verified task owner's name beside the
		// original lifecycle generations. Removal binds the task's generation.
		if len(users) == 0 {
			users = append(users, current[0]) // original owner
			if r.CleanupOwner != "" {
				users = append(users, current[2])
			}
			if task.Owner == r.Archive.TaskOwner {
				users = append(users, current[1])
			}
		}
		if r.Archive.TaskOwner != "" {
			names = append(names, r.Archive.TaskOwner)
		}
	}
	for _, u := range current {
		if u.Name == "" {
			continue
		}
		if slices.Contains(users, u) {
			continue
		}
		known := u.Name == yesmen.LeadName && y.HiredAt.Equal(u.Since) || slices.ContainsFunc(workers, func(w yesmen.Worker) bool { return w.Name == u.Name && w.StartedAt.Equal(u.Since) })
		if !known || u.Since.IsZero() {
			return fmt.Errorf("cannot verify recorded owner %s", u.Name)
		}
		users = append(users, u)
	}
	for _, u := range users {
		if yesmen.ValidName(u.Name) != nil || u.Since.IsZero() {
			return errors.New("invalid archive owner generation")
		}
		if !slices.Contains(names, u.Name) {
			names = append(names, u.Name)
		}
	}
	for _, t := range b.Tasks {
		if t.Open() && slices.Contains(names, t.Owner) {
			return fmt.Errorf("%s still owns open task %s", t.Owner, t.ID)
		}
	}
	run := func() error { return fn(task, users) }
	for _, user := range names {
		inner := run
		run = func() error { return s.Agents.WithWorktreeOwner(agent.ID(y.Name, user), "", inner) }
	}
	return run()
}
