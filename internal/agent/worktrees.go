package agent

import (
	"errors"
	"fmt"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

var errWorktreeCleanup = errors.New("worktree cleanup in progress; retry the owner after removal completes")

// WorktreeUserPaused reads a user's Stop even before it has reached disk. The
// caller already holds the workspace lease and verifies the worker record.
func (m *Manager) WorktreeUserPaused(y yesmen.Yesman, name string, since time.Time) bool {
	m.mu.Lock()
	if name != yesmen.LeadName {
		wp := m.workerPauses[ID(y.Name, name)]
		paused := wp != nil && wp.hiredAt.Equal(y.HiredAt) && wp.since.Equal(since) && wp.paused
		m.mu.Unlock()
		return paused
	}
	if lp := m.pauseOf(y); lp != nil && lp.known {
		paused := lp.paused
		m.mu.Unlock()
		return paused
	}
	m.mu.Unlock()
	c, err := m.cfg.Yesmen.Continuation(y.Name)
	return err != nil || !c.HiredAt.Equal(y.HiredAt) || !c.LeadPausedAt.IsZero()
}

// WithWorktreeOwner reserves the owner under the same lock used to register
// startup/input, without holding the manager lock during git or disk operations.
// A fallback cannot race a resumed owner. The caller may retire its own tree as
// the last sequential tool of its run. Completion evidence is checked separately;
// stopped never means completed.
func (m *Manager) WithWorktreeOwner(owner, caller string, remove func() error) error {
	m.mu.Lock()
	if m.closed || m.worktreeCleanup[owner] {
		m.mu.Unlock()
		return errWorktreeCleanup
	}
	if a := m.agents[owner]; a != nil {
		// Submit can be using the workspace before it has a process or a
		// registered prompt. Retirement can also still be draining its writer
		// after Stop has cleared the process. Neither boundary is idle yet.
		busy := a.dir != "" && a.deliveries == nil && a.deliveryErr == nil || a.removing ||
			a.retiring && a.deliveryWriter != nil && !a.deliveryWriter.finished() ||
			a.queue.Known && a.queue.Steering+a.queue.FollowUp > 0
		for _, d := range a.deliveries {
			busy = busy || d.State == "sending" && d.Session == ""
		}
		if caller == "" && (a.calls != 0 || a.deliveryErr != nil || len(a.operations) != 0 || m.workerPauses[owner] != nil && m.workerPauses[owner].paused) {
			busy = true
		}
		if busy || a.prompts != 0 || a.recalls != 0 || owner != caller && (a.starting != nil || a.proc != nil || a.reports != 0 || a.info.Status == Starting || a.info.Status == Working || a.info.Status == Waiting || a.info.Status == Idle) {
			m.mu.Unlock()
			return fmt.Errorf("retain worktree: owner %s is active", owner)
		}
	}
	if m.worktreeCleanup == nil {
		m.worktreeCleanup = map[string]bool{}
	}
	m.worktreeCleanup[owner] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.worktreeCleanup, owner)
		m.mu.Unlock()
	}()
	return remove()
}
