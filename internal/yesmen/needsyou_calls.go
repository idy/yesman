package yesmen

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/bits"
	"slices"
	"time"
)

// A tool call that registered a handoff never registers another while this
// yesman remembers it, whichever process makes it again. Each tool call it
// accepted is remembered in one of three places: in its handoff, while the
// handoff is kept; as itself, among the keptCalls of handoffs trimmed most
// recently; or, older, as a fingerprint (the first 64 bits of a SHA-256 of
// the call) in a sorted set, the tool-call memory. A fingerprint is not the
// call: a new call whose fingerprint happens to equal an old one's is refused
// as a repeat, about once in 2^64/maxCallRecords (some 7×10^13) new calls
// when the memory is full, and the lead calls again.
//
// The three hold at most maxCallRecords tool calls together. A new tool call
// is taken only while there is room for it; closing and trimming only move
// calls from one place to the next, so they never run out of room, and the
// fingerprints never outgrow what this version reads. Once the memory is
// full, new tool calls are refused, with an error that says so, until the
// user starts a new tool-call memory under Needs you (ResetCallMemory). That
// forgets the fingerprints, never a handoff or the calls kept as they are,
// and is recorded: after it, a forgotten call made again registers one new
// handoff, which is then remembered again; another reset can let it back once
// more.

const (
	// maxCallRecords is the tool calls a yesman remembers at most: some
	// 2 MiB of fingerprints, 2.8 MB in the file.
	maxCallRecords = 1 << 18
	// nearCallRecords is when the page and the lead are told the memory is
	// nearly full.
	nearCallRecords = maxCallRecords / 10 * 9
	// keptResets is how many resets of the memory the file records.
	keptResets = 8
	// maxCallFingerprints bounds a fingerprint set read from the file.
	maxCallFingerprints = maxCallRecords
)

// callFingerprint is the fingerprint of a tool call.
func callFingerprint(call string) uint64 {
	sum := sha256.Sum256([]byte("yesman needs-you tool call\x00" + call))
	return binary.BigEndian.Uint64(sum[:8])
}

// callMemory is the fingerprints of the tool calls of handoffs no longer
// kept, and the resets of the memory.
type callMemory struct {
	// Epoch counts the resets; the user's confirmation names it, so that a
	// confirmation shown before another reset forgets nothing.
	Epoch int
	// Set is the fingerprints, sorted, each once.
	Set    []uint64
	Resets []callReset
	extra  map[string]json.RawMessage
}

// callReset records a reset of the memory: when, how many records it
// forgot, and the SHA-256 of what it forgot, for an audit.
type callReset struct {
	Epoch   int       `json:"epoch"`
	At      time.Time `json:"at"`
	Cleared int       `json:"cleared"`
	SHA256  string    `json:"sha256"`
	extra   map[string]json.RawMessage
}

var callResetKeys = []string{"epoch", "at", "cleared", "sha256"}

func (m *callMemory) has(call string) bool {
	_, ok := slices.BinarySearch(m.Set, callFingerprint(call))
	return ok
}

func (m *callMemory) add(call string) {
	fp := callFingerprint(call)
	if i, ok := slices.BinarySearch(m.Set, fp); !ok {
		m.Set = slices.Insert(m.Set, i, fp)
	}
}

// empty reports whether the file need not keep the memory at all.
func (m *callMemory) empty() bool {
	return m.Epoch == 0 && len(m.Set) == 0 && len(m.Resets) == 0 && len(m.extra) == 0
}

func (m *callMemory) setBytes() []byte {
	b := make([]byte, 8*len(m.Set))
	for i, v := range m.Set {
		binary.BigEndian.PutUint64(b[8*i:], v)
	}
	return b
}

func decodeCallSet(b []byte) ([]uint64, error) {
	if len(b)%8 != 0 {
		return nil, errors.New("not a whole number of fingerprints")
	}
	if len(b)/8 > maxCallFingerprints {
		return nil, errors.New("more fingerprints than this version reads")
	}
	set := make([]uint64, len(b)/8)
	for i := range set {
		set[i] = binary.BigEndian.Uint64(b[8*i:])
		if i > 0 && set[i] <= set[i-1] {
			return nil, errors.New("fingerprints out of order or repeated")
		}
	}
	return set, nil
}

// CallMemory is what the page and the lead are shown of a yesman's tool-call
// memory: how full it is, and why new tool calls may be refused. It holds
// counts only, never a call or a fingerprint.
type CallMemory struct {
	// Used is the tool calls remembered: those of kept handoffs, those kept
	// as they are, and the fingerprints. Capacity is the most it holds.
	Used     int `json:"used"`
	Capacity int `json:"capacity"`
	// Forgettable is what a reset forgets: the fingerprints, and those a
	// pre-release version kept.
	Forgettable int `json:"forgettable"`
	// Epoch counts the resets; a reset names the one it was shown.
	Epoch int `json:"epoch"`
	// State is empty while new tool calls are taken freely, "near" from 90%
	// full, "full" once no new one fits, "blocked" when a pre-release
	// version forgot calls without a trace, so every new one is refused,
	// "legacy" when the filter a pre-release version kept older calls in
	// may refuse a few new ones, and "legacy-full" when it is so full that
	// it refuses many (one in a hundred or more).
	State string `json:"state,omitempty"`
}

