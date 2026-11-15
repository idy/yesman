package agent

import (
	"errors"
	"sync"

	"github.com/idy/yesman/internal/yesmen"
)

// ErrRetired refuses a change asked for with an agent's credential that
// was retired, or began to be, before the change could begin: its process
// stopped or is stopping, or its yesman was dismissed, maybe hired again.
var ErrRetired = errors.New("the agent's credential was retired")

// BeginChange admits a change to workspace y by the process token was given
// to, which must still run there as the agent it was started as. Until
// release, that process cannot finish stopping: Stop, and with it dismissal,
// worker removal and quitting, waits for the change to end, and refuses
// any change that has not begun. A change authenticated when its request
// arrived therefore cannot land after its agent stopped or was replaced.
//
// Call it inside the Store's Use of y, after checking y is the generation
// the request came from, and hold release across the change's commit only,
// never across reading a request. It waits for no lock of the manager's
// beyond a short check.
func (m *Manager) BeginChange(token string, y yesmen.Yesman) (release func(), err error) {
	current := func() (*agent, *proc) {
		id, ok := m.tokens[token]
		a := m.agents[id]
		if !ok || token == "" || a == nil || a.token != token || a.tokenProc == nil {
			return nil, nil
		}
		return a, a.tokenProc
	}
	m.mu.Lock()
	_, p := current()
	m.mu.Unlock()
	if p == nil {
		return nil, ErrRetired
	}
	p.changes.RLock()
	m.mu.Lock()
	a, q := current()
	ok := q == p && !p.changesClosed && !m.closed && !a.stopping && !a.retiring && !a.removing &&
		a.info.Yesman == y.Name && a.workspaceSince.Equal(y.HiredAt)
	m.mu.Unlock()
	if !ok {
		p.changes.RUnlock()
		return nil, ErrRetired
	}
	var once sync.Once
	return func() { once.Do(p.changes.RUnlock) }, nil
}

// retireCredential ends the credential of p, a process of a that exited or
// was stopped. First, under a short hold of mu, the credential stops
// authenticating, so no new request with it is accepted and no new change
// begins. Then, outside mu, it waits for the changes requests with it had
// already begun, which still land. Every path that forgets a process's
// credential calls it, after the process ended and before clearLocked.
func (m *Manager) retireCredential(a *agent, p *proc) {
	m.mu.Lock()
	m.revokeLocked(a, p)
	m.mu.Unlock()
	p.retireChanges()
}

// revokeLocked forgets the credential a holds for p, so that it no longer
// authenticates. The credential of a process started since is not p's,
// and stays.
func (m *Manager) revokeLocked(a *agent, p *proc) {
	// Cancel this exact process's network/gh operations before waiting on
	// its shared changes gate. A late old watcher must not cancel a new run.
	cancelOperationsLocked(a, p)
	if a.tokenProc == p && a.token != "" {
		delete(m.tokens, a.token)
		a.token, a.tokenProc = "", nil
	}
}

// retireChanges waits for the changes that requests with this process's
// credential began, and refuses later ones. Never call it holding the
// manager's mu or the Store's own mutexes, which a change may wait for
// while it holds the gate. Holding a workspace's Use, or Archive's hold of
// it, is fine: a change takes its workspace's lease before the gate, never
// while holding it.
func (p *proc) retireChanges() {
	p.changes.Lock()
	p.changesClosed = true
	p.changes.Unlock()
}
