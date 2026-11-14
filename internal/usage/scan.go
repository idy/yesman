// Package usage reads what yesman's agents actually used from their pi
// session logs, for Settings › Model usage: the model, effort, tokens and
// timing of each response, aggregated by provider, model and effort.
//
// It only reads. Session logs stay pi's: a session file is JSON Lines that
// pi appends to (see pi's docs/session-format.md and docs/message-types.md).
// The fields used are:
//
//   - message entries whose message has role "assistant": provider, model,
//     responseModel, thinkingLevel (the Pi thinking level requested for that
//     response), usage, stopReason, timestamp (Unix ms when the request
//     started) and durationMs (ms from timestamp until the response ended,
//     on a monotonic clock; absent for legacy messages);
//   - usage entries (model work outside the conversation, such as cache
//     warming): provider, model and usage, without an effort;
//   - compaction and branch_summary entries, and tool results, whose usage
//     names no model.
//
// pi's Usage counts input without cache reads and writes; totalTokens is
// pi's own total, which is input + output + cacheRead + cacheWrite for the
// providers it normalizes. Reasoning tokens are part of output. Cost is
// pi's estimate from its catalog's prices, not a provider bill, and is not
// read.
package usage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kinds of usage record.
const (
	// KindResponse is an assistant message: one model request.
	KindResponse = "response"
	// KindEntry is a usage entry: model-attributed work outside the
	// conversation, such as cache warming.
	KindEntry = "usage"
	// KindSummary is the usage of a compaction or branch summary, which pi
	// records without a model.
	KindSummary = "summary"
	// KindTool is nested model work a tool reported, without a model.
	KindTool = "tool"
)

// Tokens are the counts of one usage record, as pi reports them.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Total      int64 `json:"total"`
}

// Record is one usage record of an agent's session log: numbers and stable
// identifiers only, never message text.
type Record struct {
	// key identifies the entry across copies of it: a forked or branched
	// session copies entries, with their IDs, into a new file.
	key  string
	Kind string
	// Provider and Model name the model requested; empty when the record
	// names none.
	Provider, Model string
	// ResponseModel is the concrete model the provider reported, if it
	// reported another.
	ResponseModel string
	// Effort is the Pi thinking level requested for the response; empty
	// when pi did not record one.
	Effort string
	// At is when the request started, for a response, else when the entry
	// was written.
	At time.Time
	// DurationMs is how long the response took, from the request's start
	// to its end; -1 when not recorded.
	DurationMs int64
	// Stop is a response's stopReason.
	Stop string
	Tokens
}

// maxLine is the longest line read. Longer lines (large images in user
// messages or tool results) never hold usage worth reading, and are
// skipped and counted. Tests lower it.
var maxLine int64 = 64 << 20

// headLen is how much of a file's start, and of what was read last, is
// kept to recognize a file that was rewritten rather than appended to.
const headLen = 256

// file is what was read of one session file.
type file struct {
	size       int64
	mod        time.Time
	offset     int64  // bytes read, always at the end of a line
	head       []byte // the first bytes, to tell an append from a rewrite
	tail       []byte // the bytes read last, before offset, likewise
	records    []Record
	unreadable int // lines that are not JSON, or whose usage is malformed
	oversized  int // lines longer than maxLine, skipped
	err        error
}

// Dir reads one agent's session directory, again only as far as its files
// grew since the last read. It is not safe for concurrent use.
type Dir struct {
	files map[string]*file
}

// DirStats count what reading a session directory met.
type DirStats struct {
	Files      int
	Unreadable int
	Oversized  int
	Errors     []string
}

// Scan reads the directory's session files: new ones whole, grown ones from
// where the last read ended, and rewritten or shorter ones again. A missing
// directory has no records.
func (d *Dir) Scan(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		d.files = nil
		return nil
	}
	if err != nil {
		return err
	}
	if d.files == nil {
		d.files = map[string]*file{}
	}
	seen := map[string]bool{}
	for _, e := range entries {
		// Regular files only: links could lead outside the workspace.
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		seen[e.Name()] = true
		f := d.files[e.Name()]
		if f == nil {
			f = &file{}
			d.files[e.Name()] = f
		}
		f.read(ctx, filepath.Join(dir, e.Name()))
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	for name := range d.files {
		if !seen[name] {
			delete(d.files, name)
		}
	}
	return nil
}

