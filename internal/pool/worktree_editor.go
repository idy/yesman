package pool

import (
	"path/filepath"
	"strings"
	"sync"
)

// HoldWorktreeEditor registers an editor before it becomes writable. Unlike a
// long-lived read lock, it does not block reads/saves while a writer is waiting.
// An editor opened through All files may not yet know the Git instance. It still
// holds the same physical directory that cleanup would have to remove.
func (p *Pool) HoldWorktreeEditor(path, repo, id string) (func(), error) {
	l, err := p.treeLock(path)
	if err != nil {
		return nil, err
	}
	l.RLock()
	defer l.RUnlock()
	if id != "" {
		if !cleanupIDRE.MatchString(id) {
			return nil, &RemovalHold{"The worktree instance changed or its private marker is missing.", filepath.Base(path)}
		}
		for segment := range strings.SplitSeq(repo, "/") {
			if !validSegment(segment) {
				return nil, &RemovalHold{"The worktree instance changed or its private marker is missing.", filepath.Base(path)}
			}
		}
		opened, _, err := openInstanceRoot(p.dir(repo), path, id)
		if err != nil {
			return nil, err
		}
		opened.Close()
	} else {
		opened, err := openWorktree(path)
		if err != nil {
			return nil, err
		}
		opened.Close()
	}
	p.mu.Lock()
	if p.edits == nil {
		p.edits = make(map[*sync.RWMutex]int)
	}
	p.edits[l]++
	p.mu.Unlock()
	return sync.OnceFunc(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.edits[l]--
		if p.edits[l] == 0 {
			delete(p.edits, l)
		}
	}), nil
}

func (p *Pool) checkWorktreeEditors(l *sync.RWMutex) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.edits[l] != 0 {
		return &RemovalHold{Reason: "A file editor is open in this worktree."}
	}
	return nil
}
