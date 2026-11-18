package agent

import (
	"time"

	"github.com/idy/yesman/internal/restart"
	"github.com/idy/yesman/internal/yesmen"
)

// beginRestartOperation shares shutdown's existing automatic-work drain.
// Registration and closing admission are serialized under the Manager lock.
func (m *Manager) beginRestartOperation() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.evaluations.Add(1)
	return true
}

// restartPausedLocked checks the pause of the exact recorded incarnation.
// Unlike an explicit message, a restart never clears either pause.
func (m *Manager) restartPausedLocked(r restart.Agent) bool {
	if r.Role == RoleLead {
		return m.leadPausedLocked(yesmen.Yesman{Name: r.Yesman, HiredAt: r.WorkspaceHiredAt})
	}
	wp := m.workerPauses[ID(r.Yesman, r.Name)]
	return wp != nil && wp.hiredAt.Equal(r.WorkspaceHiredAt) && wp.since.Equal(r.AgentSince) && wp.paused
}

// restartHasInputLocked distinguishes a read-only process/start and recovery's
// own bookkeeping from new incoming work. Both markers belong to the exact
// recorded generation and survive closing only its pi process.
func (m *Manager) restartHasInputLocked(r restart.Agent) bool {
	if a := m.agents[ID(r.Yesman, r.Name)]; a != nil && a.workspaceSince.Equal(r.WorkspaceHiredAt) && a.info.Since.Equal(r.AgentSince) && a.givenWork {
		return true
	}
	if r.Role == RoleLead {
		lp := m.pauseOf(yesmen.Yesman{Name: r.Yesman, HiredAt: r.WorkspaceHiredAt})
		return lp != nil && lp.givenWork
	}
	return false
}

// HoldAfterRestart preserves an interrupted human wait, unsuccessful recovery or uncertain report
// through later ordinary starts too. The existing pause record expresses the
// same contract: only an explicit message gives it work again. Never mark a
// same-name replacement, and never hold the Manager lock over storage I/O.
func (m *Manager) HoldAfterRestart(r restart.Agent) error {
	if !m.beginRestartOperation() {
		return errStopped
	}
	defer m.evaluations.Done()
	var persist func() error
	err := m.cfg.Yesmen.Use(r.Yesman, func(y yesmen.Yesman) error {
		if !y.HiredAt.Equal(r.WorkspaceHiredAt) {
			return yesmen.ErrRehired
		}
		id := ID(r.Yesman, r.Name)
		if r.Role == RoleLead && (r.Name != yesmen.LeadName || !r.AgentSince.Equal(y.HiredAt)) {
			return errStopped
		}
		if r.Role == RoleWorker {
			ws, err := m.cfg.Yesmen.Workers(y.Name)
			if err != nil {
				return err
			}
			found := false
			for _, w := range ws {
				found = found || w.Name == r.Name && w.StartedAt.Equal(r.AgentSince)
			}
			if !found {
				return errStopped
			}
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.restartHasInputLocked(r) {
			return errStopped // newer work owns the agent, including a pending start
		}
		if a := m.agents[id]; a != nil && (a.retiring || a.removing || !a.workspaceSince.Equal(r.WorkspaceHiredAt) || !a.info.Since.Equal(r.AgentSince)) {
			return errStopped
		}
		if r.Role == RoleLead {
			if !m.ownPauseLocked(y) {
				return errStopped
			}
			_, persist = m.setPauseLocked(y, true)
		} else {
			at := time.Now().UTC()
			epoch := m.setWorkerPauseLocked(id, y.HiredAt, r.AgentSince, at, true)
			persist = func() error { return m.persistWorkerPause(id, y.Name, r.Name, y.HiredAt, r.AgentSince, at, epoch) }
		}
		return nil
	})
	if err != nil {
		return err
	}
	return persist()
}
