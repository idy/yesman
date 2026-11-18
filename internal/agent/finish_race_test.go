package agent

import (
	"context"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// The prompt that gave a finished worker work again may end only after its
// run's report reached the lead (hosted CI saw this on macOS): the worker is
// still closed as finished, and nothing given to it after its run is lost.
func TestFinishedWorkerClosesAfterItsPromptEnds(t *testing.T) {
	m, _ := newTestManager(t, 0)
	prompts := promptsTo(m)
	ctx := context.Background()
	if _, err := m.StartWorker(ctx, "alice", "w1", "Fix", "a task"); err != nil {
		t.Fatal(err)
	}
	id := ID("alice", "w1")
	waitFinished(t, m, id, time.Time{})
	release := make(chan struct{})
	held := make(chan struct{}, 1)
	m.beforePromptDone = func(a *agent) {
		if a.info.ID == id {
			select {
			case held <- struct{}{}:
				<-release
			default:
			}
		}
	}
	again := time.Now()
	errc := make(chan error, 1)
	go func() { errc <- m.Prompt(ctx, id, "[from your lead] one more thing") }()
	<-held
	lead := ID("alice", yesmen.LeadName)
	waitCount(t, prompts, lead, 2) // the second report reached the lead first
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	waitFinished(t, m, id, again)
}
