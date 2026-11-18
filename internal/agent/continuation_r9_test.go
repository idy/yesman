package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// unreadableMeta makes name's workspace metadata unreadable, as a file grown
// past what yesman reads is, and returns what puts it back.
func unreadableMeta(t *testing.T, s *yesmen.Store, name string) (restore func()) {
	t.Helper()
	y, err := s.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(y.MetaDir(), "yesman.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(append([]byte{}, raw...), strings.Repeat(" ", 1<<20)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(name); err == nil {
		t.Fatal("the workspace still reads")
	}
	done := false
	restore = func() {
		if !done {
			done = true
			if err := os.WriteFile(file, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(restore)
	return restore
}

// The user's Stop or Abort of a running lead holds even when its workspace
// cannot be read then: the pause is the running lead's hire's, set before
// any storage is read; not saving it is shown and reported, and it is saved
// once the workspace reads again, so a later Manager reads it too. The
// user's own message lifts it as ever.
func TestR9LeadStopHoldsWhenTheWorkspaceCannotBeRead(t *testing.T) {
	for _, abort := range []bool{false, true} {
		name := "Stop"
		if abort {
			name = "Abort"
		}
		t.Run(name, func(t *testing.T) {
			m, s := newTestManager(t, 0)
			id := ID("alice", yesmen.LeadName)
			y, _ := s.Get("alice")
			if _, _, err := m.ensure(id); err != nil {
				t.Fatal(err)
			}
			if m.loadPause(y) {
				t.Fatal("a new lead starts paused")
			}
			restore := unreadableMeta(t, s, "alice")
			if abort {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				err := m.AbortByUser(ctx, id)
				if err == nil || !strings.Contains(err.Error(), "could not be saved") {
					t.Fatalf("Abort did not report the unsaved stop: %v", err)
				}
			} else {
				m.StopByUser(id)
			}
			if abort {
				if m.running(id) == nil {
					t.Fatal("Abort closed the lead's process instead of stopping its run")
				}
			} else if m.running(id) != nil {
				t.Fatal("Stop returned before the lead's process ended")
			}
			m.mu.Lock()
			lp := m.pauseOf(y)
			held := lp != nil && lp.known && lp.paused && lp.local && !lp.persisted && m.leadPausedLocked(y)
			m.mu.Unlock()
			if !held {
				t.Fatalf("the user's pause was not kept for the running hire: %+v", lp)
			}
			// m.Get reads the workspace too, which cannot be read: the info
			// shown is the loaded agent's.
			m.mu.Lock()
			detail := m.agents[id].info.Detail
			m.mu.Unlock()
			if !strings.HasPrefix(detail, pauseUnsaved) {
				t.Fatalf("the unsaved stop is not shown: %q", detail)
			}
			if !m.deferForLead("alice", y.HiredAt, func() {}) {
				t.Fatal("yesman's own message was not held for the paused lead")
			}

			restore()
			if !m.loadPause(y) {
				t.Fatal("the lead is not paused once the workspace reads again")
			}
			if c, err := s.Continuation("alice"); err != nil || c.LeadPausedAt.IsZero() {
				t.Fatalf("the pause was not saved once the workspace read again: %+v %v", c, err)
			}
			if info, _ := m.Get(id); strings.HasPrefix(info.Detail, pauseUnsaved) {
				t.Fatalf("the saved stop is still shown as unsaved: %q", info.Detail)
			}
			if !reloadedPause(t, s, y) {
				t.Fatal("a later Manager does not read the user's pause")
			}

			info, _ := m.Get(id)
			if _, err := m.Submit(context.Background(), id, deliveryTestID(1), "go on", nil, info.Since); err != nil {
				t.Fatal(err)
			}
			if c, _ := s.Continuation("alice"); !c.LeadPausedAt.IsZero() {
				t.Fatal("the user's own message did not lift the pause")
			}
		})
	}
}

// Ordinary loaded-lead Stop/Abort saves the exact hire's pause without an
// error marker. A subsequent user message resumes it and clears the record.
func TestR9LoadedLeadStopAndResume(t *testing.T) {
	for _, abort := range []bool{false, true} {
		name := "Stop"
		if abort {
			name = "Abort"
		}
		t.Run(name, func(t *testing.T) {
			m, s := newTestManager(t, 0)
			id := ID("alice", yesmen.LeadName)
			y, _ := s.Get("alice")
			if _, _, err := m.ensure(id); err != nil {
				t.Fatal(err)
			}
			if abort {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := m.AbortByUser(ctx, id); err != nil {
					t.Fatal(err)
				}
			} else {
				m.StopByUser(id)
			}
			c, err := s.Continuation("alice")
			if err != nil || !c.HiredAt.Equal(y.HiredAt) || c.LeadPausedAt.IsZero() {
				t.Fatalf("pause not saved for this hire: %+v %v", c, err)
			}
			info, err := m.Get(id)
			if err != nil || strings.HasPrefix(info.Detail, pauseUnsaved) {
				t.Fatalf("ordinary stop reported unsaved: %+v %v", info, err)
			}
			if _, err := m.Submit(context.Background(), id, deliveryTestID(1), "continue", nil, info.Since); err != nil {
				t.Fatal(err)
			}
			if m.loadPause(y) || reloadedPause(t, s, y) {
				t.Fatal("user message did not clear the pause in memory and on disk")
			}
		})
	}
}

// A captured Stop still waiting to be saved must not overwrite a later
// user setting after the workspace metadata becomes readable again.
func TestR9RecoveredMetadataKeepsNewestSetting(t *testing.T) {
	for _, stopAgain := range []bool{false, true} {
		name := "resume"
		if stopAgain {
			name = "resume then stop"
		}
		t.Run(name, func(t *testing.T) {
			m, s := newTestManager(t, 0)
			id := ID("alice", yesmen.LeadName)
			y, _ := s.Get("alice")
			if _, _, err := m.ensure(id); err != nil {
				t.Fatal(err)
			}
			restore := unreadableMeta(t, s, "alice")
			older := m.pauseByUser(id)
			restore()
			m.resumeByUser(id)()
			if stopAgain {
				m.StopByUser(id)
			}
			before, err := s.Continuation("alice")
			if err != nil {
				t.Fatal(err)
			}
			if err := older(); err != nil {
				t.Fatal(err)
			}
			after, err := s.Continuation("alice")
			if err != nil || !after.LeadPausedAt.Equal(before.LeadPausedAt) || m.loadPause(y) != stopAgain || reloadedPause(t, s, y) != stopAgain {
				t.Fatalf("old write changed the latest setting: before=%v after=%v error=%v", before.LeadPausedAt, after.LeadPausedAt, err)
			}
		})
	}
}

// reloadedPause reports whether a Manager started now reads y's lead paused.
func reloadedPause(t *testing.T, s *yesmen.Store, y yesmen.Yesman) bool {
	t.Helper()
	m := New(Config{Pi: "/nonexistent", Yesmen: s})
	t.Cleanup(m.StopAll)
	return m.loadPause(y)
}

// With no lead of it running here, a workspace that cannot be read leaves
// nothing to set the user's pause on: that is reported, not taken as saved.
// Another workspace's Stop, at the same time, is saved as ever.
func TestR9StopWithoutItsHireIsReportedAndScoped(t *testing.T) {
	m, s := newTestManager(t, 0)
	if _, err := s.Hire("bob"); err != nil {
		t.Fatal(err)
	}
	alice, bob := ID("alice", yesmen.LeadName), ID("bob", yesmen.LeadName)
	if _, _, err := m.ensure(bob); err != nil {
		t.Fatal(err)
	}
	restore := unreadableMeta(t, s, "alice")
	var wg sync.WaitGroup
	var aliceErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		aliceErr = m.pauseByUser(alice)()
	}()
	go func() {
		defer wg.Done()
		m.StopByUser(bob)
	}()
	wg.Wait()
	if aliceErr == nil {
		t.Fatal("a Stop that found no hire to record was taken as saved")
	}
	if c, err := s.Continuation("bob"); err != nil || c.LeadPausedAt.IsZero() {
		t.Fatalf("bob's Stop was not saved: %+v %v", c, err)
	}
	if info, _ := m.Get(bob); strings.HasPrefix(info.Detail, pauseUnsaved) {
		t.Fatalf("bob's saved Stop is shown as unsaved: %q", info.Detail)
	}
	restore()
	if err := m.pauseByUser(alice)(); err != nil {
		t.Fatalf("Stop once the workspace reads: %v", err)
	}
	if c, err := s.Continuation("alice"); err != nil || c.LeadPausedAt.IsZero() {
		t.Fatalf("alice's Stop was not saved once the workspace read: %+v %v", c, err)
	}
}
