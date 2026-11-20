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

func (s *Server) WorktreeMove(ctx context.Context, req WorktreeMove) (MovePreview, error) {
	var out MovePreview
	if !dirRE.MatchString(req.Dir) || req.ID == "" || req.Since.IsZero() || req.Action != "archive" && req.Action != "restore" {
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
			return s.withArchiveUsers(y, r, func(task yesmen.Task) error {
				active, stored, err := s.Yesmen.WorktreePaths(y, r.Dir, r.ID)
				if err != nil {
					return err
				}
				move := pool.MoveRequest{Repo: r.Repo, ID: r.ID, Active: active, Stored: stored, Ino: r.Root.Ino, Birth: r.Root.Birth, OriginalBranch: r.Branch, Restore: req.Action == "restore"}
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
					// Include the previous transaction in the digest: returning to
					// identical files after restore never revives an old confirmation.
					data, err := json.Marshal([]any{y.Name, y.HiredAt, req.Action, r, task, actual.LinkedLocation, actual.Physical, actual.Registered})
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
						if err := publish(i, journal); err != nil {
							return err
						}
					}
					if err := actual.Finish(ctx, "yesman move "+journal.Transaction); err != nil {
						return fmt.Errorf("move pending in Worktrees: %w", err)
					}
					journal.Phase = "archived"
					if move.Restore {
						journal.Phase = "restored"
					}
					return publish(i, journal)
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
func (s *Server) withArchiveUsers(y yesmen.Yesman, r yesmen.WorktreeRecord, fn func(yesmen.Task) error) error {
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
	users := []string{}
	taskSince := task.OwnerSince
	if taskSince.IsZero() && task.Owner == r.Owner {
		// A task handed over before its worker started can lack OwnerSince.
		// The original recorded owner and the live generation must still match.
		taskSince = r.OwnerSince
	}
	for _, u := range []struct {
		name  string
		since time.Time
	}{{r.Owner, r.OwnerSince}, {task.Owner, taskSince}, {r.CleanupOwner, r.CleanupSince}} {
		if u.name == "" {
			continue
		}
		known := u.name == yesmen.LeadName && y.HiredAt.Equal(u.since) || slices.ContainsFunc(workers, func(w yesmen.Worker) bool { return w.Name == u.name && w.StartedAt.Equal(u.since) })
		if !known || u.since.IsZero() {
			return fmt.Errorf("cannot verify recorded owner %s", u.name)
		}
		if !slices.Contains(users, u.name) {
			users = append(users, u.name)
		}
	}
	for _, t := range b.Tasks {
		if t.Open() && slices.Contains(users, t.Owner) {
			return fmt.Errorf("%s still owns open task %s", t.Owner, t.ID)
		}
	}
	run := func() error { return fn(task) }
	for _, user := range users {
		inner := run
		run = func() error { return s.Agents.WithWorktreeOwner(agent.ID(y.Name, user), "", inner) }
	}
	return run()
}
