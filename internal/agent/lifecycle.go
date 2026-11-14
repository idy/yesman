package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// Called only inside Store.Use. A failed Archive leaves the original workspace
// in place; a sealed writer can be replaced there only after its goroutine has
// completely exited. A new hire with the same name gets a fresh agent object.
func (m *Manager) workspaceAgentLocked(y yesmen.Yesman, info Info) (*agent, error) {
	a := m.agents[info.ID]
	if a != nil && (((a.retiring || !a.workspaceSince.IsZero()) && !a.workspaceSince.Equal(y.HiredAt)) || ((a.retiring || !a.info.Since.IsZero()) && !a.info.Since.Equal(info.Since))) {
		if a.proc != nil || a.starting != nil || a.deliveryWriter != nil && !a.deliveryWriter.finished() {
			return nil, errStopped
		}
		a = nil
	}
	if a != nil && a.removing {
		return nil, errStopped
	}
	if a == nil {
		a = &agent{info: Info{ID: info.ID, Yesman: y.Name, Name: info.Name, Role: info.Role, Status: Stopped, Since: info.Since}, dir: y.Path, workspaceSince: y.HiredAt}
		m.agents[info.ID] = a
	}
	if a.retiring {
		if a.deliveryWriter != nil && !a.deliveryWriter.finished() {
			return nil, errStopped
		}
		if m.closed {
			return nil, errStopped
		}
		a.retiring, a.deliverySealed = false, false
		if a.deliveryWriter != nil {
			a.deliveryWriter = newDeliveryWriter(filepath.Join(y.SessionDir(info.Name), "delivery.json"), m.cfg.deliveryWrite)
			m.persistDeliveriesLocked(a)
		}
	}
	a.dir, a.workspaceSince, a.info.Since = y.Path, y.HiredAt, info.Since
	return a, nil
}

// ForgetYesman releases only the drained incarnation that Archive moved. A
// same-name hire may already exist when service post-processing gets here.
func (m *Manager) ForgetYesman(name string, since time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, a := range m.agents {
		if a.info.Yesman == name && a.workspaceSince.Equal(since) && a.retiring && a.proc == nil && a.starting == nil && (a.deliveryWriter == nil || a.deliveryWriter.finished()) {
			if a.token != "" {
				delete(m.tokens, a.token)
				a.token = ""
			}
			delete(m.agents, id)
		}
	}
}

// StopYesman stops and drains a workspace's agents. Archive calls this while
// holding its exclusive Store lifecycle lock; new Use operations cannot start
// until the move succeeds or fails. Standalone calls still allow later messages
// to resume the stopped workspace through Use.
func (m *Manager) StopYesman(name string) error {
	return m.StopYesmanContext(context.Background(), name)
}
func (m *Manager) StopYesmanContext(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	y, err := m.cfg.Yesmen.Get(name)
	if err != nil {
		return err
	}
	m.mu.Lock()
	var agents []*agent
	for _, a := range m.agents {
		if a.info.Yesman == name && (a.workspaceSince.IsZero() || a.workspaceSince.Equal(y.HiredAt)) {
			a.retiring = true
			agents = append(agents, a)
		}
	}
	m.mu.Unlock()
	// Join initialization even if it began before retirement. Archive has already
	// waited for Submit's Use lease, but standalone stop also joins that boundary.
	var errs []error
	for _, a := range agents {
		if err := m.ensureDeliveries(a, filepath.Join(y.SessionDir(a.info.Name), "delivery.json")); err != nil {
			errs = append(errs, err)
		}
	}
	var group sync.WaitGroup
	for _, a := range agents {
		group.Go(func() { m.Stop(a.info.ID) })
	}
	group.Wait()
	m.mu.Lock()
	var events []Event
	var writers []*deliveryWriter
	for _, a := range agents {
		if a.deliveryWriter == nil {
			continue
		}
		if a.deliverySealed && a.deliveryWriter.finished() {
			// Retry a failed/previous drain only once its old writer cannot write again.
			a.deliverySealed = false
			a.deliveryWriter = newDeliveryWriter(filepath.Join(y.SessionDir(a.info.Name), "delivery.json"), m.cfg.deliveryWrite)
		}
		if !a.deliverySealed {
			events = append(events, m.sealDeliveriesLocked(a)...)
		}
		writers = append(writers, a.deliveryWriter)
	}
	m.mu.Unlock()
	for _, event := range events {
		m.cfg.OnEvent(event)
	}
	results := make(chan error, len(writers))
	for _, writer := range writers {
		group.Go(func() { results <- writer.close(ctx) })
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("stop workspace %s: %w", name, err)
	}
	return nil
}

// No caller can enqueue metadata after this final snapshot. In-flight RPC and
// submission goroutines keep the old object, fenced by retiring/sealed state.
func (m *Manager) sealDeliveriesLocked(a *agent) []Event {
	if a.deliverySealed {
		return nil
	}
	var events []Event
	for _, d := range a.deliveries {
		if !pendingDelivery(d.State) {
			continue
		}
		d.State, d.Error = "interrupted", ""
		if d.Session == "" {
			d.State, d.Error = "failed", errStopped.Error()
		}
		events = append(events, m.deliveryLocked(a, d))
	}
	m.persistDeliveriesLocked(a)
	a.deliverySealed = true
	return events
}

func (m *Manager) reopenWorkerInScope(a *agent) (bool, error) {
	var reopened bool
	err := m.cfg.Yesmen.Use(a.info.Yesman, func(y yesmen.Yesman) error {
		m.mu.Lock()
		active := !a.retiring && m.agents[a.info.ID] == a && a.workspaceSince.Equal(y.HiredAt)
		m.mu.Unlock()
		if !active {
			return errStopped
		}
		var err error
		reopened, err = m.cfg.Yesmen.ReopenWorker(y.Name, a.info.Name)
		return err
	})
	return reopened, err
}

// Snapshot only immutable generation information for asynchronous reports.
func (m *Manager) reportScope(a *agent) (time.Time, bool) {
	m.mu.Lock()
	scope, active := a.workspaceSince, !a.retiring && !m.closed
	m.mu.Unlock()
	if active && scope.IsZero() {
		y, err := m.cfg.Yesmen.Get(a.info.Yesman)
		if err != nil {
			return scope, false
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if a.retiring || m.closed {
			return scope, false
		}
		if a.workspaceSince.IsZero() {
			a.workspaceSince = y.HiredAt
		}
		return a.workspaceSince, true
	}
	return scope, active
}
