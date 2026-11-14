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
//
// Logs are read from the workspace only: the workspace directory may be
// reached through a link, but .yesman, sessions, each agent's directory and
// each log must be a real directory or a regular file with a single link
// inside it. Anything else is not read, and is reported.
//
// A log already read is read again only as far as it grew: pi appends to
// its session files, and rewrites one whole only to migrate it (which
// changes its first line) or to start an empty one. A log is read again
// from the start when it is another file (an atomic replacement), did not
// grow, or no longer starts or goes on to where it was read as it did; and
// once more when it changed so close to when it was read that a further
// change could keep its time. A rewrite in place that keeps the file, grows
// it and keeps those bytes, or that sets its time back, is not noticed
// until the cache is rebuilt.
package usage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/idy/yesman/internal/yesmen"
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
	// Ended is when a response is known to have ended: the later of when
	// pi wrote it, which it does once the response has ended, and its
	// start plus its duration. Zero when neither is recorded.
	Ended time.Time
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

// racyWindow is how close to when a file was read its modification time
// may be for a later change to keep that time, on file systems that keep
// times as coarsely as two seconds. Such a file is read again once. Tests
// change it.
var racyWindow = 3 * time.Second

// maxDuration bounds a response's recorded duration that is believed: a
// longer one, or a negative one, does not tell when the response ended.
const maxDuration = 24 * time.Hour

// opening, when set by tests, runs between looking at a directory or log and
// opening it, to replace it meanwhile.
var opening func(name string)

// Why a log or directory is not read.
var (
	errNotDir   = errors.New("not a directory in the workspace (a link or a special file); not read")
	errNotFile  = errors.New("not a regular file in the workspace (a link or a special file); not read")
	errChanged  = errors.New("changed while it was being opened; not read")
	errHardLink = errors.New("has other hard links; not read")
)

// file is what was read of one session file.
type file struct {
	info       os.FileInfo // the file read, to tell another one in its place
	size       int64
	mod        time.Time
	racy       bool   // changed too close to when it was read
	offset     int64  // bytes read, always at the end of a line
	parsed     int64  // bytes parsed since the file was first read, for tests
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

// Sessions reads a workspace's session directories, one Dir per agent. It
// is not safe for concurrent use.
type Sessions struct {
	dirs map[string]*Dir
}

// DirStats count what reading a session directory met.
type DirStats struct {
	Files      int
	Unreadable int
	Oversized  int
	Errors     []string
}

// Scan reads the session logs of the workspace at path: the records of each
// agent, by name, with what reading met. The workspace itself may be
// reached through a link; .yesman must be a directory in it, or nothing is
// read and Scan fails. Linked or special session and agent directories and
// logs are skipped and reported in the stats, as are unreadable ones. A
// workspace without sessions has no records.
func (s *Sessions) Scan(ctx context.Context, workspace string) (map[string][]Record, DirStats, error) {
	out := map[string][]Record{}
	var st DirStats
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, st, err
	}
	defer root.Close()
	meta, err := openDir(root, ".yesman")
	if err != nil {
		return nil, st, fmt.Errorf(".yesman: %w", err)
	}
	defer meta.Close()
	sessions, err := openDir(meta, "sessions")
	if errors.Is(err, fs.ErrNotExist) {
		s.dirs = nil
		return out, st, nil
	}
	if err != nil {
		s.dirs = nil
		st.Errors = append(st.Errors, "sessions: "+err.Error())
		return out, st, nil
	}
	defer sessions.Close()
	entries, err := readDir(sessions)
	if err != nil {
		s.dirs = nil
		st.Errors = append(st.Errors, "sessions: "+err.Error())
		return out, st, nil
	}
	if s.dirs == nil {
		s.dirs = map[string]*Dir{}
	}
	seen := map[string]bool{}
	for _, e := range entries {
		// An agent's directory has its name; regular files there are not
		// agents' (delivery state lives inside each agent's directory).
		name := e.Name()
		if yesmen.ValidName(name) != nil || e.Type().IsRegular() {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, st, err
		}
		sub, err := openDir(sessions, name)
		if err != nil {
			st.Errors = append(st.Errors, name+": "+err.Error())
			continue
		}
		seen[name] = true
		d := s.dirs[name]
		if d == nil {
			d = &Dir{}
			s.dirs[name] = d
		}
		err = d.Scan(ctx, sub)
		sub.Close()
		if ctx.Err() != nil {
			return nil, st, ctx.Err()
		}
		if err != nil {
			st.Errors = append(st.Errors, name+": "+err.Error())
		}
		records, ds := d.Records()
		if len(records) > 0 {
			out[name] = records
		}
		st.Files += ds.Files
		st.Unreadable += ds.Unreadable
		st.Oversized += ds.Oversized
		for _, msg := range ds.Errors {
			st.Errors = append(st.Errors, name+"/"+msg)
		}
	}
	for name := range s.dirs {
		if !seen[name] {
			delete(s.dirs, name)
		}
	}
	return out, st, nil
}

