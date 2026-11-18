package agent

import (
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// A task told about, then done, then reopened is told about again; so is a
// task whose dependency was done, undone and done again. A note alone is
// no change.
func TestContinuationReopenedTaskIsToldAgain(t *testing.T) {
	m, store := newTestManager(t, 0)
	prompts := promptsTo(m)
	addTasks(t, store, yesmen.TaskEdit{Title: str("Orphan")}, yesmen.TaskEdit{Title: str("Predecessor"), Status: str("doing"), Owner: str("busy")}, yesmen.TaskEdit{Title: str("Dependent")})
	blockOn(t, store, "t3", "t2")
	if err := store.AddWorker("alice", yesmen.Worker{Name: "busy", Task: "synthetic", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock() // busy carries t2
	m.agents["alice/busy"] = &agent{info: Info{ID: "alice/busy", Yesman: "alice", Name: "busy", Role: RoleWorker, Status: Working, Since: workerSince(t, store, "busy")}}
	m.mu.Unlock()
	lead := ID("alice", yesmen.LeadName)
	if _, _, err := m.ensure(lead); err != nil {
		t.Fatal(err)
	}
	set := func(id, status string) {
		t.Helper()
		if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: id, Status: str(status)}}}); err != nil {
			t.Fatal(err)
		}
	}
	settle := func() {
		t.Helper()
		waitStatus(t, m, lead, Idle)
		time.Sleep(150 * time.Millisecond)
		m.continueTasks("alice")
		time.Sleep(150 * time.Millisecond)
	}
	set("t2", "done")
	settle()
	waitNoticeListing(t, prompts, lead, "t1")
	waitNoticeListing(t, prompts, lead, "t3")
	before := len(noticesOf(prompts(lead)))

	if _, err := store.ChangeBoard("alice", yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: "t1", Note: str("just a note")}}}); err != nil {
		t.Fatal(err)
	}
	settle()
	quiet(t, prompts, lead, before)

	set("t1", "done")
	set("t2", "doing")
	settle()
	set("t1", "todo")
	set("t2", "done")
	settle()
	got := noticesOf(prompts(lead))
	if len(got) <= before || noticesListing(got[before:], "t1") == 0 || noticesListing(got[before:], "t3") == 0 {
		t.Fatalf("reopened t1 or re-readied t3 not told again: %q", got[before:])
	}
}

// The user stopping an asleep lead after the evaluation read its pause, but
// before the notice starts it, keeps it stopped.
func TestContinuationLatePauseOfAsleepLead(t *testing.T) {
	m, store := newTestManager(t, 0)
	addTasks(t, store, yesmen.TaskEdit{Title: str("Orphan")})
	lead := ID("alice", yesmen.LeadName)
	prompts := promptsTo(m)
	m.beforeNotice = func() { m.StopByUser(lead) }
	m.continueTasks("alice")
	quiet(t, prompts, lead, 0)
	if m.running(lead) != nil {
		t.Fatal("started a lead the user stopped meanwhile")
	}
	if c, _ := store.Continuation("alice"); len(c.Attempts) != 0 {
		t.Fatalf("recorded a notice that must not go: %+v", c)
	}
}
