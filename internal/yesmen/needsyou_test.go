package yesmen

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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
			handoffFault = func(at, _ string) error {
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
		`{"items":[],"next":0,"forgottenCalls":null}`,
		`{"items":[],"next":0,"forgottenCalls":{"places":0,"bits":"AA==","count":1}}`,
		`{"items":[],"next":0,"forgottenCalls":{"places":7,"bits":"","count":1}}`,
		`{"items":[],"next":0,"callMemory":null}`,
		`{"items":[],"next":0,"callMemory":{"epoch":-1,"fingerprints":"","resets":[]}}`,
		`{"items":[],"next":0,"callMemory":{"epoch":0,"fingerprints":"AAAA","resets":[]}}`,
		`{"items":[],"next":0,"callMemory":{"epoch":0,"fingerprints":"AAAAAAAAAAEAAAAAAAAAAQ==","resets":[]}}`,
		`{"items":[],"next":0,"callMemory":{"epoch":0,"fingerprints":"AAAAAAAAAAIAAAAAAAAAAQ==","resets":[]}}`,
		`{"items":[],"next":0,"callMemory":{"epoch":0,"fingerprints":"","resets":[{"epoch":1,"at":"2026-01-01T00:00:00Z","cleared":0,"sha256":"` + strings.Repeat("0", 64) + `"}]}}`,
		`{"items":[],"next":0,"callMemory":{"epoch":1,"fingerprints":"","resets":[{"epoch":1,"at":"2026-01-01T00:00:00Z","cleared":0,"sha256":"00"}]}}`,
		`{"items":[],"next":0,"callMemory":{"epoch":0,"fingerprints":"` + base64.StdEncoding.EncodeToString(make([]byte, 8*(maxCallFingerprints+1))) + `","resets":[]}}`,
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
				t.Errorf("rewritten: %.200s", data)
			}
		})
	}
}

// The yesman's identity is read from its own yesman.json, a regular file of
// a bounded size: one that cannot be read, or is not that, is an error, and
// the handoffs file stays as it is.
func TestHandoffsIdentityIsReadSafely(t *testing.T) {
	for _, tc := range []struct {
		name, says string
		change     func(t *testing.T, meta string)
	}{
		{"the same yesman", "", func(*testing.T, string) {}},
		{"oversized", "larger than", func(t *testing.T, meta string) {
			data, _ := os.ReadFile(meta)
			padded := append(bytes.TrimSuffix(bytes.TrimSpace(data), []byte("}")), []byte(`,"pad":"`+strings.Repeat("x", maxMetaBytes)+`"}`)...)
			if err := os.WriteFile(meta, padded, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"not JSON", "yesman.json", func(t *testing.T, meta string) {
			if err := os.WriteFile(meta, []byte("{"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"a directory", "not a regular file", func(t *testing.T, meta string) {
			if err := os.Remove(meta); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(meta, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"a link to the same content", "not a regular file", func(t *testing.T, meta string) {
			copied := filepath.Join(t.TempDir(), "yesman.json")
			data, _ := os.ReadFile(meta)
			if err := os.WriteFile(copied, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(meta); err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, copied, meta)
		}},
		{"missing", "replaced", func(t *testing.T, meta string) {
			if err := os.Remove(meta); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := handoffStore(t)
			if _, err := s.Hire("bob"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "one"}); err != nil {
				t.Fatal(err)
			}
			y, _ := s.Get("alice")
			before, _ := os.ReadFile(y.HandoffsFile())
			tc.change(t, filepath.Join(y.MetaDir(), "yesman.json"))
			f, hs, err := openHandoffs(s.root, y, &s.metaMu)
			if f != nil {
				f.close()
			}
			switch {
			case tc.says == "" && (err != nil || len(hs.Items) != 1):
				t.Fatalf("%+v %v", hs, err)
			case tc.says != "" && (err == nil || !strings.Contains(err.Error(), tc.says)):
				t.Fatalf("opened: %v", err)
			}
			if tc.says != "" && tc.says != "replaced" && strings.Contains(err.Error(), "replaced") {
				t.Errorf("an unreadable identity taken for another yesman: %v", err)
			}
			if after, _ := os.ReadFile(y.HandoffsFile()); !bytes.Equal(before, after) {
				t.Error("the handoffs file changed")
			}
			if _, _, err := s.AddHandoff("bob", Handoff{Type: HandoffManual, Title: "bob's"}); err != nil {
				t.Errorf("bob: %v", err)
			}
		})
	}
}

// The history keeps the most recently closed handoffs, and a tool call that
// registered a handoff never registers another, even once it was trimmed
// from the history, and after a restart.
func TestHandoffsHistoryAndRepeatedCalls(t *testing.T) {
	s := handoffStore(t)
	y, _ := s.Get("alice")
	first, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "old open", ToolCall: "original-call"})
	if err != nil {
		t.Fatal(err)
	}
	closeNewer := func(n int, prefix string) {
		t.Helper()
		for i := range n {
			h, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "step", ToolCall: prefix + strconv.Itoa(i)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.CloseHandoff("alice", h.ID, HandoffDone, "user", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	closeNewer(keptClosed, "a")
	// Closed last, it is the most recent history, though created first.
	if _, err := s.CloseHandoff("alice", first.ID, HandoffDone, "user", ""); err != nil {
		t.Fatal(err)
	}
	hs, _ := s.Handoffs("alice")
	if h, ok := hs.Find(first.ID); !ok || h.Open() || len(hs.Items) != keptClosed {
		t.Fatalf("kept %d, %s: %+v %v", len(hs.Items), first.ID, h, ok)
	}
	if h, created, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "old open", ToolCall: "original-call"}); err != nil || created || h.ID != first.ID {
		t.Fatalf("repeat while kept = %+v %v %v", h, created, err)
	}
	// Now trimmed: a repeat is refused rather than registered again.
	closeNewer(keptClosed, "b")
	hs, _ = s.Handoffs("alice")
	if _, ok := hs.Find(first.ID); ok {
		t.Fatalf("%s still kept", first.ID)
	}
	for _, store := range []*Store{s, New(filepath.Dir(y.Path), filepath.Join(t.TempDir(), "archive"))} {
		h, created, err := store.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "old open", ToolCall: "original-call"})
		if err == nil || created || !strings.Contains(err.Error(), first.ID) {
			t.Fatalf("repeat after trimming = %+v %v %v", h, created, err)
		}
		if _, _, err := store.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "step", ToolCall: "a3"}); err == nil {
			t.Fatal("an older trimmed call registered again")
		}
		hs, _ := store.Handoffs("alice")
		if len(hs.Open()) != 0 || len(hs.Items) != keptClosed {
			t.Fatalf("after repeats: %d open of %d", len(hs.Open()), len(hs.Items))
		}
	}
	// A new call still registers.
	if h, created, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "new", ToolCall: "new-call"}); err != nil || !created {
		t.Fatalf("new call = %+v %v %v", h, created, err)
	}
}

