package agent

import "github.com/idy/yesman/internal/yesmen"

// DeliveryFence binds a delivery confirmation to the lead process and user
// pause generation that were current when its HTTP request arrived.
type DeliveryFence struct {
	agent       *agent
	proc        *proc
	stop, epoch uint64
}

func (m *Manager) CaptureDeliveryFence(token string) DeliveryFence {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[m.tokens[token]]
	if token == "" || a == nil || a.info.Role != RoleLead || a.token != token || a.tokenProc == nil {
		return DeliveryFence{}
	}
	f := DeliveryFence{agent: a, proc: a.tokenProc, stop: a.humanStop}
	if lp := m.pauseOf(yesmen.Yesman{Name: a.info.Yesman, HiredAt: a.workspaceSince}); lp != nil {
		if lp.paused {
			return DeliveryFence{}
		}
		f.epoch = lp.epoch
	}
	return f
}

func (m *Manager) deliveryCurrentLocked(f DeliveryFence, y yesmen.Yesman) bool {
	a := f.agent
	if a == nil || m.agents[ID(y.Name, yesmen.LeadName)] != a || a.tokenProc != f.proc || a.proc != f.proc ||
		!a.workspaceSince.Equal(y.HiredAt) || a.humanStop != f.stop || m.closed || a.stopping || a.retiring || a.removing {
		return false
	}
	select {
	case <-f.proc.done:
		return false
	default:
	}
	if lp := m.pauseOf(y); lp != nil {
		return !lp.paused && lp.epoch == f.epoch
	}
	return f.epoch == 0
}

// CheckDelivery reads a saved pause only when this Manager has not seen it.
// It runs within the workspace lease, before locking the Board or handoffs.
func (m *Manager) CheckDelivery(f DeliveryFence, y yesmen.Yesman) error {
	m.mu.Lock()
	current := m.deliveryCurrentLocked(f, y)
	lp := m.pauseOf(y)
	known := lp != nil && lp.known
	m.mu.Unlock()
	if !current {
		return ErrRetired
	}
	if !known {
		record, err := m.cfg.Yesmen.Continuation(y.Name)
		if err != nil {
			return err
		}
		if !record.HiredAt.Equal(y.HiredAt) || !record.LeadPausedAt.IsZero() {
			return ErrRetired
		}
	}
	return nil
}

// BeginDeliveryCommit rechecks the fence immediately before the staged
// rename. Hold it through the rename and the nonblocking delivery callback.
// The caller already holds the process admission returned by BeginChange.
func (m *Manager) BeginDeliveryCommit(f DeliveryFence, y yesmen.Yesman) (func(), error) {
	if f.proc == nil {
		return nil, ErrRetired
	}
	f.proc.deliveryCommit.Lock()
	m.mu.Lock()
	ok := m.deliveryCurrentLocked(f, y)
	m.mu.Unlock()
	if !ok {
		f.proc.deliveryCommit.Unlock()
		return nil, ErrRetired
	}
	return f.proc.deliveryCommit.Unlock, nil
}
