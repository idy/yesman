package yesmen

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fingerprintSet is the fingerprints of prefix0 … prefix(n-1), as the
// memory keeps them.
func fingerprintSet(t *testing.T, prefix string, n int) []uint64 {
	t.Helper()
	set := make([]uint64, n)
	for i := range n {
		set[i] = callFingerprint(prefix + strconv.Itoa(i))
	}
	slices.Sort(set)
	if set = slices.Compact(set); len(set) != n {
		t.Fatalf("only %d of %d fingerprints differ", len(set), n)
	}
	return set
}

// preReleaseFilter is a filter as the pre-release version kept, holding
// prefix0 … prefix(n-1): seven places in 32 KiB.
func preReleaseFilter(prefix string, n int) *callFilter {
	f := &callFilter{Places: 7, Bits: make([]byte, 1<<15), Count: n}
	for i := range n {
		sum := sha256.Sum256([]byte("yesman needs-you tool call\x00" + prefix + strconv.Itoa(i)))
		h1, h2 := binary.BigEndian.Uint64(sum[0:8]), binary.BigEndian.Uint64(sum[8:16])|1
		bits := uint64(len(f.Bits)) * 8
		for j := range uint64(f.Places) {
			p := (h1 + j*h2) % bits
			f.Bits[p/8] |= 1 << (p % 8)
		}
	}
	return f
}