// Past keptCalls the oldest calls are kept only as fingerprints, which
// still refuse them, in a new Store as in any process, through every later
// trimming; new calls register.
func TestHandoffsForgottenCalls(t *testing.T) {
	s := handoffStore(t)
	y, _ := s.Get("alice")
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	closed := func(from, to int) []Handoff {
		var items []Handoff
		for i := from; i < to; i++ {
			at := base.Add(time.Duration(i) * time.Minute)
			items = append(items, Handoff{ID: "n" + strconv.Itoa(i+1), Type: HandoffManual, Title: "x", ToolCall: "c" + strconv.Itoa(i), CreatedAt: at, ClosedAt: at.Add(time.Second), Resolution: HandoffDone, ClosedBy: "user"})
		}
		return items
	}
	// The history as the app leaves it, one open handoff to close last.
	n := keptCalls + keptClosed + 2
	items := append(closed(0, n), Handoff{ID: "n" + strconv.Itoa(n+1), Type: HandoffManual, Title: "last", ToolCall: "last", CreatedAt: base.Add(48 * time.Hour)})
	raw, err := encodeHandoffs(Handoffs{Items: items, Next: n + 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(y.HandoffsFile(), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// A real close trims it: c0, c1 and c2 are forgotten, c3 is the oldest
	// call kept as it is.
	if _, err := s.CloseHandoff("alice", "n"+strconv.Itoa(n+1), HandoffDone, "user", ""); err != nil {
		t.Fatal(err)
	}
	fresh := func() *Store { return New(filepath.Dir(y.Path), filepath.Join(t.TempDir(), "archive")) }
	read, err := fresh().Handoffs("alice")
	if err != nil || len(read.Items) != keptClosed || len(read.trimmed) != keptCalls || read.trimmed[0].ToolCall != "c3" || len(read.memory.Set) != 3 {
		t.Fatalf("read back %d items, %d calls, %d fingerprints, %v", len(read.Items), len(read.trimmed), len(read.memory.Set), err)
	}
	if m := read.CallMemory(); m.Used != keptClosed+keptCalls+3 || m.Capacity != maxCallRecords || m.State != "" || m.Forgettable != 3 {
		t.Fatalf("call memory %+v", m)
	}
	refused := func(store *Store, call, says string) {
		t.Helper()
		before, _ := os.ReadFile(y.HandoffsFile())
		h, created, err := store.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "again", ToolCall: call})
		if err == nil || created || !strings.Contains(err.Error(), says) {
			t.Errorf("%s registered again: %+v %v %v", call, h, created, err)
		}
		if after, _ := os.ReadFile(y.HandoffsFile()); !bytes.Equal(before, after) {
			t.Errorf("refusing %s changed the file", call)
		}
	}
	for _, store := range []*Store{s, fresh()} {
		refused(store, "c0", "long ago")
		refused(store, "c2", "long ago") // the latest forgotten
		refused(store, "c3", "n4")       // the oldest kept, with its handoff
	}
	// Trimmed again and again, from new Stores: what was forgotten stays
	// refused, and so does what is forgotten now.
	for round := range 3 {
		store := fresh()
		for i := range 10 {
			h, created, err := store.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "new", ToolCall: "r" + strconv.Itoa(round) + "-" + strconv.Itoa(i)})
			if err != nil || !created {
				t.Fatalf("a new call: %v %v", created, err)
			}
			if _, err := store.CloseHandoff("alice", h.ID, HandoffDone, "user", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	read, _ = fresh().Handoffs("alice")
	if len(read.memory.Set) != 33 || read.trimmed[0].ToolCall != "c33" {
		t.Fatalf("after trimming again: %d forgotten, oldest kept %s", len(read.memory.Set), read.trimmed[0].ToolCall)
	}
	for _, call := range []string{"c0", "c2", "c3", "c32"} {
		refused(fresh(), call, "long ago")
	}
	refused(fresh(), "c33", "n34")
	// A new call registers, once.
	first, created, err := fresh().AddHandoff("alice", Handoff{Type: HandoffManual, Title: "new", ToolCall: "toolu_new"})
	if err != nil || !created {
		t.Fatalf("a new call: %v %v", created, err)
	}
	if again, created, err := fresh().AddHandoff("alice", Handoff{Type: HandoffManual, Title: "new", ToolCall: "toolu_new"}); err != nil || created || again.ID != first.ID {
		t.Fatalf("the same call again: %+v %v %v", again, created, err)
	}
	// Without a tool call there is nothing to repeat.
	if _, created, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "by hand"}); err != nil || !created {
		t.Fatalf("without a call: %v %v", created, err)
	}
}

