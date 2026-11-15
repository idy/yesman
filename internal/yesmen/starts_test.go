package yesmen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fullStarts writes a list of maxStarts starts, as a build that dropped
// starts without saying so did: w's at t0, others' an hour on and later.
func fullStarts(t *testing.T, y Yesman, t0 time.Time) {
	t.Helper()
	starts := []WorkerStart{{Name: "w", StartedAt: t0}}
	for i := 1; i < maxStarts; i++ {
		starts = append(starts, WorkerStart{Name: "filler", StartedAt: t0.Add(time.Hour + time.Duration(i)*time.Second)})
	}
	if err := writeJSON(y.startsFile(), starts); err != nil {
		t.Fatal(err)
	}
}

// Starts dropped to keep the history bounded are noted, and never
// forgotten again: once written, after a restart, and as more are dropped.
func TestStartHistoryNotesDroppedStarts(t *testing.T) {
	tmp := t.TempDir()
	s := New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	y, err := s.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	history := func(s *Store) StartHistory {
		t.Helper()
		h, err := s.StartHistory("alice")
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	// Below the bound, the history is complete and stays a list.
	if err := s.AddWorker("alice", Worker{Name: "a", Task: "x", StartedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if h := history(s); len(h.Starts) != 1 || !h.Forgotten.IsZero() {
		t.Fatalf("history below the bound = %+v", h)
	}
	if data, _ := os.ReadFile(y.startsFile()); !strings.HasPrefix(strings.TrimSpace(string(data)), "[") {
		t.Fatalf("complete history not a list: %s", data)
	}

	// A full list may have dropped starts before its oldest, unsaid.
	fullStarts(t, y, t0)
	if h := history(s); len(h.Starts) != maxStarts || !h.Forgotten.Equal(t0) {
		t.Fatalf("full list forgotten = %v, %d starts", h.Forgotten, len(h.Starts))
	}

	// Dropping a start says so, from then on.
	next := t0.Add(48 * time.Hour)
	if err := s.AddWorker("alice", Worker{Name: "next", Task: "x", StartedAt: next}); err != nil {
		t.Fatal(err)
	}
	h := history(s)
	if len(h.Starts) != maxStarts || !h.Forgotten.Equal(t0) || h.Starts[0].Name != "filler" || !h.Starts[len(h.Starts)-1].StartedAt.Equal(next) {
		t.Fatalf("after a drop: forgotten %v, %d starts, first %+v", h.Forgotten, len(h.Starts), h.Starts[0])
	}
	var file map[string]json.RawMessage
	if err := readJSON(y.startsFile(), &file); err != nil || file["forgotten"] == nil {
		t.Fatalf("drop not written: %v %v", file, err)
	}

	// Other fields are kept; each drop moves Forgotten on.
	file["future"] = json.RawMessage(`{"kept":true}`)
	if err := writeJSON(y.startsFile(), file); err != nil {
		t.Fatal(err)
	}
	dropped := h.Starts[0].StartedAt
	if err := s.AddWorker("alice", Worker{Name: "later", Task: "x", StartedAt: next.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if h := history(s); !h.Forgotten.Equal(dropped) || len(h.Starts) != maxStarts {
		t.Fatalf("second drop: forgotten %v want %v", h.Forgotten, dropped)
	}
	file = nil
	var kept struct{ Kept bool }
	if err := readJSON(y.startsFile(), &file); err != nil || json.Unmarshal(file["future"], &kept) != nil || !kept.Kept {
		t.Fatalf("other field lost: %s %v", file["future"], err)
	}

	// A start older than those kept is dropped at once, and Forgotten does
	// not go back for it.
	moved := history(s).Forgotten
	if err := s.RemoveWorker("alice", "a"); err != nil { // a started at t0
		t.Fatal(err)
	}
	if h := history(s); !h.Forgotten.Equal(moved) || h.Starts[0].StartedAt.Before(moved) {
		t.Fatalf("old start: forgotten %v want %v; first %v", h.Forgotten, moved, h.Starts[0].StartedAt)
	}

	// A restart reads the same.
	again := New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	if a, b := history(again), history(s); !a.Forgotten.Equal(b.Forgotten) || len(a.Starts) != len(b.Starts) {
		t.Fatalf("after a restart %v/%d, before %v/%d", a.Forgotten, len(a.Starts), b.Forgotten, len(b.Starts))
	}

	// A history that is not what yesman writes is left alone.
	for _, bad := range []string{`{"starts": 5}`, `{"forgotten": "soon"}`, `null`, `"x"`} {
		if err := os.WriteFile(y.startsFile(), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.StartHistory("alice"); err == nil {
			t.Errorf("%s read without an error", bad)
		}
		if err := s.AddWorker("alice", Worker{Name: "x" + string(rune('a'+len(bad))), Task: "x", StartedAt: next.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(y.startsFile()); string(data) != bad {
			t.Errorf("%s replaced by %s", bad, data)
		}
	}
}
