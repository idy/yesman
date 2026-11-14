package yesmen

import (
	"bytes"
	"encoding/json"
	"errors"
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

// external is a valid handoffs file outside the workspace, which no
// operation may read or change.
const external = `{"items":[{"id":"n7","type":"manual","title":"EXTERNAL SENTINEL","createdAt":"2026-01-01T00:00:00Z"}],"next":7}`

func externalFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(path, []byte(external), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links are not available here: %v", err)
	}
}

// The file is used only as a regular, singly linked file inside the
// workspace's own .yesman; anything else is reported and left alone.
func TestHandoffsFileBoundaries(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, y Yesman, outside string)
	}{
		{"link to an outside file", func(t *testing.T, y Yesman, outside string) {
			symlinkOrSkip(t, outside, y.HandoffsFile())
		}},
		{"hard link to an outside file", func(t *testing.T, y Yesman, outside string) {
			if err := os.Link(outside, y.HandoffsFile()); err != nil {
				t.Skipf("hard links are not available here: %v", err)
			}
		}},
		{".yesman replaced by a link", func(t *testing.T, y Yesman, _ string) {
			moved := y.MetaDir() + ".moved"
			if err := os.Rename(y.MetaDir(), moved); err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, moved, y.MetaDir())
		}},
		{"a directory in its place", func(t *testing.T, y Yesman, _ string) {
			if err := os.Mkdir(y.HandoffsFile(), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"too large", func(t *testing.T, y Yesman, _ string) {
			f, err := os.Create(y.HandoffsFile())
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := f.Truncate(maxHandoffsBytes + 1); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := handoffStore(t)
			y, _ := s.Get("alice")
			outside := externalFile(t)
			c.setup(t, y, outside)
			before, _ := os.Lstat(y.HandoffsFile())
			if hs, err := s.Handoffs("alice"); err == nil {
				t.Errorf("read through it: %+v", hs)
			}
			if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "must not be written"}); err == nil {
				t.Error("added through it")
			}
			if data, _ := os.ReadFile(outside); string(data) != external {
				t.Errorf("the outside file changed: %s", data)
			}
			after, err := os.Lstat(y.HandoffsFile())
			if before != nil && (err != nil || after.Mode().Type() != before.Mode().Type() || !os.SameFile(before, after)) {
				t.Errorf("what was in the file's place changed: %v", err)
			}
		})
	}
}

// A file at the old fixed temporary name, here a link to an outside file,
// is neither followed nor removed; each write uses a fresh temporary file
// and leaves none behind.
func TestHandoffsWriteUsesAFreshTemporaryFile(t *testing.T) {
	s := handoffStore(t)
	y, _ := s.Get("alice")
	outside := externalFile(t)
	symlinkOrSkip(t, outside, y.HandoffsFile()+".tmp")
	for range 3 {
		if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "step"}); err != nil {
			t.Fatal(err)
		}
	}
	if data, _ := os.ReadFile(outside); string(data) != external {
		t.Errorf("the outside file changed: %s", data)
	}
	if fi, err := os.Lstat(y.HandoffsFile() + ".tmp"); err != nil || fi.Mode().Type() != os.ModeSymlink {
		t.Errorf("the link at the old temporary name changed: %v", err)
	}
	entries, _ := os.ReadDir(y.MetaDir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".needs-you") {
			t.Errorf("a temporary file was left: %s", e.Name())
		}
	}
	if hs, _ := s.Handoffs("alice"); len(hs.Items) != 3 {
		t.Errorf("%d handoffs written", len(hs.Items))
	}
}