// Records returns the directory's records, oldest file first, each entry
// once.
func (d *Dir) Records() ([]Record, DirStats) {
	names := make([]string, 0, len(d.files))
	for name := range d.files {
		names = append(names, name)
	}
	// pi names session files by their start time, so names sort in order.
	sort.Strings(names)
	var out []Record
	var st DirStats
	seen := map[string]bool{}
	for _, name := range names {
		f := d.files[name]
		st.Files++
		st.Unreadable += f.unreadable
		st.Oversized += f.oversized
		if f.err != nil {
			st.Errors = append(st.Errors, fmt.Sprintf("%s: %v", name, f.err))
		}
		for _, r := range f.records {
			if r.key != "" {
				if seen[r.key] {
					continue
				}
				seen[r.key] = true
			}
			out = append(out, r)
		}
	}
	return out, st
}

// read brings f up to date with the file at path.
func (f *file) read(ctx context.Context, path string) {
	h, err := os.Open(path)
	if err != nil {
		f.reset()
		f.err = err
		return
	}
	defer h.Close()
	info, err := h.Stat()
	if err != nil {
		f.reset()
		f.err = err
		return
	}
	if !info.Mode().IsRegular() {
		f.reset()
		f.err = errors.New("not a regular file")
		return
	}
	if f.err == nil && info.Size() == f.size && info.ModTime().Equal(f.mod) {
		return
	}
	if !f.continues(h, info.Size()) {
		f.reset()
	}
	f.err = nil
	if _, err := h.Seek(f.offset, io.SeekStart); err != nil {
		f.reset()
		f.err = err
		return
	}
	if err := f.parse(ctx, h, path); err != nil {
		f.err = err
	}
	// The size and time read are those before reading: a write meanwhile
	// shows as a change next time.
	f.size, f.mod = info.Size(), info.ModTime()
	if len(f.head) < headLen {
		f.head = readAt(h, 0, headLen)
	}
	f.tail = f.tailAt(h)
}

// tailAt reads the last bytes before the offset.
func (f *file) tailAt(h *os.File) []byte {
	n := min(f.offset, headLen)
	return readAt(h, f.offset-n, int(n))
}

func (f *file) reset() {
	*f = file{}
}

// continues reports whether the file only grew since it was read: it is no
// shorter than what was read, and starts and goes on to the offset as it
// did. pi appends to session files, and rewrites them whole only to migrate
// them, which changes their header.
func (f *file) continues(h *os.File, size int64) bool {
	if f.offset == 0 || size < f.offset {
		return false
	}
	head := readAt(h, 0, headLen)
	n := min(len(head), len(f.head))
	return n > 0 && bytes.Equal(head[:n], f.head[:n]) && bytes.Equal(f.tailAt(h), f.tail)
}

func readAt(h *os.File, at int64, n int) []byte {
	buf := make([]byte, n)
	got, _ := h.ReadAt(buf, at)
	return buf[:got]
}

// parse reads complete lines from the current offset. A last line without
// its newline is being written: it is read next time.
func (f *file) parse(ctx context.Context, r io.Reader, path string) error {
	br := bufio.NewReaderSize(r, 256<<10)
	var (
		line []byte // a line longer than the buffer, gathered
		size int64  // bytes of the line so far
		skip bool   // the line is longer than maxLine
	)
	for n := 0; ; n++ {
		if n%512 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		chunk, err := br.ReadSlice('\n')
		size += int64(len(chunk))
		if errors.Is(err, bufio.ErrBufferFull) {
			if !skip && size <= maxLine {
				line = append(line, chunk...)
			} else {
				skip, line = true, line[:0]
			}
			continue
		}
		if err == io.EOF {
			return nil // partial or no last line: left for next time
		}
		if err != nil {
			return err
		}
		start := f.offset
		f.offset += size
		switch {
		case skip || size > maxLine:
			f.oversized++
		case len(line) > 0:
			line = append(line, chunk...)
			f.line(bytes.TrimSpace(line), path, start)
		default:
			f.line(bytes.TrimSpace(chunk), path, start)
		}
		line, size, skip = line[:0], 0, false
	}
}

// entry holds the fields of a session entry that usage reads.
type entry struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Provider  string          `json:"provider"`
	Model     string          `json:"model"`
	Usage     json.RawMessage `json:"usage"`
	Message   *struct {
		Role          string          `json:"role"`
		Provider      string          `json:"provider"`
		Model         string          `json:"model"`
		ResponseModel string          `json:"responseModel"`
		ThinkingLevel string          `json:"thinkingLevel"`
		Usage         json.RawMessage `json:"usage"`
		StopReason    string          `json:"stopReason"`
		Timestamp     json.Number     `json:"timestamp"`
		DurationMs    json.Number     `json:"durationMs"`
	} `json:"message"`
}

