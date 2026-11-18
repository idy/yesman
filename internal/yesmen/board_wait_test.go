package yesmen

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s := New(filepath.Join(dir, "yesmen"), filepath.Join(dir, "archive"))
	if _, err := s.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	return s
}

func sp(s string) *string { return &s }

func TestTaskWait(t *testing.T) {
	s := waitStore(t)
	b, err := s.ChangeBoard("alice", BoardChange{Add: []TaskEdit{
		{Title: sp("Publish")},
		{Title: sp("After publish"), Wait: &WaitEdit{Kind: "TASKS", Tasks: []string{"1", "#1"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	w := b.Tasks[1].Wait
	if b.Tasks[1].Status != TaskBlocked || w == nil || w.Kind != WaitTasks || len(w.Tasks) != 1 || w.Tasks[0] != "t1" || w.Since.IsZero() {
		t.Fatalf("waiting task: %+v %+v", b.Tasks[1], w)
	}
	since := w.Since
	// An unchanged wait keeps when it began; a note does not touch it.
	b, err = s.ChangeBoard("alice", BoardChange{Update: []TaskEdit{{ID: "t2", Note: sp("still"), Wait: &WaitEdit{Kind: WaitTasks, Tasks: []string{"t1"}}}}})
	if err != nil || !b.Tasks[1].Wait.Since.Equal(since) {
		t.Fatalf("since moved: %v %+v", err, b.Tasks[1].Wait)
	}
	// Going on clears it.
	b, err = s.ChangeBoard("alice", BoardChange{Update: []TaskEdit{{ID: "t2", Status: sp("doing")}}})
	if err != nil || b.Tasks[1].Wait != nil {
		t.Fatalf("doing kept its wait: %v %+v", err, b.Tasks[1])
	}
	// A plain blocked task keeps no wait: the legacy shape.
	b, err = s.ChangeBoard("alice", BoardChange{Update: []TaskEdit{{ID: "t2", Status: sp("blocked")}}})
	if err != nil || b.Tasks[1].Wait != nil || b.Tasks[1].Status != TaskBlocked {
		t.Fatalf("legacy blocked: %v %+v", err, b.Tasks[1])
	}
	for name, e := range map[string]TaskEdit{
		"unknown kind":     {ID: "t2", Wait: &WaitEdit{Kind: "soon"}},
		"missing task":     {ID: "t2", Wait: &WaitEdit{Kind: WaitTasks, Tasks: []string{"t9"}}},
		"itself":           {ID: "t2", Wait: &WaitEdit{Kind: WaitTasks, Tasks: []string{"t2"}}},
		"no tasks":         {ID: "t2", Wait: &WaitEdit{Kind: WaitTasks}},
		"external reason":  {ID: "t2", Wait: &WaitEdit{Kind: WaitExternal}},
		"paused reason":    {ID: "t2", Wait: &WaitEdit{Kind: WaitPaused}},
		"tasks for user":   {ID: "t2", Wait: &WaitEdit{Kind: WaitUser, Tasks: []string{"t1"}}},
		"waiting but done": {ID: "t2", Status: sp("done"), Wait: &WaitEdit{Kind: WaitUser}},
	} {
		if _, err := s.ChangeBoard("alice", BoardChange{Update: []TaskEdit{e}}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// A task waited on can be removed later: the lead hears of it.
	if _, err := s.ChangeBoard("alice", BoardChange{Update: []TaskEdit{{ID: "t2", Wait: &WaitEdit{Kind: WaitTasks, Tasks: []string{"t1"}}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeBoard("alice", BoardChange{Remove: []string{"t1"}}); err != nil {
		t.Fatalf("removing a task waited on: %v", err)
	}
	if _, err := s.ChangeBoard("alice", BoardChange{Update: []TaskEdit{{ID: "t2", Note: sp("later change")}}}); err != nil {
		t.Fatalf("an unrelated change after the removal: %v", err)
	}
}

func TestOnlyTheLeadPauses(t *testing.T) {
	s := waitStore(t)
	pause := &WaitEdit{Kind: WaitPaused, Reason: "the user asked"}
	if _, err := s.ChangeBoardBy("alice", BoardChange{Add: []TaskEdit{{Title: sp("Fix"), Wait: pause}}}, false); !errors.Is(err, ErrPauseByLead) {
		t.Fatalf("a worker paused a task: %v", err)
	}
	if _, err := s.ChangeBoard("alice", BoardChange{Add: []TaskEdit{{Title: sp("Fix"), Owner: sp("w1"), Wait: pause}}}); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]BoardChange{
		"lift":   {Update: []TaskEdit{{ID: "t1", Wait: &WaitEdit{}}}},
		"resume": {Update: []TaskEdit{{ID: "t1", Status: sp("doing")}}},
		"owner":  {Update: []TaskEdit{{ID: "t1", Owner: sp("w2")}}},
		"remove": {Remove: []string{"t1"}},
	} {
		if _, err := s.ChangeBoardBy("alice", c, false); !errors.Is(err, ErrPauseByLead) {
			t.Errorf("a worker could %s a paused task: %v", name, err)
		}
	}
	if _, err := s.ChangeBoardBy("alice", BoardChange{Update: []TaskEdit{{ID: "t1", Note: sp("progress so far")}}}, false); err != nil {
		t.Fatalf("a worker could not note a paused task: %v", err)
	}
	b, err := s.ChangeBoard("alice", BoardChange{Update: []TaskEdit{{ID: "t1", Status: sp("doing")}}})
	if err != nil || b.Tasks[0].Paused() {
		t.Fatalf("the lead could not lift the pause: %v %+v", err, b.Tasks[0])
	}
}

// Files of earlier versions read unchanged; their blocked tasks have no
// wait and keep their status.
func TestLegacyBoardAndWorkers(t *testing.T) {
	s := waitStore(t)
	y, _ := s.Get("alice")
	legacy := `{"tasks":[{"id":"t1","title":"Old","status":"blocked","owner":"w1","note":"waits for 0.1.37","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}],"next":1}`
	if err := os.WriteFile(y.BoardFile(), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(y.MetaDir(), "workers.json"), []byte(`[{"name":"w1","task":"x","startedAt":"2026-01-01T00:00:00Z"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := s.Board("alice")
	if err != nil || b.Tasks[0].Wait != nil || b.Tasks[0].Status != TaskBlocked {
		t.Fatalf("legacy board: %v %+v", err, b)
	}
	ws, err := s.Workers("alice")
	if err != nil || !ws[0].PausedAt.IsZero() || !ws[0].FailedAt.IsZero() {
		t.Fatalf("legacy workers: %v %+v", err, ws)
	}
	data, _ := json.Marshal(ws[0])
	if string(data) != `{"name":"w1","number":1,"task":"x","startedAt":"2026-01-01T00:00:00Z"}` {
		t.Fatalf("new fields are written for an old worker: %s", data)
	}
	// Pausing records only for the generation named.
	if err := s.PauseWorker("alice", "w1", time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if ws, _ := s.Workers("alice"); !ws[0].PausedAt.IsZero() {
		t.Fatal("a pause of another generation reached the worker")
	}
	if err := s.PauseWorker("alice", "w1", ws[0].StartedAt, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.FailWorker("alice", "w1", ws[0].StartedAt, time.Now()); err != nil {
		t.Fatal(err)
	}
	if reopened, err := s.ReopenWorker("alice", "w1"); err != nil || reopened {
		t.Fatalf("reopen: %v %v", reopened, err)
	}
	if ws, _ := s.Workers("alice"); !ws[0].PausedAt.IsZero() || !ws[0].FailedAt.IsZero() {
		t.Fatal("giving the worker work did not lift its pause and failure")
	}
}

func TestContinuationRecordIsPerHire(t *testing.T) {
	s := waitStore(t)
	y, _ := s.Get("alice")
	if _, err := s.UpdateContinuation("alice", y.HiredAt, func(c *Continuation) bool {
		c.Attempts = append(c.Attempts, time.Now())
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateContinuation("alice", y.HiredAt.Add(-time.Hour), func(*Continuation) bool { return true }); !errors.Is(err, ErrRehired) {
		t.Fatalf("an earlier hire's change: %v", err)
	}
	if err := os.WriteFile(y.ContinuationFile(), []byte(`{"hiredAt":"2000-01-01T00:00:00Z","attempts":["2026-01-01T00:00:00Z"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, err := s.Continuation("alice"); err != nil || len(c.Attempts) != 0 || !c.HiredAt.Equal(y.HiredAt) {
		t.Fatalf("another hire's record was read: %v %+v", err, c)
	}
}