// A file of a pre-release version, which forgot calls without keeping their
// fingerprint, keeps refusing calls it does not know, rather than taking a
// forgotten one for new; what it knows still works.
func TestHandoffsUnsureFileRefusesUnknownCalls(t *testing.T) {
	s := handoffStore(t)
	y, _ := s.Get("alice")
	raw := `{"items":[{"id":"n9","type":"manual","title":"open","toolCall":"known","createdAt":"2026-01-02T00:00:00Z"}],"next":9,` +
		`"trimmedCalls":[{"toolCall":"kept","id":"n8","createdAt":"2026-01-01T00:00:00Z"}],"trimmedCallsBefore":"2025-12-31T00:00:00Z"}`
	if err := os.WriteFile(y.HandoffsFile(), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"call-0", "toolu_new"} {
		if _, created, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: call}); err == nil || created || !strings.Contains(err.Error(), "pre-release") {
			t.Errorf("%s: %v %v", call, created, err)
		}
	}
	if h, created, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "known"}); err != nil || created || h.ID != "n9" {
		t.Errorf("known call: %+v %v %v", h, created, err)
	}
	if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "kept"}); err == nil || !strings.Contains(err.Error(), "n8") {
		t.Errorf("kept call: %v", err)
	}
	if _, err := s.CloseHandoff("alice", "n9", HandoffDone, "user", ""); err != nil {
		t.Fatal(err)
	}
	fresh := New(filepath.Dir(y.Path), filepath.Join(t.TempDir(), "archive"))
	if _, _, err := fresh.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "call-0"}); err == nil {
		t.Error("the mark was lost in writing")
	}
	if _, created, err := fresh.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "by hand"}); err != nil || !created {
		t.Errorf("without a call: %v %v", created, err)
	}
	// Until the user starts a new tool-call memory, which forgets only the
	// mark: what is kept stays, and stays refused or found.
	hs, err := fresh.Handoffs("alice")
	if err != nil {
		t.Fatal(err)
	}
	m := hs.CallMemory()
	if m.State != "blocked" || !m.Refusing() || !m.Resettable() {
		t.Fatalf("call memory %+v", m)
	}
	if _, _, err := fresh.ResetCallMemory("alice", y.HiredAt, m.Epoch, m.Used, m.Forgettable); err != nil {
		t.Fatal(err)
	}
	if h, created, err := fresh.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "call-0"}); err != nil || !created {
		t.Errorf("a call after the reset: %+v %v %v", h, created, err)
	}
	if h, created, err := fresh.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "known"}); err != nil || created || h.ID != "n9" {
		t.Errorf("known call after the reset: %+v %v %v", h, created, err)
	}
	if _, _, err := fresh.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "kept"}); err == nil || !strings.Contains(err.Error(), "n8") {
		t.Errorf("kept call after the reset: %v", err)
	}
	if raw, _ := os.ReadFile(y.HandoffsFile()); bytes.Contains(raw, []byte(unsureKey)) {
		t.Error("the mark is still in the file")
	}
}

