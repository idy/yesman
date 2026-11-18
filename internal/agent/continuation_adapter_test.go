package agent

// The adapter of continuation_expect_test.go for this version's API.

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func isNotice(s string) bool { return strings.HasPrefix(s, noticeHeader) }

// leadSettled evaluates as the lead settling does.
func leadSettled(t *testing.T, m *Manager) {
	t.Helper()
	m.continueTasks("alice")
}

// appStarted is what the app does once it started.
func appStarted(t *testing.T, m *Manager) {
	t.Helper()
	m.Resume()
}

// userStop is the user's Stop in the window.
func userStop(m *Manager, id string) { m.StopByUser(id) }

// blockOn records that a task waits for others.
func blockOn(t *testing.T, store *yesmen.Store, task string, deps ...string) {
	t.Helper()
	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: task, Wait: &yesmen.WaitEdit{Kind: yesmen.WaitTasks, Tasks: deps}}}}); err != nil {
		t.Fatal(err)
	}
}

func workerSince(t *testing.T, store *yesmen.Store, name string) time.Time {
	t.Helper()
	ws, err := store.Workers("alice")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(ws, func(w yesmen.Worker) bool { return w.Name == name })
	if i < 0 {
		t.Fatalf("no worker %s", name)
	}
	return ws[i].StartedAt
}