func writeHandoffsFile(t *testing.T, y Yesman, hs Handoffs) []byte {
	t.Helper()
	raw, err := encodeHandoffs(hs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(y.HandoffsFile(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return raw
}

// A full memory holds every call it was given and, at 2^18 of 2^64
// fingerprints, refuses none of a million new ones here; the file the
// longest handoffs make with it stays under the read limit.
func TestCallMemoryAtCapacity(t *testing.T) {
	set := fingerprintSet(t, "held-", maxCallRecords)
	m := callMemory{Set: set}
	for i := range maxCallRecords {
		if !m.has("held-" + strconv.Itoa(i)) {
			t.Fatalf("lost held-%d", i)
		}
	}
	refused := 0
	for i := range 1_000_000 {
		if m.has("fresh-" + strconv.Itoa(i)) {
			refused++
		}
	}
	if refused != 0 {
		t.Errorf("%d of a million new calls refused", refused)
	}

	// The longest file this version writes: every field at its longest, in
	// characters JSON escapes to six bytes each.
	long := func(n int, i int) string {
		tag := strconv.Itoa(i)
		return strings.Repeat("<", n-len(tag)) + tag
	}
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	hs := Handoffs{Next: 2000, memory: callMemory{Epoch: keptResets, Set: set[:maxCallRecords-maxOpenHandoffs-keptClosed-keptCalls]}}
	for i := range maxOpenHandoffs + keptClosed {
		h := Handoff{ID: "n" + strconv.Itoa(i+1), Type: HandoffManual, Title: long(maxHandoffTitle, i), Detail: long(maxHandoffDetail, i),
			ToolCall: long(maxHandoffToolLen, i), CreatedAt: base.Add(time.Duration(i) * time.Minute)}
		if i < keptClosed {
			h.ClosedAt, h.Resolution, h.ClosedBy, h.Note = h.CreatedAt.Add(time.Second), HandoffDone, "user", long(maxHandoffNote, i)
		}
		hs.Items = append(hs.Items, h)
	}
	for i := range keptCalls {
		hs.trimmed = append(hs.trimmed, trimmedCall{ToolCall: long(maxHandoffToolLen, 1000+i), ID: "n" + strconv.Itoa(100+i), CreatedAt: base.Add(-time.Duration(i) * time.Minute)})
	}
	for i := range keptResets {
		hs.memory.Resets = append(hs.memory.Resets, callReset{Epoch: i + 1, At: base, Cleared: maxCallRecords, SHA256: strings.Repeat("0", 64)})
	}
	if c := hs.CallMemory(); c.Used != maxCallRecords || c.State != "full" {
		t.Fatalf("call memory %+v", c)
	}
	start := time.Now()
	raw, err := encodeHandoffs(hs)
	if err != nil {
		t.Fatal(err)
	}
	encoded := time.Since(start)
	t.Logf("the longest file: %d bytes of %d (encoded in %v)", len(raw), maxHandoffsBytes, encoded)
	if len(raw) > maxHandoffsBytes/2 {
		t.Errorf("the longest file takes %d bytes", len(raw))
	}
	start = time.Now()
	back, err := decodeHandoffs(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("decoded in %v", time.Since(start))
	if !slices.Equal(back.memory.Set, hs.memory.Set) || len(back.memory.Resets) != keptResets || back.memory.Epoch != keptResets || len(back.trimmed) != keptCalls || len(back.Items) != len(hs.Items) {
		t.Fatal("not read back as written")
	}
	// Closing an open handoff, from the file, through a Store.
	s := handoffStore(t)
	y, _ := s.Get("alice")
	if err := os.WriteFile(y.HandoffsFile(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseHandoff("alice", "n80", HandoffDone, "user", long(maxHandoffNote, 80)); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(y.HandoffsFile())
	t.Logf("after a close: %d bytes", len(after))
	if read, err := s.Handoffs("alice"); err != nil || read.CallMemory().Used != maxCallRecords {
		t.Fatalf("after a close: %v", err)
	}
}

// The memory never needs more room than it had: with every call taken up to
// the last one, closing all fifty open handoffs, a handoff without a call
// coming and going, and new Stores reading the file between, always work,
// keep every call remembered, and a new call is refused, saying why, with
// the file untouched. The user's reset then takes new calls again, keeping
// every handoff and the calls kept as they are.
func TestCallMemoryReserve(t *testing.T) {
	s := handoffStore(t)
	y, _ := s.Get("alice")
	fresh := func() *Store { return New(filepath.Dir(y.Path), filepath.Join(t.TempDir(), "archive")) }
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	hs := Handoffs{Next: 2000, extra: map[string]json.RawMessage{"future": json.RawMessage(`9007199254740993`)}}
	for i := range keptClosed {
		at := base.Add(time.Duration(i) * time.Minute)
		hs.Items = append(hs.Items, Handoff{ID: "n" + strconv.Itoa(i+1), Type: HandoffManual, Title: "closed", ToolCall: "closed-" + strconv.Itoa(i),
			CreatedAt: at, ClosedAt: at.Add(time.Second), Resolution: HandoffDone, ClosedBy: "user"})
	}
	for i := range maxOpenHandoffs - 1 {
		hs.Items = append(hs.Items, Handoff{ID: "n" + strconv.Itoa(31+i), Type: HandoffManual, Title: "open", ToolCall: "open-" + strconv.Itoa(i),
			CreatedAt: base.Add(time.Hour + time.Duration(i)*time.Minute)})
	}
	for i := range keptCalls {
		hs.trimmed = append(hs.trimmed, trimmedCall{ToolCall: "kept-" + strconv.Itoa(i), ID: "n" + strconv.Itoa(100+i), CreatedAt: base.Add(-time.Duration(keptCalls-i) * time.Minute)})
	}
	hs.memory.Set = fingerprintSet(t, "old-", maxCallRecords-1-len(hs.Items)-keptCalls)
	writeHandoffsFile(t, y, hs)

	memory := func(store *Store) CallMemory {
		t.Helper()
		hs, err := store.Handoffs("alice")
		if err != nil {
			t.Fatal(err)
		}
		if len(hs.trimmed) > keptCalls || len(hs.memory.Set) > maxCallRecords {
			t.Fatalf("%d calls kept, %d fingerprints", len(hs.trimmed), len(hs.memory.Set))
		}
		return hs.CallMemory()
	}
	if m := memory(fresh()); m.Used != maxCallRecords-1 || m.State != "near" {
		t.Fatalf("one short of full: %+v", m)
	}
	last, created, err := fresh().AddHandoff("alice", Handoff{Type: HandoffManual, Title: "the last", ToolCall: "fresh-last"})
	if err != nil || !created {
		t.Fatalf("the last call that fits: %v %v", created, err)
	}
	if m := memory(fresh()); m.Used != maxCallRecords || m.State != "full" || !m.Refusing() {
		t.Fatalf("full: %+v", m)
	}
	refusedFull := func(store *Store, call string) {
		t.Helper()
		before, _ := os.ReadFile(y.HandoffsFile())
		_, created, err := store.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "new", ToolCall: call})
		if err == nil || created || !strings.Contains(err.Error(), "memory is full (262144 of 262144") || strings.Contains(err.Error(), "call again") {
			t.Errorf("%s when full: %v %v", call, created, err)
		}
		if after, _ := os.ReadFile(y.HandoffsFile()); !bytes.Equal(before, after) {
			t.Errorf("refusing %s changed the file", call)
		}
	}
	refusedFull(s, "fresh-1")
	refusedFull(fresh(), "fresh-2")
	// What it remembers still answers, full or not.
	for call, id := range map[string]string{"open-3": "n34", "closed-0": "n1"} {
		if h, created, err := fresh().AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: call}); err != nil || created || h.ID != id {
			t.Errorf("%s again: %+v %v %v", call, h, created, err)
		}
	}
	for call, says := range map[string]string{"kept-5": "n105", "old-7": "long ago"} {
		if _, _, err := fresh().AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: call}); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%s again: %v", call, err)
		}
	}

	// Closing every open handoff only moves calls along.
	open, _ := s.Handoffs("alice")
	for i, h := range open.Open() {
		store := s
		if i%10 == 0 {
			store = fresh()
		}
		if _, err := store.CloseHandoff("alice", h.ID, HandoffDone, "user", ""); err != nil {
			t.Fatalf("closing %s, the %d-th: %v", h.ID, i+1, err)
		}
		if m := memory(s); m.Used != maxCallRecords {
			t.Fatalf("after closing %s: %+v", h.ID, m)
		}
	}
	if m := memory(fresh()); m.Used != maxCallRecords || len(open.Open()) != maxOpenHandoffs {
		t.Fatalf("after closing all: %+v", m)
	}
	// A handoff without a call comes and goes: it takes no room, and pushes
	// a call along without losing it.
	manual, created, err := fresh().AddHandoff("alice", Handoff{Type: HandoffManual, Title: "by hand"})
	if err != nil || !created {
		t.Fatalf("without a call when full: %v %v", created, err)
	}
	if _, err := fresh().CloseHandoff("alice", manual.ID, HandoffDone, "user", ""); err != nil {
		t.Fatal(err)
	}
	if m := memory(fresh()); m.Used != maxCallRecords {
		t.Fatalf("after one without a call: %+v", m)
	}
	refusedFull(fresh(), "fresh-3")
	if h, created, err := fresh().AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: last.ToolCall}); err != nil || created || h.ID != last.ID {
		t.Errorf("the last call again: %+v %v %v", h, created, err)
	}
	// kept-5 moved on to the fingerprints; kept-999 is still kept as it is.
	if _, _, err := fresh().AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "kept-5"}); err == nil || !strings.Contains(err.Error(), "long ago") {
		t.Errorf("kept-5 after closing: %v", err)
	}

	// The user's reset.
	before, _ := fresh().Handoffs("alice")
	m := before.CallMemory()
	reset, cleared, err := fresh().ResetCallMemory("alice", y.HiredAt, m.Epoch, m.Used, m.Forgettable)
	if err != nil || cleared != len(before.memory.Set) || reset.Epoch != 1 || reset.State != "" || reset.Used != m.Used-cleared {
		t.Fatalf("reset: %+v %d %v", reset, cleared, err)
	}
	after, _ := fresh().Handoffs("alice")
	if !slices.EqualFunc(after.Items, before.Items, func(a, b Handoff) bool {
		return a.ID == b.ID && a.ToolCall == b.ToolCall && a.ClosedAt.Equal(b.ClosedAt)
	}) ||
		!slices.EqualFunc(after.trimmed, before.trimmed, func(a, b trimmedCall) bool { return a.ToolCall == b.ToolCall && a.ID == b.ID }) ||
		string(after.extra["future"]) != "9007199254740993" || len(after.memory.Resets) != 1 || after.memory.Resets[0].Cleared != cleared {
		t.Fatal("the reset lost what it keeps")
	}
	if now, _ := s.Get("alice"); !now.HiredAt.Equal(y.HiredAt) || now.Path != y.Path {
		t.Fatal("the reset replaced the workspace")
	}
	if _, _, err := fresh().AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "kept-999"}); err == nil || !strings.Contains(err.Error(), "n1099") {
		t.Errorf("a kept call after the reset: %v", err)
	}
	// A forgotten call made again registers one handoff, then is the same.
	again, created, err := fresh().AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "old-7"})
	if err != nil || !created {
		t.Fatalf("a forgotten call after the reset: %v %v", created, err)
	}
	if same, created, err := fresh().AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "old-7"}); err != nil || created || same.ID != again.ID {
		t.Fatalf("the forgotten call once more: %+v %v %v", same, created, err)
	}
	if _, created, err := fresh().AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "fresh-1"}); err != nil || !created {
		t.Fatalf("a new call after the reset: %v %v", created, err)
	}
}