// The workspace must be a directory in the Store's folder, of the yesman
// asked for. The folder itself may be reached through a link.
func TestHandoffsWorkspaceIsTheStores(t *testing.T) {
	t.Run("a link in the workspace's place", func(t *testing.T) {
		for _, target := range []string{"moved", "bob"} {
			t.Run(target, func(t *testing.T) {
				s := handoffStore(t)
				if _, err := s.Hire("bob"); err != nil {
					t.Fatal(err)
				}
				alice, _ := s.Get("alice")
				bob, _ := s.Get("bob")
				if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "alice's"}); err != nil {
					t.Fatal(err)
				}
				if _, _, err := s.AddHandoff("bob", Handoff{Type: HandoffManual, Title: "bob's"}); err != nil {
					t.Fatal(err)
				}
				dest := bob.Path
				if target == "moved" {
					dest = filepath.Join(t.TempDir(), "moved")
					if err := os.Rename(alice.Path, dest); err != nil {
						t.Fatal(err)
					}
				} else if err := os.RemoveAll(alice.Path); err != nil {
					t.Fatal(err)
				}
				symlinkOrSkip(t, dest, alice.Path)
				file := filepath.Join(dest, ".yesman", handoffsName)
				before, _ := os.ReadFile(file)
				if hs, err := s.Handoffs("alice"); err == nil {
					t.Errorf("read through the link: %+v", hs)
				}
				if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "through the link"}); err == nil {
					t.Error("added through the link")
				}
				if _, err := s.CloseHandoff("alice", "n1", HandoffDone, "user", ""); err == nil {
					t.Error("closed through the link")
				}
				if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
					t.Errorf("the file behind the link changed:\n%s", after)
				}
			})
		}
	})
	t.Run("replaced by a newer yesman", func(t *testing.T) {
		s := handoffStore(t)
		old, _ := s.Get("alice")
		if _, err := s.Archive("alice", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Hire("alice"); err != nil {
			t.Fatal(err)
		}
		if newer, _ := s.Get("alice"); newer.HiredAt.Equal(old.HiredAt) {
			t.Skip("hired at the same instant")
		}
		// What Store.Get said before the workspace was replaced.
		if f, _, err := openHandoffs(s.root, old, &s.metaMu); err == nil {
			f.close()
			t.Fatal("opened the newer yesman's workspace for the older")
		}
		if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "new"}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("the folder through a link", func(t *testing.T) {
		tmp := t.TempDir()
		dir := filepath.Join(tmp, "real")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		linked := filepath.Join(tmp, "linked")
		symlinkOrSkip(t, dir, linked)
		s := New(linked, filepath.Join(tmp, "archive"))
		if _, err := s.Hire("alice"); err != nil {
			t.Fatal(err)
		}
		h, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "step"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CloseHandoff("alice", h.ID, HandoffDone, "user", ""); err != nil {
			t.Fatal(err)
		}
		if hs, err := s.Handoffs("alice"); err != nil || len(hs.Items) != 1 {
			t.Fatalf("%+v %v", hs, err)
		}
	})
}

// Someone who edits the file in place after it was read keeps their edit:
// adding or closing then fails, removes its temporary file and works once
// tried again, on the edited file.
func TestHandoffsEditedWhileWritingIsKept(t *testing.T) {
	for _, op := range []string{"add", "close"} {
		t.Run(op, func(t *testing.T) {
			s := handoffStore(t)
			y, _ := s.Get("alice")
			raw := `{"items":[{"id":"n1","type":"manual","title":"original","createdAt":"2026-01-01T00:00:00Z","future":9007199254740993}],"next":1}`
			if err := os.WriteFile(y.HandoffsFile(), []byte(raw), 0o644); err != nil {
				t.Fatal(err)
			}
			// The same size, so that only the bytes tell.
			edited := []byte(strings.Replace(raw, `"original"`, `"ORIGINAL"`, 1))
			handoffFault = func(step, path string) error {
				if step != "write" {
					return nil
				}
				f, err := os.OpenFile(path, os.O_WRONLY, 0)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = f.WriteAt(edited, 0)
				return err
			}
			t.Cleanup(func() { handoffFault = nil })
			change := func() error {
				if op == "add" {
					_, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "next"})
					return err
				}
				_, err := s.CloseHandoff("alice", "n1", HandoffDone, "user", "")
				return err
			}
			if err := change(); err == nil || !strings.Contains(err.Error(), "changed after it was read") {
				t.Fatalf("the change went through: %v", err)
			}
			if data, _ := os.ReadFile(y.HandoffsFile()); !bytes.Equal(data, edited) {
				t.Errorf("the edit was lost:\n%s", data)
			}
			entries, _ := os.ReadDir(y.MetaDir())
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".needs-you") {
					t.Errorf("a temporary file was left: %s", e.Name())
				}
			}
			handoffFault = nil
			if err := change(); err != nil {
				t.Fatal(err)
			}
			hs, err := s.Handoffs("alice")
			h, _ := hs.Find("n1")
			data, _ := os.ReadFile(y.HandoffsFile())
			if err != nil || h.Title != "ORIGINAL" || !bytes.Contains(data, []byte("9007199254740993")) {
				t.Fatalf("after trying again: %+v %v\n%s", h, err, data)
			}
		})
	}
}

