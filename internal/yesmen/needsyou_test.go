package yesmen

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func handoffStore(t *testing.T) *Store {
	t.Helper()
	tmp := t.TempDir()
	s := New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	if _, err := s.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHandoffsLifecycle(t *testing.T) {
	s := handoffStore(t)
	if hs, err := s.Handoffs("alice"); err != nil || hs.Items == nil || len(hs.Items) != 0 {
		t.Fatalf("new list = %+v, %v", hs, err)
	}
	if _, err := s.ChangeBoard("alice", BoardChange{Add: []TaskEdit{{Title: ptr("Publish")}}}); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := s.AddWorker("alice", Worker{Name: "w", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Handoff{
		{Type: "chat", Title: "x"},
		{Type: HandoffLogin, Title: "   "},
		{Type: HandoffLogin, Title: strings.Repeat("x", maxHandoffTitle+1)},
		{Type: HandoffLogin, Title: "x", Task: "t9"},
		{Type: HandoffLogin, Title: "x", Worker: "nobody"},
	} {
		if _, _, err := s.AddHandoff("alice", bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	h, created, err := s.AddHandoff("alice", Handoff{Type: HandoffLogin, Title: "  Log in to   the registry ", Detail: "Run `npm login`.", Task: "t1", Worker: "w", ToolCall: "call-1"})
	if err != nil || !created || h.ID != "n1" || h.Title != "Log in to the registry" || !h.WorkerSince.Equal(started) || h.CreatedAt.IsZero() || !h.Open() {
		t.Fatalf("add = %+v, %v", h, err)
	}
	// The same tool call again returns the same handoff.
	again, created, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "Something else", ToolCall: "call-1"})
	if err != nil || created || again.ID != "n1" || again.Type != HandoffLogin {
		t.Fatalf("repeated call = %+v, %v", again, err)
	}
	second, _, err := s.AddHandoff("alice", Handoff{Type: HandoffDecision, Title: "Pick a name"})
	if err != nil || second.ID != "n2" {
		t.Fatalf("second = %+v, %v", second, err)
	}
	closed, err := s.CloseHandoff("alice", "n1", HandoffDone, "user", "")
	if err != nil || closed.Open() || closed.Resolution != HandoffDone || closed.ClosedBy != "user" {
		t.Fatalf("close = %+v, %v", closed, err)
	}
	// A handoff is closed once; nothing reopens it.
	if _, err := s.CloseHandoff("alice", "n1", HandoffNotNeeded, "lead", ""); err == nil {
		t.Error("closed twice")
	}
	if _, err := s.CloseHandoff("alice", "n9", HandoffDone, "user", ""); err == nil {
		t.Error("closed an unknown handoff")
	}
	if _, err := s.CloseHandoff("alice", "n2", "maybe", "user", ""); err == nil {
		t.Error("accepted an unknown resolution")
	}
	// A closed handoff's call is not a way to make a new one either.
	if h, _, err := s.AddHandoff("alice", Handoff{Type: HandoffLogin, Title: "x", ToolCall: "call-1"}); err != nil || h.ID != "n1" || h.Open() {
		t.Fatalf("replayed call after close = %+v, %v", h, err)
	}
	hs, _ := s.Handoffs("alice")
	if open := hs.Open(); len(open) != 1 || open[0].ID != "n2" {
		t.Fatalf("open = %+v", open)
	}
	// IDs are not reused once the list is trimmed.
	third, _, _ := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "Plug in the device"})
	if third.ID != "n3" {
		t.Fatalf("third = %+v", third)
	}
}

func TestHandoffsBounds(t *testing.T) {
	s := handoffStore(t)
	for i := range maxOpenHandoffs {
		if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "step"}); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "one too many"}); err == nil {
		t.Fatal("accepted more open handoffs than the limit")
	}
	hs, _ := s.Handoffs("alice")
	for _, h := range hs.Items {
		if _, err := s.CloseHandoff("alice", h.ID, HandoffResolved, "lead", "settled"); err != nil {
			t.Fatal(err)
		}
	}
	hs, _ = s.Handoffs("alice")
	if len(hs.Items) != keptClosed || len(hs.Open()) != 0 {
		t.Fatalf("kept %d closed handoffs, %d open", len(hs.Items), len(hs.Open()))
	}
	if h, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "after"}); err != nil || h.ID != "n51" {
		t.Fatalf("after trimming = %+v, %v", h, err)
	}
}

// A damaged file is reported, never replaced.
func TestHandoffsCorruptFileIsKept(t *testing.T) {
	s := handoffStore(t)
	y, _ := s.Get("alice")
	if err := os.WriteFile(y.HandoffsFile(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Handoffs("alice"); err == nil {
		t.Error("read a damaged file")
	}
	if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x"}); err == nil {
		t.Error("added to a damaged file")
	}
	if data, _ := os.ReadFile(y.HandoffsFile()); string(data) != "{not json" {
		t.Errorf("the damaged file changed: %q", data)
	}
}

func TestHandoffsConcurrentAdds(t *testing.T) {
	s := handoffStore(t)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "step"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	hs, _ := s.Handoffs("alice")
	seen := map[string]bool{}
	for _, h := range hs.Items {
		if seen[h.ID] {
			t.Fatalf("ID %s given twice", h.ID)
		}
		seen[h.ID] = true
	}
	if len(seen) != 20 {
		t.Fatalf("%d handoffs", len(seen))
	}
}
