package agent

import (
	"context"
	"sync"
	"time"
)

// externalOperation shares the exact process's changes gate with Board and
// CallerOperation. Unlike short local commits, network/gh work is cancellable:
// revocation cancels it under mu before retiring that gate outside mu.
type externalOperation struct {
	process *proc
	cancel  context.CancelFunc
}

// BeginOperation revalidates the server's original actor/token/workspace after
// decoding. Only admission holds Store.Use; network work keeps the process gate
// and releases it after the gh child and verification finish. No operation may
// reacquire Store.Use while holding this gate (Archive may be draining it).
func (m *Manager) BeginOperation(ctx context.Context, token string, expected Info, generation time.Time) (context.Context, func(), error) {
	y, release, err := m.cfg.Yesmen.BeginUse(expected.Yesman)
	if err != nil {
		return nil, nil, ErrRetired
	}
	defer release()
	m.mu.Lock()
	a := m.agents[expected.ID]
	var p *proc
	if token != "" && a != nil && a.token == token {
		p = a.tokenProc
	}
	m.mu.Unlock()
	if p == nil {
		return nil, nil, ErrRetired
	}
	p.changes.RLock()
	m.mu.Lock()
	active := m.agents[expected.ID] == a && m.tokens[token] == expected.ID && a.token == token &&
		a.tokenProc == p && a.proc == p && !p.changesClosed && !m.closed && !a.retiring && !a.removing && !a.stopping &&
		a.info.Since.Equal(expected.Since) && a.workspaceSince.Equal(generation) && y.HiredAt.Equal(generation)
	select {
	case <-p.done:
		active = false
	default:
	}
	if !active || ctx.Err() != nil {
		m.mu.Unlock()
		p.changes.RUnlock()
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		return nil, nil, ErrRetired
	}
	ctx, cancel := context.WithCancel(ctx)
	op := &externalOperation{process: p, cancel: cancel}
	if a.operations == nil {
		a.operations = make(map[*externalOperation]struct{})
	}
	a.operations[op] = struct{}{}
	m.mu.Unlock()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel()
			m.mu.Lock()
			delete(a.operations, op)
			m.mu.Unlock()
			p.changes.RUnlock()
		})
	}, nil
}

func cancelOperationsLocked(a *agent, p *proc) {
	for op := range a.operations {
		if op.process == p {
			op.cancel()
		}
	}
}