// Refusing reports whether new tool calls are refused, all or many, until
// the user starts a new memory.
func (m CallMemory) Refusing() bool {
	return m.State == "full" || m.State == "blocked" || m.State == "legacy-full"
}

// Resettable reports whether starting a new tool-call memory would change
// anything.
func (m CallMemory) Resettable() bool {
	return m.Forgettable > 0 || m.State == "blocked" || m.State == "legacy" || m.State == "legacy-full"
}

// callRecords is the tool calls this yesman remembers.
func (hs Handoffs) callRecords() int {
	n := len(hs.memory.Set) + len(hs.trimmed)
	for _, h := range hs.Items {
		if h.ToolCall != "" {
			n++
		}
	}
	return n
}

// CallMemory says how full the yesman's tool-call memory is.
func (hs Handoffs) CallMemory() CallMemory {
	m := CallMemory{Used: hs.callRecords(), Capacity: maxCallRecords, Forgettable: len(hs.memory.Set), Epoch: hs.memory.Epoch}
	if hs.filter != nil {
		m.Forgettable += hs.filter.Count
	}
	switch {
	case !hs.unsure.IsZero():
		m.State = "blocked"
	case m.Used >= maxCallRecords:
		m.State = "full"
	case m.Used >= nearCallRecords:
		m.State = "near"
	case hs.filter != nil && hs.filter.refusesNew() >= legacyRefusing:
		m.State = "legacy-full"
	case hs.filter != nil:
		m.State = "legacy"
	}
	return m
}

// forgetCalls forgets the fingerprints, and what a pre-release version kept
// in their place, recording it, and returns how many records it forgot.
func (hs *Handoffs) forgetCalls(at time.Time) int {
	cleared := len(hs.memory.Set)
	sum := sha256.New()
	sum.Write([]byte("fingerprints\x00"))
	sum.Write(hs.memory.setBytes())
	if hs.filter != nil {
		cleared += hs.filter.Count
		sum.Write([]byte("\x00pre-release filter\x00"))
		sum.Write(hs.filter.Bits)
	}
	if !hs.unsure.IsZero() {
		sum.Write([]byte("\x00pre-release floor\x00" + hs.unsure.UTC().Format(time.RFC3339Nano)))
	}
	hs.memory.Epoch++
	hs.memory.Set = nil
	// The calls a pre-release filter held are forgotten; fields a later
	// version wrote beside them are not.
	if hs.filter != nil && len(hs.filter.extra) > 0 {
		hs.retired = &retiredFilter{Epoch: hs.memory.Epoch, extra: hs.filter.extra}
	}
	hs.memory.Resets = append(hs.memory.Resets, callReset{Epoch: hs.memory.Epoch, At: at, Cleared: cleared, SHA256: hex.EncodeToString(sum.Sum(nil))})
	if n := len(hs.memory.Resets) - keptResets; n > 0 {
		hs.memory.Resets = slices.Clone(hs.memory.Resets[n:])
	}
	hs.filter = nil
	hs.unsure = time.Time{}
	return cleared
}

// A pre-release version kept the tool calls of older handoffs in a Bloom
// filter of a fixed size, which fills up and then refuses new calls more and
// more often. A file that has one keeps it as it is: it is still checked, so
// that what it holds is still refused, but nothing is added to it any more,
// and a reset forgets it. A reset forgets the calls only: the filter's
// fields this version does not know stay where they were, with
// forgottenByReset, the epoch of the reset, in place of the calls.

// retiredFilter is what a reset kept of a pre-release filter.
type retiredFilter struct {
	Epoch int
	extra map[string]json.RawMessage
}

// retiredKey marks a filter whose calls a reset forgot.
const retiredKey = "forgottenByReset"

const (
	maxCallFilterBytes  = 1 << 20
	maxCallFilterPlaces = 32
	// legacyRefusing is when the filter refuses too many new calls for the
	// lead to call again.
	legacyRefusing = 0.01
)

// callFilter is that Bloom filter.
type callFilter struct {
	Places int    `json:"places"`
	Bits   []byte `json:"bits"`  // base64 in the file
	Count  int    `json:"count"` // the calls added
	extra  map[string]json.RawMessage
}

var callFilterKeys = []string{"places", "bits", "count"}

func (f *callFilter) valid() error {
	if f.Places < 1 || f.Places > maxCallFilterPlaces || len(f.Bits) == 0 || len(f.Bits) > maxCallFilterBytes || f.Count < 0 {
		return errors.New("not a filter of tool calls")
	}
	return nil
}

// refusesNew is the chance that the filter takes a new call for one it
// holds: the share of its bits set, to the power of the places.
func (f *callFilter) refusesNew() float64 {
	ones := 0
	for _, b := range f.Bits {
		ones += bits.OnesCount8(b)
	}
	return math.Pow(float64(ones)/float64(8*len(f.Bits)), float64(f.Places))
}

// has reports whether the filter may hold a call; nil holds none.
func (f *callFilter) has(call string) bool {
	if f == nil {
		return false
	}
	sum := sha256.Sum256([]byte("yesman needs-you tool call\x00" + call))
	h1 := binary.BigEndian.Uint64(sum[0:8])
	h2 := binary.BigEndian.Uint64(sum[8:16]) | 1
	n := uint64(len(f.Bits)) * 8
	for i := range uint64(f.Places) {
		p := (h1 + i*h2) % n
		if f.Bits[p/8]&(1<<(p%8)) == 0 {
			return false
		}
	}
	return true
}
