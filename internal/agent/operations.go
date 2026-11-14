package agent

import (
	"context"
	"sync"
	"time"
)

// externalOperation binds a backend side effect to this exact running actor.
// Retirement cancels under mu and drains outside it; no network work holds mu
// or the workspace's Use lease. The completion fence also covers child exit.
type externalOperation struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// BeginOperation revalidates a request after decoding its untrusted body. The
// token, actor and workspace generation are the server's original snapshot.
// It reserves only the live incarnation, never a replacement with the same name.
func (m *Manager) BeginOperation(ctx context.Context, token string, expected Info, generation time.Time) (context.Context, func(), error) {
	y, release, err := m.cfg.Yesmen.BeginUse(expected.Yesman)
	if err != nil {
		return nil, nil, errStopped
	}
	defer release()
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[expected.ID]
	if token == "" || m.tokens[token] != expected.ID || a == nil || a.token != token ||
		m.closed || a.retiring || a.removing || a.stopping || a.proc == nil ||
		!a.info.Since.Equal(expected.Since) || !a.workspaceSince.Equal(generation) || !y.HiredAt.Equal(generation) {
		return nil, nil, errStopped
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	op := &externalOperation{cancel: cancel, done: make(chan struct{})}
	if a.operations == nil {
		a.operations = make(map[*externalOperation]struct{})
	}
	a.operations[op] = struct{}{}
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel()
			m.mu.Lock()
			delete(a.operations, op)
			close(op.done)
			m.mu.Unlock()
		})
	}, nil
}

func cancelOperationsLocked(a *agent) []*externalOperation {
	operations := make([]*externalOperation, 0, len(a.operations))
	for op := range a.operations {
		op.cancel()
		operations = append(operations, op)
	}
	return operations
}

func drainOperations(operations []*externalOperation) {
	for _, op := range operations {
		<-op.done
	}
}
