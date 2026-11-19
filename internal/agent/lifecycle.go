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
		// The drained writer already sealed this generation's archive snapshot.
		// A delayed ForgetYesman can only see the replacement after this point.
		m.clearRecallLocked(a)
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
	// The user's pauses of that hire go with it; another hire's stay.
	if lp := m.leadPauses[name]; lp != nil && lp.hiredAt.Equal(since) {
		delete(m.leadPauses, name)
	}
	for id, wp := range m.workerPauses {
		if ys, _, err := SplitID(id); err == nil && ys == name && wp.hiredAt.Equal(since) {
			delete(m.workerPauses, id)
		}
	}
	for id, a := range m.agents {
		if a.info.Yesman == name && a.workspaceSince.Equal(since) && a.retiring && a.proc == nil && a.starting == nil && (a.deliveryWriter == nil || a.deliveryWriter.finished()) {
			if a.token != "" {
				delete(m.tokens, a.token)
				a.token, a.tokenProc = "", nil
			}
			m.clearRecallLocked(a)
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
			cancelOperationsLocked(a, a.tokenProc)
			agents = append(agents, a)
		}
	}
	m.mu.Unlock()
	// Join initialization even if it began before retirement. Archive has already
	// waited for Submit's Use lease, but standalone stop also joins that boundary.
	var errs []error
	for _, a := range agents {
		if err := m.joinDeliveryLoad(a); err != nil {
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
	for id, d := range a.deliveries {
		if !d.recoverDraft {
			m.forgetRecallLocked(a, id)
		}
	}
	a.nativeQueue = pendingQueue{}
	var events []Event
	for _, d := range a.deliveries {
		if !pendingDelivery(d.State) && !d.AwaitingRecord {
			continue
		}
		d.State, d.Error, d.AwaitingRecord = "interrupted", "", false
		if d.Session == "" {
			d.State, d.Error = "failed", errStopped.Error()
		}
		events = append(events, m.deliveryLocked(a, d))
	}
	// Keep the payloads until the workspace is retired. A failed drain or
	// a second Stop may need to write this snapshot again.
	m.persistDeliveriesLocked(a)
	a.deliverySealed = true
	return events
}

// reopenWorkerInScope records that worker a was given work, clearing the
// user's pause only while resume is still its current setting.
func (m *Manager) reopenWorkerInScope(a *agent, resume workerResume) (bool, error) {
	if m.reviewBeforeReopen != nil {
		m.reviewBeforeReopen() // tests: the write is slow to begin
	}
	var reopened bool
	err := m.cfg.Yesmen.Use(a.info.Yesman, func(y yesmen.Yesman) error {
		var err error
		reopened, err = m.reopenWorkerIn(y, a, resume)
		return err
	})
	return reopened, err
}

func (m *Manager) reopenWorkerIn(y yesmen.Yesman, a *agent, resume workerResume) (bool, error) {
	m.mu.Lock()
	active := !a.retiring && m.agents[a.info.ID] == a && a.workspaceSince.Equal(y.HiredAt)
	m.mu.Unlock()
	if !active {
		return false, errStopped
	}
	if reviewR5BeforeStoreReopen != nil {
		reviewR5BeforeStoreReopen()
	}
	return m.cfg.Yesmen.ReopenWorkerIf(y.Name, a.info.Name, resume.since, func(yesmen.Worker) bool {
		return m.workerSettingCurrent(resume.id, resume.hiredAt, resume.since, resume.epoch)
	})
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

// reviewR5BeforeStoreReopen, tests only: runs as a worker given work, its
// scope checked, is about to be reopened in the workspace.
var reviewR5BeforeStoreReopen func()
