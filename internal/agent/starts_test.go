package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/idy/yesman/internal/usage"
	"github.com/idy/yesman/internal/yesmen"
)

// Starting a worker through the manager, when the starts kept are full,
// drops the oldest and says so: a legacy task the dropped worker may have
// done does not go to the worker that held its name after it.
func TestManagerStartNotesDroppedStarts(t *testing.T) {
	m, store := newTestManager(t, 0)
	y, err := store.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).UTC()
	starts := []yesmen.WorkerStart{{Name: "w", StartedAt: old}, {Name: "w", StartedAt: old.Add(time.Hour)}}
	for i := len(starts); i < 10000; i++ {
		starts = append(starts, yesmen.WorkerStart{Name: "filler", StartedAt: old.Add(2*time.Hour + time.Duration(i)*time.Millisecond)})
	}
	raw, _ := json.Marshal(starts)
	if err := os.WriteFile(filepath.Join(y.MetaDir(), "worker-starts.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := m.StartWorker(context.Background(), "alice", "next", "fixture", "synthetic task"); err != nil {
		t.Fatal(err)
	}
	waitFinished(t, m, ID("alice", "next"), started)
	h, err := store.StartHistory("alice")
	if err != nil || len(h.Starts) != 10000 || !h.Forgotten.Equal(old) || h.Starts[0].StartedAt.Equal(old) || h.Starts[len(h.Starts)-1].Name != "next" {
		t.Fatalf("history: %d starts, forgotten %v, %v", len(h.Starts), h.Forgotten, err)
	}
	ws, err := store.Workers("alice")
	if err != nil {
		t.Fatal(err)
	}
	board := yesmen.Board{Tasks: []yesmen.Task{{ID: "t1", Status: yesmen.TaskDone, Owner: "w", CreatedAt: old.Add(time.Minute), UpdatedAt: old.Add(time.Minute)}}}
	later := []usage.Record{{Kind: usage.KindResponse, Provider: "p", Model: "new", Effort: "low", At: old.Add(time.Hour + time.Minute), Ended: old.Add(time.Hour + 2*time.Minute), DurationMs: 60000, Stop: "stop"}}
	space := usage.Workspace{Name: "alice", HiredAt: y.HiredAt, Workers: ws, Starts: h.Starts, StartsCut: h.Forgotten, Board: board, Agents: map[string][]usage.Record{"w": later}}
	if s := usage.Summarize([]usage.Workspace{space}, usage.Query{Workers: true}); s.Tasks.Attributed != 0 || s.Tasks.ReusedName != 1 || len(s.Coverage.StartsCut) != 1 {
		t.Fatalf("summary %+v %+v", s.Tasks, s.Coverage.StartsCut)
	}
}
