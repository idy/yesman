package yesmen

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The handoffs file is read and written only inside the workspace's own
// .yesman directory, through handles opened from the workspace: never through
// a link, a directory put in .yesman's place, a hard link shared with another
// file, a pipe or an oversized file. Such a file is reported and left as it
// is. Writes go to a fresh temporary file created exclusively in the same
// directory, which then replaces the file read, if it is still that file.
// Fields this version does not know are kept as they were written.

const (
	handoffsName = "needs-you.json"
	// maxHandoffsBytes bounds a read. Everything this version writes stays
	// far below it: the open and kept items at their longest fields, fully
	// escaped, are a few megabytes.
	maxHandoffsBytes = 16 << 20
)

// handoffFault, set only by tests, fails or holds up a step of reading
// ("read") or writing ("write", "rename") the file at path.
var handoffFault func(step, path string) error

// handoffsFile is a handoffs file opened for reading and, maybe, replacing.
type handoffsFile struct {
	path     string   // for messages
	ws, meta *os.Root // the workspace and its .yesman directory
	found    os.FileInfo
}

// openHandoffs opens a yesman's handoffs and reads them.
func openHandoffs(y Yesman) (*handoffsFile, Handoffs, error) {
	f := &handoffsFile{path: y.HandoffsFile()}
	hs, err := f.open(y)
	if err != nil {
		f.close()
		return nil, Handoffs{}, fmt.Errorf("%s: %w; it was left as it is", f.path, err)
	}
	return f, hs, nil
}

func (f *handoffsFile) open(y Yesman) (Handoffs, error) {
	ws, err := os.OpenRoot(y.Path)
	if err != nil {
		return Handoffs{}, err
	}
	f.ws = ws
	dir, err := ws.Lstat(".yesman")
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
	return f.read()
}

func (f *handoffsFile) read() (Handoffs, error) {
	fi, err := f.meta.Lstat(handoffsName)
	if errors.Is(err, fs.ErrNotExist) {
		return Handoffs{Items: []Handoff{}}, nil
	}
	if err != nil {
		return Handoffs{}, err
	}
	if !fi.Mode().IsRegular() {
		return Handoffs{}, errors.New("not a regular file (a link, directory or device)")
	}
	if fi.Size() > maxHandoffsBytes {
		return Handoffs{}, fmt.Errorf("larger than %d bytes", maxHandoffsBytes)
	}
	file, err := f.meta.OpenFile(handoffsName, os.O_RDONLY|openNoBlock, 0)
	if err != nil {
		return Handoffs{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return Handoffs{}, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(fi, opened) {
		return Handoffs{}, errors.New("it changed while it was opened")
	}
	if n, err := linkCount(file, opened); err != nil {
		return Handoffs{}, err
	} else if n != 1 {
		return Handoffs{}, errors.New("it has other hard links")
	}
	if handoffFault != nil {
		if err := handoffFault("read", f.path); err != nil {
			return Handoffs{}, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxHandoffsBytes+1))
	if err != nil {
		return Handoffs{}, err
	}
	if len(data) > maxHandoffsBytes {
		return Handoffs{}, fmt.Errorf("larger than %d bytes", maxHandoffsBytes)
	}
	hs, err := decodeHandoffs(data)
	if err != nil {
		return Handoffs{}, err
	}
	f.found = opened
	return hs, nil
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
	if err := f.unchanged(); err != nil {
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
	// In this app writers take turns; this catches another process that
	// replaced the file meanwhile. A replacement after this check, before
	// the rename, cannot be excluded: renames do not compare and swap.
	if err := f.unchanged(); err != nil {
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

// unchanged checks that the file is still the one read, or still absent.
func (f *handoffsFile) unchanged() error {
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

// The file's own keys besides "items" and "next".
const (
	trimmedKey       = "trimmedCalls"
	trimmedBeforeKey = "trimmedCallsBefore"
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
	hs := Handoffs{Items: make([]Handoff, 0, len(items)), extra: unknownFields(top, "items", "next", trimmedKey, trimmedBeforeKey)}
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
	if raw, ok := top[trimmedBeforeKey]; ok {
		if err := json.Unmarshal(raw, &hs.trimmedBefore); err != nil {
			return fmt.Errorf("%q is not a time", trimmedBeforeKey)
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
	if !hs.trimmedBefore.IsZero() {
		at, err := json.Marshal(hs.trimmedBefore)
		if err != nil {
			return nil, err
		}
		b.WriteString(`,"` + trimmedBeforeKey + `":`)
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