// openDir opens a directory of parent that is a directory there, not a
// link or anything else, and is still the directory looked at once open.
// parent keeps it from leading outside, even if it is replaced meanwhile.
func openDir(parent *os.Root, name string) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	// Windows reports junctions and other name surrogates without ModeDir.
	if info.Mode().Type() != fs.ModeDir {
		return nil, errNotDir
	}
	if opening != nil {
		opening(name)
	}
	sub, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	h, err := sub.Open(".")
	if err != nil {
		sub.Close()
		return nil, err
	}
	got, err := h.Stat()
	h.Close()
	if err == nil && (!got.IsDir() || !os.SameFile(info, got)) {
		err = errChanged
	}
	if err != nil {
		sub.Close()
		return nil, err
	}
	return sub, nil
}

func readDir(dir *os.Root) ([]os.DirEntry, error) {
	h, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	defer h.Close()
	return h.ReadDir(-1)
}

// Scan reads the session files of an agent's directory: new ones whole,
// grown ones from where the last read ended, and replaced, rewritten or
// shorter ones again. Logs that are links or special files are not read,
// and are reported.
func (d *Dir) Scan(ctx context.Context, dir *os.Root) error {
	entries, err := readDir(dir)
	if err != nil {
		d.files = nil
		return err
	}
	if d.files == nil {
		d.files = map[string]*file{}
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
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
		f.read(ctx, dir, e.Name())
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

// read brings f up to date with the file name in dir. Only a regular file
// with a single link is read, and only the file that was looked at.
func (f *file) read(ctx context.Context, dir *os.Root, name string) {
	checked := time.Now()
	look, err := dir.Lstat(name)
	if err != nil {
		f.fail(err)
		return
	}
	if !look.Mode().IsRegular() {
		f.fail(errNotFile)
		return
	}
	if opening != nil {
		opening(name)
	}
	h, err := dir.OpenFile(name, openFlags, 0)
	if err != nil {
		f.fail(err)
		return
	}
	defer h.Close()
	info, err := h.Stat()
	if err != nil {
		f.fail(err)
		return
	}
	if !info.Mode().IsRegular() || !os.SameFile(look, info) {
		f.fail(errChanged)
		return
	}
	if n, ok := hardLinks(h, info); ok && n > 1 {
		f.fail(errHardLink)
		return
	}
	same := f.err == nil && f.info != nil && os.SameFile(f.info, info)
	if same && !f.racy && info.Size() == f.size && info.ModTime().Equal(f.mod) {
		return
	}
	// Only the same file, grown, and as it was up to where it was read,
	// goes on from there; anything else is read again.
	if !same || info.Size() <= f.size || !f.continues(h) {
		f.reset()
	}
	if _, err := h.Seek(f.offset, io.SeekStart); err != nil {
		f.fail(err)
		return
	}
	if err := f.parse(ctx, h, name); err != nil {
		f.fail(err)
		return
	}
	// The size and time read are those before reading: a write meanwhile
	// shows as a change next time.
	f.info, f.size, f.mod = info, info.Size(), info.ModTime()
	f.racy = !info.ModTime().Before(checked.Add(-racyWindow))
	if len(f.head) < headLen {
		f.head = readAt(h, 0, headLen)
	}
	f.tail = f.tailAt(h)
}

// fail forgets what was read and records why the file is not read.
func (f *file) fail(err error) {
	f.reset()
	f.err = err
}

// tailAt reads the last bytes before the offset.
func (f *file) tailAt(h *os.File) []byte {
	n := min(f.offset, headLen)
	return readAt(h, f.offset-n, int(n))
}

func (f *file) reset() {
	*f = file{parsed: f.parsed}
}

// continues reports whether the file, which grew, starts and goes on to the
// offset as it did. pi appends to session files, and rewrites them whole
// only to migrate them, which changes their header.
func (f *file) continues(h *os.File) bool {
	if f.offset == 0 {
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
func (f *file) parse(ctx context.Context, r io.Reader, name string) error {
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
		f.parsed += size
		switch {
		case skip || size > maxLine:
			f.oversized++
		case len(line) > 0:
			line = append(line, chunk...)
			f.line(bytes.TrimSpace(line), name, start)
		default:
			f.line(bytes.TrimSpace(chunk), name, start)
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

// line reads one line, at offset start of the file name.
func (f *file) line(b []byte, name string, start int64) {
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
		r.Ended = ended(r.At, written, r.DurationMs)
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
		r.key = "l|" + name + "|" + strconv.FormatInt(start, 10)
	}
	r.Tokens = t
	f.records = append(f.records, r)
}

// ended is when a response is known to have ended: the later of when pi
// wrote its entry and its start plus its duration, among those recorded and
// believable. pi writes the entry once the response has ended, so the
// entry's time is no earlier than the end; one before the start is not
// believed. Zero when neither tells.
func ended(start, written time.Time, durationMs int64) time.Time {
	var end time.Time
	if !written.IsZero() && !written.Before(start) {
		end = written
	}
	if durationMs >= 0 && durationMs <= maxDuration.Milliseconds() && !start.IsZero() {
		end = later(end, start.Add(time.Duration(durationMs)*time.Millisecond))
	}
	return end
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