// A failed write, or a file replaced while a write was under way, leaves the
// file as it was and no temporary file.
func TestHandoffsFailedWriteKeepsTheFile(t *testing.T) {
	for _, step := range []string{"write", "rename", "replaced"} {
		t.Run(step, func(t *testing.T) {
			s := handoffStore(t)
			y, _ := s.Get("alice")
			if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "original"}); err != nil {
				t.Fatal(err)
			}
			kept, _ := os.ReadFile(y.HandoffsFile())
			handoffFault = func(at string) error {
				switch {
				case step == "replaced" && at == "write":
					// Another process swaps in a file of its own while the
					// new one is written; the replacement must not land.
					kept = []byte(external)
					tmp := y.HandoffsFile() + ".other"
					if err := os.WriteFile(tmp, kept, 0o644); err != nil {
						return err
					}
					return os.Rename(tmp, y.HandoffsFile())
				case at == step:
					return errors.New("injected failure")
				}
				return nil
			}
			t.Cleanup(func() { handoffFault = nil })
			if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "lost"}); err == nil {
				t.Fatal("the failed write succeeded")
			}
			if data, _ := os.ReadFile(y.HandoffsFile()); string(data) != string(kept) {
				t.Errorf("the file changed:\n%s", data)
			}
			entries, _ := os.ReadDir(y.MetaDir())
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".needs-you") {
					t.Errorf("a temporary file was left: %s", e.Name())
				}
			}
		})
	}
}

// Fields a later version wrote are kept, byte for byte, through adding,
// closing and reading again; the API never shows them.
func TestHandoffsUnknownFieldsKept(t *testing.T) {
	s := handoffStore(t)
	y, _ := s.Get("alice")
	raw := `{"items":[{"id":"n1","type":"manual","title":"old","createdAt":"2026-01-01T00:00:00Z","futureItem":{"n":9007199254740993,"s":"PRIVATE"}}],"next":1,"futureTop":[1e400,"keep"]}`
	if err := os.WriteFile(y.HandoffsFile(), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "next"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseHandoff("alice", "n1", HandoffDone, "user", ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(y.HandoffsFile())
	for _, want := range []string{`"futureItem": {`, `"n": 9007199254740993`, `"s": "PRIVATE"`, `"futureTop": [`, `1e400`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("lost %s:\n%s", want, data)
		}
	}
	fresh := New(filepath.Dir(y.Path), filepath.Join(filepath.Dir(filepath.Dir(y.Path)), "archive"))
	hs, err := fresh.Handoffs("alice")
	if err != nil {
		t.Fatal(err)
	}
	h, _ := hs.Find("n1")
	var item bytes.Buffer
	if err := json.Compact(&item, h.extra["futureItem"]); err != nil || h.Open() || item.String() != `{"n":9007199254740993,"s":"PRIVATE"}` {
		t.Errorf("read again: %+v %s %v", h, item.String(), err)
	}
	api, _ := json.Marshal(hs)
	if strings.Contains(string(api), "future") || strings.Contains(string(api), "PRIVATE") {
		t.Errorf("the API shows unknown fields: %s", api)
	}
}

// A file that is valid JSON but not a handoffs file is reported, and kept.
func TestHandoffsMalformedFileKept(t *testing.T) {
	for _, raw := range []string{
		`null`,
		`{}`,
		`[]`,
		`{"items":null,"next":0}`,
		`{"items":[],"next":-1}`,
		`{"items":[{"id":"n1"}],"next":1}`,
		`{"items":[{"id":"x1","type":"manual","title":"a","createdAt":"2026-01-01T00:00:00Z"}],"next":1}`,
		`{"items":[{"id":"n1","type":"chat","title":"a","createdAt":"2026-01-01T00:00:00Z"}],"next":1}`,
		`{"items":[{"id":"n1","type":"manual","title":"a","createdAt":"2026-01-01T00:00:00Z","resolution":"done"}],"next":1}`,
		`{"items":[{"id":"n1","type":"manual","title":"a","createdAt":"2026-01-01T00:00:00Z"},{"id":"n1","type":"manual","title":"b","createdAt":"2026-01-01T00:00:00Z"}],"next":1}`,
	} {
		t.Run(raw, func(t *testing.T) {
			s := handoffStore(t)
			y, _ := s.Get("alice")
			if err := os.WriteFile(y.HandoffsFile(), []byte(raw), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Handoffs("alice"); err == nil {
				t.Error("read")
			}
			if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "new"}); err == nil {
				t.Error("added")
			}
			if data, _ := os.ReadFile(y.HandoffsFile()); string(data) != raw {
				t.Errorf("rewritten: %s", data)
			}
		})
	}
}