// A reset forgets only what the user was shown: a window of a yesman
// replaced since, a count changed since, a reset made since (even when as
// many calls came back), and a second of two at once forget nothing. Each
// reset is recorded, the last keptResets of them.
func TestResetCallMemoryConfirmation(t *testing.T) {
	s := handoffStore(t)
	y, _ := s.Get("alice")
	writeHandoffsFile(t, y, Handoffs{Items: []Handoff{}, memory: callMemory{Set: fingerprintSet(t, "old-", 100), extra: map[string]json.RawMessage{"later": json.RawMessage(`9007199254740993`)}}})
	unchanged := func(why string, err error, want error) {
		t.Helper()
		before, _ := os.ReadFile(y.HandoffsFile())
		if err == nil || (want != nil && !errors.Is(err, want)) {
			t.Errorf("%s: %v", why, err)
		}
		if after, _ := os.ReadFile(y.HandoffsFile()); !bytes.Equal(before, after) {
			t.Errorf("%s changed the file", why)
		}
	}
	_, _, err := s.ResetCallMemory("alice", y.HiredAt.Add(time.Second), 0, 100, 100)
	unchanged("another yesman's", err, nil)
	_, _, err = s.ResetCallMemory("alice", y.HiredAt, 0, 99, 100)
	unchanged("a changed count", err, ErrCallMemoryChanged)
	_, _, err = s.ResetCallMemory("alice", y.HiredAt, 0, 100, 99)
	unchanged("a changed number of fingerprints", err, ErrCallMemoryChanged)
	_, _, err = s.ResetCallMemory("alice", y.HiredAt, 1, 100, 100)
	unchanged("a later epoch", err, ErrCallMemoryChanged)

	// Two windows confirm at once: one reset.
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
		errs []error
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.ResetCallMemory("alice", y.HiredAt, 0, 100, 100)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else {
				errs = append(errs, err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 || len(errs) != 7 || !errors.Is(errs[0], ErrCallMemoryChanged) {
		t.Fatalf("%d resets, %v", wins, errs)
	}
	hs, _ := s.Handoffs("alice")
	if m := hs.CallMemory(); m.Epoch != 1 || m.Used != 0 || string(hs.memory.extra["later"]) != "9007199254740993" {
		t.Fatalf("after the reset: %+v", m)
	}
	r := hs.memory.Resets[0]
	sum := sha256.Sum256(append([]byte("fingerprints\x00"), (&callMemory{Set: fingerprintSet(t, "old-", 100)}).setBytes()...))
	if r.Epoch != 1 || r.Cleared != 100 || r.SHA256 != hex.EncodeToString(sum[:]) || r.At.IsZero() {
		t.Fatalf("recorded %+v", r)
	}
	// As many calls again, other ones: the window shown before the reset
	// still forgets nothing.
	hs.memory.Set = fingerprintSet(t, "later-", 100)
	writeHandoffsFile(t, y, hs)
	_, _, err = s.ResetCallMemory("alice", y.HiredAt, 0, 100, 100)
	unchanged("a confirmation from before the last reset", err, ErrCallMemoryChanged)
	if _, cleared, err := s.ResetCallMemory("alice", y.HiredAt, 1, 100, 100); err != nil || cleared != 100 {
		t.Fatalf("a confirmation of what is there: %d %v", cleared, err)
	}
	_, _, err = s.ResetCallMemory("alice", y.HiredAt, 2, 0, 0)
	unchanged("nothing to forget", err, nil)

	// The last keptResets resets are recorded, and read back.
	hs, _ = s.Handoffs("alice")
	for i := range 10 {
		hs.memory.Set = fingerprintSet(t, "round-"+strconv.Itoa(i)+"-", 3)
		hs.forgetCalls(time.Now().UTC())
	}
	back, err := decodeHandoffs(writeHandoffsFile(t, y, hs))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(back.memory.Resets); n != keptResets || back.memory.Epoch != 12 || back.memory.Resets[0].Epoch != 5 || back.memory.Resets[n-1].Epoch != 12 {
		t.Fatalf("recorded %+v", back.memory.Resets)
	}
}

// A pre-release version's filter is kept as it is: still checked, never
// added to, its state said truly, and forgotten only by the user's reset.
func TestHandoffsPreReleaseFilterIsKept(t *testing.T) {
	t.Run("lightly used", func(t *testing.T) {
		s := handoffStore(t)
		y, _ := s.Get("alice")
		filter := preReleaseFilter("legacy-", 2000)
		base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		var items []Handoff
		for i := range keptCalls + keptClosed + 1 {
			at := base.Add(time.Duration(i) * time.Minute)
			items = append(items, Handoff{ID: "n" + strconv.Itoa(i+1), Type: HandoffManual, Title: "x", ToolCall: "c" + strconv.Itoa(i), CreatedAt: at, ClosedAt: at.Add(time.Second), Resolution: HandoffDone, ClosedBy: "user"})
		}
		writeHandoffsFile(t, y, Handoffs{Items: items, Next: len(items), filter: filter})
		hs, _ := s.Handoffs("alice")
		if m := hs.CallMemory(); m.State != "legacy" || m.Forgettable != 2000 || m.Refusing() || !m.Resettable() {
			t.Fatalf("call memory %+v", m)
		}
		if _, _, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "legacy-5"}); err == nil || !strings.Contains(err.Error(), "pre-release") || !strings.Contains(err.Error(), "call again") {
			t.Errorf("a call the filter holds: %v", err)
		}
		h, created, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "new-1"})
		if err != nil || !created {
			t.Fatalf("a new call: %v %v", created, err)
		}
		// Trimming goes to the memory, never into the filter.
		if _, err := s.CloseHandoff("alice", h.ID, HandoffDone, "user", ""); err != nil {
			t.Fatal(err)
		}
		hs, _ = s.Handoffs("alice")
		if !bytes.Equal(hs.filter.Bits, filter.Bits) || hs.filter.Count != 2000 || len(hs.memory.Set) != 2 {
			t.Fatalf("filter changed or memory not used: %d fingerprints", len(hs.memory.Set))
		}
		m := hs.CallMemory()
		if _, cleared, err := s.ResetCallMemory("alice", y.HiredAt, m.Epoch, m.Used, m.Forgettable); err != nil || cleared != 2002 {
			t.Fatalf("reset: %d %v", cleared, err)
		}
		if raw, _ := os.ReadFile(y.HandoffsFile()); bytes.Contains(raw, []byte(filterKey)) {
			t.Error("the filter is still in the file")
		}
		if _, created, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "legacy-5"}); err != nil || !created {
			t.Errorf("a call the filter held, after the reset: %v %v", created, err)
		}
	})
	t.Run("full", func(t *testing.T) {
		s := handoffStore(t)
		y, _ := s.Get("alice")
		filter := preReleaseFilter("legacy-", 0)
		for i := range filter.Bits {
			filter.Bits[i] = 0xff
		}
		filter.Count = 1_000_000
		raw := writeHandoffsFile(t, y, Handoffs{Items: []Handoff{}, filter: filter})
		for i := range 4 {
			cold := New(filepath.Dir(y.Path), filepath.Join(t.TempDir(), "archive"))
			_, created, err := cold.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "never-held-" + strconv.Itoa(i)})
			if err == nil || created || strings.Contains(err.Error(), "call again") || !strings.Contains(err.Error(), "start a new tool-call memory") {
				t.Errorf("a new call: %v %v", created, err)
			}
			if after, _ := os.ReadFile(y.HandoffsFile()); !bytes.Equal(raw, after) {
				t.Fatal("refusing changed the file")
			}
		}
		hs, _ := s.Handoffs("alice")
		m := hs.CallMemory()
		if m.State != "legacy-full" || !m.Refusing() {
			t.Fatalf("call memory %+v", m)
		}
		if _, _, err := s.ResetCallMemory("alice", y.HiredAt, m.Epoch, m.Used, m.Forgettable); err != nil {
			t.Fatal(err)
		}
		if _, created, err := s.AddHandoff("alice", Handoff{Type: HandoffManual, Title: "x", ToolCall: "never-held-0"}); err != nil || !created {
			t.Errorf("after the reset: %v %v", created, err)
		}
	})
}
