package yesmen

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
)

// A tool call that registered a handoff never registers another. The calls
// of recent handoffs are kept as they are; older ones are kept as a
// fingerprint in callFilter, a Bloom filter of a fixed size: each call sets a
// few of its bits, at places its hash chooses. A call it holds is always
// found, after any number of restarts, so a forgotten call is refused as a
// repeat. A call it does not hold is sometimes found too, and refused as if
// it were one; the lead's next call has another ID. With the default 2^18
// bits and 7 places that happens to about 1 call in 25,000 once 10,000 calls
// are held, 1 in 500 at 20,000 and 1 in 8 at 50,000, each one a handoff the
// user closed. It never refuses every call.

const (
	callFilterBytes  = 1 << 15 // 2^18 bits
	callFilterPlaces = 7
	// Bounds on a filter read from the file, which a later version may
	// have sized differently.
	maxCallFilterBytes  = 1 << 20
	maxCallFilterPlaces = 32
)

// callFilter is the fingerprint of the tool calls of forgotten handoffs.
type callFilter struct {
	Places int    `json:"places"`
	Bits   []byte `json:"bits"`  // base64 in the file
	Count  int    `json:"count"` // the calls added, for an idea of how full it is
	extra  map[string]json.RawMessage
}

var callFilterKeys = []string{"places", "bits", "count"}

func newCallFilter() *callFilter {
	return &callFilter{Places: callFilterPlaces, Bits: make([]byte, callFilterBytes)}
}

func (f *callFilter) valid() error {
	if f.Places < 1 || f.Places > maxCallFilterPlaces || len(f.Bits) == 0 || len(f.Bits) > maxCallFilterBytes || f.Count < 0 {
		return errors.New("not a filter of tool calls")
	}
	return nil
}

// places returns where a call's bits are: hashes of the call, combined as
// in double hashing.
func (f *callFilter) places(call string) []uint64 {
	sum := sha256.Sum256([]byte("yesman needs-you tool call\x00" + call))
	h1 := binary.BigEndian.Uint64(sum[0:8])
	h2 := binary.BigEndian.Uint64(sum[8:16]) | 1
	n := uint64(len(f.Bits)) * 8
	out := make([]uint64, f.Places)
	for i := range out {
		out[i] = (h1 + uint64(i)*h2) % n
	}
	return out
}

func (f *callFilter) add(call string) {
	for _, p := range f.places(call) {
		f.Bits[p/8] |= 1 << (p % 8)
	}
	f.Count++
}

// has reports whether the filter may hold a call; nil holds none.
func (f *callFilter) has(call string) bool {
	if f == nil {
		return false
	}
	for _, p := range f.places(call) {
		if f.Bits[p/8]&(1<<(p%8)) == 0 {
			return false
		}
	}
	return true
}
