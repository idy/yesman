package yesmen

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The handoffs file is read and written only inside the workspace's own
// .yesman directory, through handles opened from the Store's folder: the
// workspace must be a directory in it, of the yesman asked for, and the file
// is never reached through a link, a directory put in the workspace's or
// .yesman's place, a hard link shared with another file, a pipe or an
// oversized file. Such a file is reported and left as it is. Writes go to a
// fresh temporary file created exclusively in the same directory, which then
// replaces the file read, if it is still that file with the bytes read.
// Fields this version does not know are kept as they were written.

const (
	handoffsName = "needs-you.json"
	// maxHandoffsBytes bounds a read. Everything this version writes stays
	// far below it: the open and kept items at their longest fields, fully
	// escaped, with the tool-call memory full, are some 6.5 MB
	// (TestCallMemoryAtCapacity).
	maxHandoffsBytes = 16 << 20
)

// handoffFault, set only by tests, fails or holds up a step of reading
// ("read") or writing ("write", "rename") the file at path, or of reading
// the yesman's identity ("meta", between checking and opening it).
var handoffFault func(step, path string) error

// handoffsFile is a handoffs file opened for reading and, maybe, replacing.
type handoffsFile struct {
	path     string   // for messages
	ws, meta *os.Root // the workspace and its .yesman directory
	// found is the file read, and sum the SHA-256 of its bytes; nil when
	// there was none.
	found os.FileInfo
	sum   [sha256.Size]byte
}

// openHandoffs opens a yesman's handoffs, in the Store's folder root, and
// reads them. metaMu is the Store's lock on reading yesman.json.
func openHandoffs(root string, y Yesman, metaMu *sync.Mutex) (*handoffsFile, Handoffs, error) {
	f := &handoffsFile{path: y.HandoffsFile()}
	hs, err := f.open(root, y, metaMu)
	if err != nil {
		f.close()
		return nil, Handoffs{}, fmt.Errorf("%s: %w; it was left as it is", f.path, err)
	}
	return f, hs, nil
}

func (f *handoffsFile) open(root string, y Yesman, metaMu *sync.Mutex) (Handoffs, error) {
	// The Store's folder may be reached through a link, as configured; the
	// workspace in it may not.
	name := filepath.Base(y.Path)
	if filepath.Join(root, name) != y.Path {
		return Handoffs{}, errors.New("not a workspace of this store")
	}
	store, err := os.OpenRoot(root)
	if err != nil {
		return Handoffs{}, err
	}
	defer store.Close()
	dir, err := store.Lstat(name)
	if err != nil {
		return Handoffs{}, err
	}
	if dir.Mode().Type() != fs.ModeDir {
		return Handoffs{}, errors.New("the workspace is not a directory of the store (a link or another kind of file)")
	}
	ws, err := store.OpenRoot(name)
	if err != nil {
		return Handoffs{}, err
	}
	f.ws = ws
	if opened, err := ws.Stat("."); err != nil {
		return Handoffs{}, err
	} else if !os.SameFile(dir, opened) {
		return Handoffs{}, errors.New("the workspace changed while it was opened")
	}
	dir, err = ws.Lstat(".yesman")
	if err != nil {
		return Handoffs{}, err
	}
	if dir.Mode().Type() != fs.ModeDir {
		return Handoffs{}, errors.New(".yesman is not a directory of the workspace (a link or another kind of file)")
	}
	meta, err := ws.OpenRoot(".yesman")
	if err != nil {
		return Handoffs{}, err
	}
	f.meta = meta
	opened, err := meta.Stat(".")
	if err != nil {
		return Handoffs{}, err
	}
	if !os.SameFile(dir, opened) {
		return Handoffs{}, errors.New(".yesman changed while it was opened")
	}
	// The workspace opened must be the yesman asked for, not a newer one
	// of the same name that took its place since.
	hired, err := f.hiredAt(metaMu)
	if err != nil {
		return Handoffs{}, err
	}
	if !hired.Equal(y.HiredAt) {
		return Handoffs{}, errors.New("the workspace was replaced while it was opened")
	}
	return f.read()
}