// A file that is slow to read holds up its own yesman only: another
// yesman's handoffs, and the Store, go on.
func TestHandoffsSlowFileHoldsUpOnlyItsYesman(t *testing.T) {
	s := handoffStore(t)
	if _, err := s.Hire("bob"); err != nil {
		t.Fatal(err)
	}
	alice, _ := s.Get("alice")
	if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "a"}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	handoffFault = func(step, path string) error {
		if step == "read" && path == alice.HandoffsFile() {
			once.Do(func() { close(entered) })
			<-release
		}
		return nil
	}
	t.Cleanup(func() { handoffFault = nil })
	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := s.Handoffs("alice"); err != nil {
			t.Error(err)
		}
	})
	<-entered
	sameYesman := make(chan error, 1)
	wg.Go(func() {
		_, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "b"})
		sameYesman <- err
	})
	others := make(chan error, 1)
	go func() {
		_, _, err := s.AddHandoff("bob", Handoff{Type: HandoffManual, Title: "c"})
		if err == nil {
			_, err = s.Handoffs("bob")
		}
		if err == nil {
			_, err = s.Board("alice")
		}
		others <- err
	}()
	select {
	case err := <-others:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Error("another yesman waited on alice's file")
	}
	select {
	case err := <-sameYesman:
		t.Errorf("alice's change did not wait for her read: %v", err)
		sameYesman <- nil
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if err := <-sameYesman; err != nil {
		t.Error(err)
	}
	if hs, _ := s.Handoffs("alice"); len(hs.Open()) != 2 {
		t.Errorf("alice has %d open", len(hs.Open()))
	}
}

// A change that would make the file too large to read is refused, and the
// file, still readable, is kept.
func TestHandoffsNeverGrowPastTheReadLimit(t *testing.T) {
	s := handoffStore(t)
	y, _ := s.Get("alice")
	head := `{"items":[{"id":"n1","type":"manual","title":"old","createdAt":"2026-01-01T00:00:00Z"}],"next":1,"futureTop":"`
	tail := `"}`
	pad := maxHandoffsBytes - len(head) - len(tail) - 2000
	raw := head + strings.Repeat("x", pad) + tail
	if err := os.WriteFile(y.HandoffsFile(), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Handoffs("alice"); err != nil {
		t.Fatalf("the file near the limit is not readable: %v", err)
	}
	_, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "big", Detail: strings.Repeat("y", maxHandoffDetail)})
	if err == nil || !strings.Contains(err.Error(), "grow past") {
		t.Fatalf("added past the limit: %v", err)
	}
	if data, _ := os.ReadFile(y.HandoffsFile()); string(data) != raw {
		t.Error("the file changed")
	}
	if hs, err := s.Handoffs("alice"); err != nil || len(hs.Items) != 1 {
		t.Fatalf("read after the refusal: %d items, %v", len(hs.Items), err)
	}
	// A small change still fits.
	if _, err := s.CloseHandoff("alice", "n1", HandoffDone, "user", ""); err != nil {
		t.Fatal(err)
	}
}
