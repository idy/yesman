package agent

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// A task evaluation already running when the app quits ends before StopAll
// returns, so nothing writes the workspace's record after it: the cause of
// a test's temporary directory found not empty at cleanup.
func TestR7StopAllJoinsRunningEvaluation(t *testing.T) {
	m, s := newTestManager(t, 0)
	y, _ := s.Get("alice")
	addTasks(t, s, yesmen.TaskEdit{Title: str("Orphan")})
	if _, _, err := m.ensure("alice/lead"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	m.beforeNotice = func() { close(entered); <-release }
	go m.continueTasks("alice")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no evaluation reached the notice")
	}
	stopped := make(chan struct{})
	go func() { m.StopAll(); close(stopped) }()
	running := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		c := m.continuations["alice"]
		return c != nil && c.running
	}
	select {
	case <-stopped:
		// StopAll returned with the evaluation still held: it did not join.
		if running() {
			t.Error("StopAll returned while a task evaluation still ran")
		}
		close(release)
	case <-time.After(time.Second):
		close(release) // StopAll is waiting for it, as it should
		select {
		case <-stopped:
		case <-time.After(15 * time.Second):
			t.Fatal("StopAll did not return once the evaluation ended")
		}
	}
	if running() {
		t.Error("a task evaluation ran on after StopAll returned")
	}
	// Nothing is left being written in the workspace's .yesman.
	entries, err := os.ReadDir(y.MetaDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a write was left in progress: %s", e.Name())
		}
	}
	// No evaluation starts once closed.
	m.continueTasks("alice")
	if running() {
		t.Error("an evaluation started after StopAll")
	}
}