var usageKey = []byte(`"usage":`)

// line reads one line, at offset start of path.
func (f *file) line(b []byte, path string, start int64) {
	if len(b) == 0 {
		return
	}
	// Only lines with a usage field can hold a record; others are checked
	// only for being JSON at all.
	if !bytes.Contains(b, usageKey) {
		if !json.Valid(b) {
			f.unreadable++
		}
		return
	}
	var e entry
	if err := json.Unmarshal(b, &e); err != nil {
		f.unreadable++
		return
	}
	written, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
	r := Record{DurationMs: -1}
	var raw json.RawMessage
	switch {
	case e.Type == "message" && e.Message != nil && e.Message.Role == "assistant":
		m := e.Message
		raw = m.Usage
		r.Kind, r.Provider, r.Model, r.ResponseModel, r.Effort, r.Stop = KindResponse, m.Provider, m.Model, m.ResponseModel, m.ThinkingLevel, m.StopReason
		if r.ResponseModel == r.Model {
			r.ResponseModel = ""
		}
		if ms, ok := wholeNumber(m.Timestamp); ok {
			r.At = time.UnixMilli(ms).UTC()
		} else {
			r.At = written
		}
		if d, ok := wholeNumber(m.DurationMs); ok {
			r.DurationMs = d
		}
		r.key = "m|" + e.ID + "|" + string(m.Timestamp) + "|" + e.Timestamp
	case e.Type == "message" && e.Message != nil && e.Message.Role == "toolResult":
		raw = e.Message.Usage
		r.Kind, r.At = KindTool, written
		r.key = "t|" + e.ID + "|" + string(e.Message.Timestamp) + "|" + e.Timestamp
	case e.Type == "usage":
		raw = e.Usage
		r.Kind, r.Provider, r.Model, r.At = KindEntry, e.Provider, e.Model, written
		r.key = "u|" + e.ID + "|" + e.Timestamp
	case e.Type == "compaction" || e.Type == "branch_summary":
		raw = e.Usage
		r.Kind, r.At = KindSummary, written
		r.key = "s|" + e.ID + "|" + e.Timestamp
	default:
		return
	}
	if len(raw) == 0 || string(raw) == "null" {
		return // no usage recorded: nothing to count
	}
	t, ok := readTokens(raw)
	if !ok {
		f.unreadable++
		return
	}
	if r.At.IsZero() {
		f.unreadable++
		return
	}
	if e.ID == "" {
		// Legacy entries have no ID: such a line counts once, where it is.
		r.key = "l|" + filepath.Base(path) + "|" + strconv.FormatInt(start, 10)
	}
	r.Tokens = t
	f.records = append(f.records, r)
}

// readTokens reads pi's Usage. Each count must be a finite whole number of
// zero or more; a missing count is zero, as pi always writes all four.
func readTokens(raw json.RawMessage) (Tokens, bool) {
	var u map[string]json.RawMessage
	if json.Unmarshal(raw, &u) != nil || u == nil {
		return Tokens{}, false
	}
	var t Tokens
	for _, c := range []struct {
		key string
		to  *int64
	}{{"input", &t.Input}, {"output", &t.Output}, {"cacheRead", &t.CacheRead}, {"cacheWrite", &t.CacheWrite}, {"totalTokens", &t.Total}} {
		v, ok := u[c.key]
		if !ok || string(v) == "null" {
			continue
		}
		n, ok := wholeNumber(json.Number(strings.TrimSpace(string(v))))
		if !ok || n < 0 {
			return Tokens{}, false
		}
		*c.to = n
	}
	if _, ok := u["totalTokens"]; !ok {
		t.Total = t.Input + t.Output + t.CacheRead + t.CacheWrite
	}
	return t, true
}

// wholeNumber reads a JSON number that is a finite whole number.
func wholeNumber(n json.Number) (int64, bool) {
	if n == "" {
		return 0, false
	}
	if i, err := n.Int64(); err == nil {
		return i, true
	}
	v, err := n.Float64()
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || math.Abs(v) > 1<<53 {
		return 0, false
	}
	return int64(v), true
}
