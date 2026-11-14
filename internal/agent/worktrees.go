package agent

import (
	"errors"
	"fmt"
)

var errWorktreeCleanup = errors.New("worktree cleanup in progress; retry the owner after removal completes")

// WithWorktreeOwner reserves the owner under the same lock used to register
// startup/input, without holding the manager lock during git or disk operations.
// A fallback cannot race a resumed owner. The caller may retire its own tree as
// the last sequential tool of its run. Completion evidence is checked separately;
// stopped never means completed.
func (m *Manager) WithWorktreeOwner(owner, caller string, remove func() error) error {
	m.mu.Lock()
	if m.worktreeCleanup[owner] {
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
		if busy || a.prompts != 0 || owner != caller && (a.starting != nil || a.proc != nil || a.reports != 0 || a.info.Status == Starting || a.info.Status == Working || a.info.Status == Waiting || a.info.Status == Idle) {
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