// maxMetaBytes bounds the yesman.json read for the yesman's identity.
const maxMetaBytes = 1 << 20

// hiredAt reads when the opened workspace's yesman was hired, from its own
// yesman.json: a regular file of a bounded size, opened without blocking, so
// that a pipe, a device or a link in its place is refused, never waited on.
// A workspace without one was hired at the zero time, as Store.Get says; one
// that cannot be read or parsed is an error. metaMu, which Rename's atomic
// replacement needs on Windows, is held only to open and read the file once
// it is known to be a regular one.
func (f *handoffsFile) hiredAt(metaMu *sync.Mutex) (time.Time, error) {
	data, err := readRegular(f.meta, "yesman.json", maxMetaBytes, metaMu)
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("yesman.json: %w", err)
	}
	var meta struct {
		HiredAt time.Time `json:"hiredAt"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return time.Time{}, fmt.Errorf("yesman.json: %w", err)
	}
	return meta.HiredAt, nil
}

// readRegular reads the regular file name in root, of at most limit bytes,
// opening it without blocking: a link, pipe, directory or device in its
// place, or one swapped in while it is opened, is refused unread. lock is
// held only while the file is opened and read.
func readRegular(root *os.Root, name string, limit int64, lock sync.Locker) ([]byte, error) {
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file (a link, pipe, directory or device)")
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("larger than %d bytes", limit)
	}
	if handoffFault != nil {
		if err := handoffFault("meta", name); err != nil {
			return nil, err
		}
	}
	lock.Lock()
	defer lock.Unlock()
	file, err := root.OpenFile(name, os.O_RDONLY|openNoBlock, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(fi, opened) {
		return nil, errors.New("it changed while it was opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("larger than %d bytes", limit)
	}
	return data, nil
}

func (f *handoffsFile) read() (Handoffs, error) {
	data, opened, err := f.load(true)
	if err != nil {
		return Handoffs{}, err
	}
	if opened == nil {
		return Handoffs{Items: []Handoff{}}, nil
	}
	hs, err := decodeHandoffs(data)
	if err != nil {
		return Handoffs{}, err
	}
	f.found, f.sum = opened, sha256.Sum256(data)
	return hs, nil
}

// load reads the file's bytes, if it is a regular, singly linked file of a
// size that can be read; with no file, it returns nil and no error. fault
// lets tests hold up the read.
func (f *handoffsFile) load(fault bool) ([]byte, os.FileInfo, error) {
	fi, err := f.meta.Lstat(handoffsName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, errors.New("not a regular file (a link, directory or device)")
	}
	if fi.Size() > maxHandoffsBytes {
		return nil, nil, fmt.Errorf("larger than %d bytes", maxHandoffsBytes)
	}
	file, err := f.meta.OpenFile(handoffsName, os.O_RDONLY|openNoBlock, 0)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(fi, opened) {
		return nil, nil, errors.New("it changed while it was opened")
	}
	if n, err := linkCount(file, opened); err != nil {
		return nil, nil, err
	} else if n != 1 {
		return nil, nil, errors.New("it has other hard links")
	}
	if fault && handoffFault != nil {
		if err := handoffFault("read", f.path); err != nil {
			return nil, nil, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxHandoffsBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxHandoffsBytes {
		return nil, nil, fmt.Errorf("larger than %d bytes", maxHandoffsBytes)
	}
	return data, opened, nil
}

// write replaces the file read with hs.
func (f *handoffsFile) write(hs Handoffs) error {
	if err := f.replace(hs); err != nil {
		return fmt.Errorf("%s: %w; it was left as it is", f.path, err)
	}
	return nil
}

func (f *handoffsFile) replace(hs Handoffs) error {
	data, err := encodeHandoffs(hs)
	if err != nil {
		return err
	}
	// Never write what could not be read again.
	if len(data) > maxHandoffsBytes {
		return fmt.Errorf("it would grow past %d bytes", maxHandoffsBytes)
	}
	if err := f.unchanged(false); err != nil {
		return err
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := "." + handoffsName + "." + hex.EncodeToString(nonce[:]) + ".tmp"
	file, err := f.meta.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = f.meta.Remove(tmp)
		}
	}()
	_, err = file.Write(data)
	if err == nil && handoffFault != nil {
		err = handoffFault("write", f.path)
	}
	if err == nil {
		err = file.Sync()
	}
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	// In this app writers take turns; this catches another process, or
	// someone editing the file, that replaced or changed it meanwhile: the
	// change is kept and this one is not made. A change after this check,
	// before the rename, cannot be excluded: renames do not compare and swap.
	if err := f.unchanged(true); err != nil {
		return err
	}
	if handoffFault != nil {
		if err := handoffFault("rename", f.path); err != nil {
			return err
		}
	}
	if err := f.meta.Rename(tmp, handoffsName); err != nil {
		return err
	}
	done = true
	return nil
}

// unchanged checks that the file is still the one read, or still absent,
// and with content, that it still has the bytes read.
func (f *handoffsFile) unchanged(content bool) error {
	cur, err := f.meta.Lstat(handoffsName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if f.found != nil {
			return errors.New("it was removed after it was read")
		}
		return nil
	case err != nil:
		return err
	case f.found == nil || !cur.Mode().IsRegular() || !os.SameFile(cur, f.found):
		return errors.New("it was replaced after it was read")
	case !content:
		return nil
	}
	data, opened, err := f.load(false)
	switch {
	case err != nil:
		return err
	case opened == nil || !os.SameFile(opened, f.found):
		return errors.New("it was replaced after it was read")
	case sha256.Sum256(data) != f.sum:
		return errors.New("it was changed after it was read")
	}
	return nil
}

func (f *handoffsFile) close() {
	if f.meta != nil {
		f.meta.Close()
	}
	if f.ws != nil {
		f.ws.Close()
	}
}

// Stored form. Handoff and Handoffs marshal only their known fields, which is
// what the API and the page get; the file also keeps the fields this version
// does not know, byte for byte, numbers included.

var (
	handoffID   = regexp.MustCompile(`^n[1-9][0-9]{0,9}$`)
	handoffKeys = jsonKeys(reflect.TypeFor[Handoff]())
	trimmedKeys = jsonKeys(reflect.TypeFor[trimmedCall]())
)

// The file's own keys besides "items" and "next". filterKey and unsureKey
// are only in files of a pre-release version.
const (
	trimmedKey = "trimmedCalls"
	memoryKey  = "callMemory"
	filterKey  = "forgottenCalls"
	unsureKey  = "trimmedCallsBefore"
)

func jsonKeys(t reflect.Type) []string {
	var keys []string
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" {
			name = f.Name
		}
		keys = append(keys, name)
	}
	return keys
}

func unknownFields(obj map[string]json.RawMessage, known ...string) map[string]json.RawMessage {
	var extra map[string]json.RawMessage
	for k, v := range obj {
		if slices.Contains(known, k) {
			continue
		}
		if extra == nil {
			extra = map[string]json.RawMessage{}
		}
		extra[k] = v
	}
	return extra
}

func decodeHandoffs(data []byte) (Handoffs, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return Handoffs{}, err
	}
	if top == nil {
		return Handoffs{}, errors.New("not a JSON object")
	}
	rawItems, okItems := top["items"]
	rawNext, okNext := top["next"]
	if !okItems || !okNext {
		return Handoffs{}, errors.New(`"items" or "next" is missing`)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawItems, &items); err != nil || items == nil {
		return Handoffs{}, errors.New(`"items" is not a list`)
	}
	hs := Handoffs{Items: make([]Handoff, 0, len(items)), extra: unknownFields(top, "items", "next", trimmedKey, memoryKey, filterKey, unsureKey)}
	if err := json.Unmarshal(rawNext, &hs.Next); err != nil || hs.Next < 0 {
		return Handoffs{}, errors.New(`"next" is not a count`)
	}
	if err := decodeTrimmed(top, &hs); err != nil {
		return Handoffs{}, err
	}
	seen := map[string]bool{}
	for i, raw := range items {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
			return Handoffs{}, fmt.Errorf("item %d is not an object", i+1)
		}
		var h Handoff
		if err := json.Unmarshal(raw, &h); err != nil {
			return Handoffs{}, fmt.Errorf("item %d: %w", i+1, err)
		}
		if err := h.valid(); err != nil {
			return Handoffs{}, fmt.Errorf("item %d: %w", i+1, err)
		}
		if seen[h.ID] {
			return Handoffs{}, fmt.Errorf("item %d repeats the ID %s", i+1, h.ID)
		}
		seen[h.ID] = true
		h.extra = unknownFields(obj, handoffKeys...)
		hs.Items = append(hs.Items, h)
	}
	return hs, nil
}

// decodeTrimmed reads the tool calls of trimmed handoffs, which files older
// than them do not have.
func decodeTrimmed(top map[string]json.RawMessage, hs *Handoffs) error {
	if raw, ok := top[unsureKey]; ok {
		if err := json.Unmarshal(raw, &hs.unsure); err != nil {
			return fmt.Errorf("%q is not a time", unsureKey)
		}
	}
	if raw, ok := top[filterKey]; ok {
		var obj map[string]json.RawMessage
		var c callFilter
		if json.Unmarshal(raw, &obj) != nil || obj == nil || json.Unmarshal(raw, &c) != nil || c.valid() != nil {
			return fmt.Errorf("%q is not a filter of tool calls", filterKey)
		}
		c.extra = unknownFields(obj, callFilterKeys...)
		hs.filter = &c
	}
	if raw, ok := top[memoryKey]; ok {
		if err := decodeCallMemory(raw, &hs.memory); err != nil {
			return fmt.Errorf("%q: %w", memoryKey, err)
		}
	}
	raw, ok := top[trimmedKey]
	if !ok {
		return nil
	}
	var calls []json.RawMessage
	if err := json.Unmarshal(raw, &calls); err != nil || calls == nil {
		return fmt.Errorf("%q is not a list", trimmedKey)
	}
	for i, raw := range calls {
		var obj map[string]json.RawMessage
		var c trimmedCall
		if json.Unmarshal(raw, &obj) != nil || obj == nil || json.Unmarshal(raw, &c) != nil ||
			c.ToolCall == "" || len(c.ToolCall) > maxHandoffToolLen || !handoffID.MatchString(c.ID) || c.CreatedAt.IsZero() {
			return fmt.Errorf("%s item %d is not a trimmed tool call", trimmedKey, i+1)
		}
		c.extra = unknownFields(obj, trimmedKeys...)
		hs.trimmed = append(hs.trimmed, c)
	}
	return nil
}

// valid checks what every stored handoff has.
func (h Handoff) valid() error {
	switch {
	case !handoffID.MatchString(h.ID):
		return fmt.Errorf("%q is not a handoff ID", h.ID)
	case !slices.Contains(HandoffTypes, h.Type):
		return fmt.Errorf("%q is not a type of handoff", h.Type)
	case strings.TrimSpace(h.Title) == "":
		return errors.New("no title")
	case h.CreatedAt.IsZero():
		return errors.New("no creation time")
	case h.ClosedAt.IsZero() != (h.Resolution == "") || h.ClosedAt.IsZero() != (h.ClosedBy == ""):
		return errors.New("closedAt, resolution and closedBy do not agree")
	}
	return nil
}

func encodeHandoffs(hs Handoffs) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`{"items":[`)
	for i, h := range hs.Items {
		if i > 0 {
			b.WriteByte(',')
		}
		known, err := json.Marshal(h)
		if err != nil {
			return nil, err
		}
		b.Write(known[:len(known)-1]) // without its closing brace
		if err := writeFields(&b, h.extra); err != nil {
			return nil, err
		}
		b.WriteByte('}')
	}
	b.WriteString(`],"next":`)
	b.WriteString(strconv.Itoa(hs.Next))
	if len(hs.trimmed) > 0 {
		b.WriteString(`,"` + trimmedKey + `":[`)
		for i, c := range hs.trimmed {
			if i > 0 {
				b.WriteByte(',')
			}
			known, err := json.Marshal(c)
			if err != nil {
				return nil, err
			}
			b.Write(known[:len(known)-1])
			if err := writeFields(&b, c.extra); err != nil {
				return nil, err
			}
			b.WriteByte('}')
		}
		b.WriteByte(']')
	}
	if !hs.memory.empty() {
		b.WriteString(`,"` + memoryKey + `":`)
		if err := encodeCallMemory(&b, &hs.memory); err != nil {
			return nil, err
		}
	}
	if hs.filter != nil {
		known, err := json.Marshal(hs.filter)
		if err != nil {
			return nil, err
		}
		b.WriteString(`,"` + filterKey + `":`)
		b.Write(known[:len(known)-1])
		if err := writeFields(&b, hs.filter.extra); err != nil {
			return nil, err
		}
		b.WriteByte('}')
	}
	if !hs.unsure.IsZero() {
		at, err := json.Marshal(hs.unsure)
		if err != nil {
			return nil, err
		}
		b.WriteString(`,"` + unsureKey + `":`)
		b.Write(at)
	}
	if err := writeFields(&b, hs.extra); err != nil {
		return nil, err
	}
	b.WriteByte('}')
	var out bytes.Buffer
	if err := json.Indent(&out, b.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// writeFields appends fields to an object being written, in a stable order.
func writeFields(b *bytes.Buffer, fields map[string]json.RawMessage) error {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		name, err := json.Marshal(k)
		if err != nil {
			return err
		}
		b.WriteByte(',')
		b.Write(name)
		b.WriteByte(':')
		b.Write(fields[k])
	}
	return nil
}

// The tool-call memory's own keys.
var callMemoryKeys = []string{"epoch", "fingerprints", "resets"}

// decodeCallMemory reads the tool-call memory: its epoch, its fingerprints,
// base64 of 8 bytes each, big-endian, sorted and each once, and its resets.
func decodeCallMemory(raw json.RawMessage, m *callMemory) error {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return errors.New("not an object")
	}
	var known struct {
		Epoch        int               `json:"epoch"`
		Fingerprints []byte            `json:"fingerprints"`
		Resets       []json.RawMessage `json:"resets"`
	}
	if err := json.Unmarshal(raw, &known); err != nil {
		return err
	}
	if known.Epoch < 0 {
		return errors.New("the epoch is negative")
	}
	set, err := decodeCallSet(known.Fingerprints)
	if err != nil {
		return err
	}
	m.Epoch, m.Set, m.extra = known.Epoch, set, unknownFields(obj, callMemoryKeys...)
	for i, raw := range known.Resets {
		var obj map[string]json.RawMessage
		var r callReset
		if json.Unmarshal(raw, &obj) != nil || obj == nil || json.Unmarshal(raw, &r) != nil ||
			r.Epoch < 1 || r.Epoch > known.Epoch || r.At.IsZero() || r.Cleared < 0 || len(r.SHA256) != 64 {
			return fmt.Errorf("reset %d is not a record of a reset", i+1)
		}
		r.extra = unknownFields(obj, callResetKeys...)
		m.Resets = append(m.Resets, r)
	}
	return nil
}

func encodeCallMemory(b *bytes.Buffer, m *callMemory) error {
	if len(m.Set) > maxCallFingerprints {
		return errors.New("its tool-call memory would hold more fingerprints than can be read")
	}
	resets := make([]json.RawMessage, 0, len(m.Resets))
	for _, r := range m.Resets {
		known, err := json.Marshal(r)
		if err != nil {
			return err
		}
		var one bytes.Buffer
		one.Write(known[:len(known)-1])
		if err := writeFields(&one, r.extra); err != nil {
			return err
		}
		one.WriteByte('}')
		resets = append(resets, one.Bytes())
	}
	known, err := json.Marshal(struct {
		Epoch        int               `json:"epoch"`
		Fingerprints []byte            `json:"fingerprints"`
		Resets       []json.RawMessage `json:"resets"`
	}{m.Epoch, m.setBytes(), resets})
	if err != nil {
		return err
	}
	b.Write(known[:len(known)-1])
	if err := writeFields(b, m.extra); err != nil {
		return err
	}
	b.WriteByte('}')
	return nil
}
