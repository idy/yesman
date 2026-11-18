package agent

import (
	"log"
	"sync"
	"time"

	"github.com/idy/yesman/internal/restart"
	"github.com/idy/yesman/internal/yesmen"
)

// restartTaskNotice records the tasks covered by the lead's restart note before
// sending it. Startup evaluation and a fast settle must not send another notice
// for the same state. The existing ledger owns the budget and generations;
// workers' reports remain separate messages and never finish board tasks.
func (m *Manager) restartTaskNotice(r restart.Agent) (func(string), error) {
	noop := func(string) {}
	if r.Role != RoleLead {
		return noop, nil
	}
	y, err := m.cfg.Yesmen.Get(r.Yesman)
	if err != nil {
		return nil, err
	}
	if !y.HiredAt.Equal(r.WorkspaceHiredAt) {
		return nil, yesmen.ErrRehired
	}
	rev := m.cfg.Yesmen.Revision(y.Name)
	board, err := m.cfg.Yesmen.Board(y.Name)
	if err != nil {
		return nil, err
	}
	workers, err := m.cfg.Yesmen.Workers(y.Name)
	if err != nil {
		return nil, err
	}
	record, err := m.cfg.Yesmen.Continuation(y.Name)
	if err != nil {
		return nil, err
	}
	handoffs, handoffErr := m.cfg.Yesmen.Handoffs(y.Name)
	runs := map[string]ownerRun{}
	m.mu.Lock()
	for _, a := range m.agents {
		if a.info.Yesman == y.Name && a.info.Role == RoleWorker && !a.retiring {
			runs[a.info.Name] = ownerRun{since: a.info.Since, active: busyLocked(a), status: a.info.Status}
		}
	}
	for id, wp := range m.workerPauses {
		ys, name, _ := SplitID(id)
		if ys == y.Name && wp.hiredAt.Equal(y.HiredAt) {
			run := runs[name]
			if run.since.IsZero() || run.since.Equal(wp.since) {
				run.since, run.paused, run.resumed = wp.since, wp.paused, !wp.paused
				runs[name] = run
			}
		}
	}
	m.mu.Unlock()
	var listed []notice
	for _, n := range classifyAs(board, workers, runs, handoffs, handoffErr != nil) {
		if n.verify {
			continue
		}
		prev, seen := record.Tasks[n.task.ID]
		if seen && prev.Sig == n.sig && (prev.Delivered || prev.RestartReserved) {
			continue
		}
		n.count = 1
		if seen && prev.Progress == n.progress {
			n.count = prev.Count + 1
		}
		n.previousNotice, n.noticed = prev, seen
		listed = append(listed, n)
	}
	if len(listed) == 0 {
		return noop, nil
	}
	at := time.Now().UTC()
	if err := m.recordAttempt(y, listed, at, false, board, rev); err != nil {
		return nil, err
	}
	// Reserve durably before the pipe write, separately from Delivered:
	// a crash/ambiguous send cannot cause replay or claim pi accepted it.
	err = m.cfg.Yesmen.Use(y.Name, func(cur yesmen.Yesman) error {
		if !cur.HiredAt.Equal(y.HiredAt) {
			return yesmen.ErrRehired
		}
		_, err := m.cfg.Yesmen.UpdateContinuation(y.Name, y.HiredAt, func(c *yesmen.Continuation) bool {
			changed := false
			for _, n := range listed {
				if r, ok := c.Tasks[n.task.ID]; ok && r.Sig == n.sig && r.At.Equal(at) {
					r.RestartReserved = true
					c.Tasks[n.task.ID] = r
					changed = true
				}
			}
			return changed
		})
		return err
	})
	if err != nil {
		m.unrecordAttempt(y, listed, at)
		return nil, err
	}
	var once sync.Once
	return func(state string) {
		once.Do(func() {
			if state == restart.ReportNotSent {
				m.unrecordAttempt(y, listed, at)
			} else if state == restart.ReportAccepted {
				if err := m.recordAttempt(y, listed, at, true, board, 0); err != nil {
					log.Printf("restart: record accepted task notice of %s: %v", y.Name, err)
				}
			}
		})
	}, nil
}
