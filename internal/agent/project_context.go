package agent

import (
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// ContextActor snapshots the live process without consulting Board or session
// history. Submission selects only dispatch metadata owned by this process.
func (m *Manager) ContextActor(token, submission string) (ContextActor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[m.tokens[token]]
	if token == "" || a == nil || a.token != token || a.proc == nil || a.proc != a.tokenProc ||
		m.closed || a.stopping || a.retiring || a.removing {
		return ContextActor{}, ErrRetired
	}
	select {
	case <-a.proc.done:
		return ContextActor{}, ErrRetired
	default:
	}
	out := ContextActor{Info: a.info, Hire: a.workspaceSince, Stop: a.humanStop, Worker: a.contextWorker}
	if d, ok := a.deliveries[submission]; ok && deliveryID.MatchString(submission) &&
		(pendingDelivery(d.State) || d.AwaitingRecord) && d.Session == a.proc.deliverySession() &&
		d.AgentSince.Equal(a.info.Since) && d.humanStop == a.humanStop {
		out.InputContext = d.hostContext
		if d.Origin != nil && d.Origin.Source == OriginSchedule {
			out.Plan, out.Run = d.Origin.Plan, d.Origin.Run
		}
	}
	return out, nil
}

type ContextActor struct {
	Info         Info
	Worker       yesmen.Worker
	Hire         time.Time
	Stop         uint64
	InputContext string
	Plan, Run    string
}
