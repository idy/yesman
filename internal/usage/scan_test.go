package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The lines below follow pi's session format (docs/session-format.md and
// docs/message-types.md of pi 1.5.2), with made-up values.

var t0 = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func header(id string) string {
	return fmt.Sprintf(`{"type":"session","version":3,"id":%q,"timestamp":%q,"cwd":"/w"}`, id, iso(t0))
}

type resp struct {
	id                         string
	provider, model, effort    string // effort "" leaves thinkingLevel out
	responseModel              string
	at                         time.Time // request start
	duration                   int64     // -1 leaves durationMs out
	input, output, read, write int64
	total                      int64 // 0: input+output+read+write
	reasoning                  int64
	stop                       string
}

func (r resp) line() string {
	total := r.total
	if total == 0 {
		total = r.input + r.output + r.read + r.write
	}
	m := map[string]any{
		"role":     "assistant",
		"content":  []any{map[string]any{"type": "text", "text": "secret text never read"}},
		"api":      "openai-completions",
		"provider": r.provider,
		"model":    r.model,
		"usage": map[string]any{
			"input": r.input, "output": r.output, "cacheRead": r.read, "cacheWrite": r.write,
			"reasoning": r.reasoning, "totalTokens": total,
			"cost": map[string]any{"input": 1, "output": 2, "cacheRead": 0, "cacheWrite": 0, "total": 3},
		},
		"stopReason": r.stop,
		"timestamp":  r.at.UnixMilli(),
	}
	if r.stop == "" {
		m["stopReason"] = "toolUse"
	}
	if r.effort != "" {
		m["thinkingLevel"] = r.effort
	}
	if r.responseModel != "" {
		m["responseModel"] = r.responseModel
	}
	if r.duration >= 0 {
		m["durationMs"] = r.duration
	}
	end := r.at.Add(time.Duration(max(r.duration, 0)) * time.Millisecond)
	return entryLine("message", r.id, end, map[string]any{"message": m})
}

func entryLine(kind, id string, at time.Time, fields map[string]any) string {
	e := map[string]any{"type": kind, "id": id, "parentId": nil, "timestamp": iso(at)}
	for k, v := range fields {
		e[k] = v
	}
	b, err := json.Marshal(e)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func userLine(id string, at time.Time, text string) string {
	return entryLine("message", id, at, map[string]any{"message": map[string]any{"role": "user", "content": text, "timestamp": at.UnixMilli()}})
}

func usageMap(input, output, read, write int64) map[string]any {
	return map[string]any{"input": input, "output": output, "cacheRead": read, "cacheWrite": write, "totalTokens": input + output + read + write,
		"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}}
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendText(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func scan(t *testing.T, d *Dir, dir string) ([]Record, DirStats) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := d.Scan(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	return d.Records()
}

// steady treats files as changed long enough before they are read that a
// later change cannot keep their time, as for logs not being written now.
// A file time may be up to a second after the clock read just before it:
// Windows reads the clock more coarsely than it stamps files.
func steady(t *testing.T) {
	old := racyWindow
	racyWindow = -time.Second
	t.Cleanup(func() { racyWindow = old })
}

func TestScanReadsEachKindOfUsage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-11-14T10-00-00-000Z_a.jsonl")
	writeLines(t, path,
		header("a"),
		entryLine("model_change", "mc000001", t0, map[string]any{"provider": "p", "modelId": "m1"}),
		entryLine("thinking_level_change", "tl000001", t0, map[string]any{"thinkingLevel": "xhigh"}),
		userLine("u0000001", t0, `say "usage": 1`),
		resp{id: "a0000001", provider: "p", model: "m1", effort: "xhigh", at: t0, duration: 2000, input: 100, output: 40, read: 1000, write: 10, reasoning: 30, stop: "toolUse"}.line(),
		entryLine("message", "r0000001", t0.Add(3*time.Second), map[string]any{"message": map[string]any{"role": "toolResult", "toolCallId": "c", "toolName": "x", "content": []any{}, "isError": false, "timestamp": t0.Add(3 * time.Second).UnixMilli(), "usage": usageMap(5, 6, 0, 0)}}),
		resp{id: "a0000002", provider: "p", model: "m1", responseModel: "m1-2026", at: t0.Add(4 * time.Second), duration: -1, input: 7, output: 0, stop: "aborted"}.line(),
		entryLine("usage", "us000001", t0.Add(5*time.Second), map[string]any{"kind": "cache_warm", "provider": "p", "model": "m1", "usage": usageMap(0, 0, 500, 0)}),
		entryLine("compaction", "co000001", t0.Add(6*time.Second), map[string]any{"summary": "secret summary", "firstKeptEntryId": "a0000001", "tokensBefore": 9, "usage": usageMap(50, 20, 0, 0)}),
		entryLine("compaction", "co000002", t0.Add(7*time.Second), map[string]any{"summary": "no usage", "firstKeptEntryId": "a0000001", "tokensBefore": 9}),
	)
	records, st := scan(t, &Dir{}, dir)
	if st.Files != 1 || st.Unreadable != 0 || len(st.Errors) != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if len(records) != 5 {
		t.Fatalf("records = %d: %+v", len(records), records)
	}
	r := records[0]
	if r.Kind != KindResponse || r.Provider != "p" || r.Model != "m1" || r.Effort != "xhigh" || !r.At.Equal(t0) || r.DurationMs != 2000 || r.Stop != "toolUse" {
		t.Errorf("response = %+v", r)
	}
	if r.Tokens != (Tokens{Input: 100, Output: 40, CacheRead: 1000, CacheWrite: 10, Total: 1150}) {
		t.Errorf("tokens = %+v", r.Tokens)
	}
	if r := records[1]; r.Kind != KindTool || r.Model != "" || r.Total != 11 {
		t.Errorf("tool usage = %+v", r)
	}
	if r := records[2]; r.Effort != "" || r.DurationMs != -1 || r.Stop != "aborted" || r.ResponseModel != "m1-2026" || r.Input != 7 {
		t.Errorf("legacy aborted response = %+v", r)
	}
	if r := records[3]; r.Kind != KindEntry || r.Model != "m1" || r.Effort != "" || r.CacheRead != 500 || !r.At.Equal(t0.Add(5*time.Second)) {
		t.Errorf("usage entry = %+v", r)
	}
	if r := records[4]; r.Kind != KindSummary || r.Model != "" || r.Total != 70 {
		t.Errorf("compaction usage = %+v", r)
	}
}

// A file read again only reads what was appended, and a line still being
// written waits for its newline.
func TestScanReadsOnlyWhatWasAppended(t *testing.T) {
	steady(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	writeLines(t, path, header("s"), resp{id: "a1", provider: "p", model: "m", effort: "high", at: t0, duration: 1000, output: 10}.line())
	d := &Dir{}
	if records, _ := scan(t, d, dir); len(records) != 1 {
		t.Fatalf("first read = %d", len(records))
	}
	f := d.files["s.jsonl"]
	offset := f.offset

	second := resp{id: "a2", provider: "p", model: "m", effort: "high", at: t0.Add(time.Minute), duration: 1000, output: 20}.line()
	appendText(t, path, second[:40])
	if records, _ := scan(t, d, dir); len(records) != 1 || f.offset != offset {
		t.Fatalf("partial line read: %d records, offset %d -> %d", len(records), offset, f.offset)
	}
	appendText(t, path, second[40:]+"\n")
	records, _ := scan(t, d, dir)
	if len(records) != 2 || records[1].Output != 20 {
		t.Fatalf("after completing the line: %+v", records)
	}
	// Reading again without a change keeps everything once, and only the
	// appended bytes were parsed.
	size := f.size
	if records, _ := scan(t, d, dir); len(records) != 2 || f.parsed != size {
		t.Fatalf("unchanged read = %d, parsed %d of %d bytes", len(records), f.parsed, size)
	}
}

// A rewritten or shortened file is read again from the start.
func TestScanRereadsRewrittenFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	a := resp{id: "a1", provider: "p", model: "m", effort: "high", at: t0, duration: 1000, output: 10}.line()
	b := resp{id: "a2", provider: "p", model: "m", effort: "high", at: t0.Add(time.Minute), duration: 1000, output: 20}.line()
	writeLines(t, path, header("s"), a, b)
	d := &Dir{}
	scan(t, d, dir)

	writeLines(t, path, header("s"), a)
	if records, _ := scan(t, d, dir); len(records) != 1 || records[0].Output != 10 {
		t.Fatalf("after shortening: %+v", records)
	}
	// Same start, different middle, longer: a rewrite, not an append.
	c := resp{id: "a3", provider: "p", model: "m", effort: "low", at: t0.Add(2 * time.Minute), duration: 1000, output: 30}.line()
	writeLines(t, path, header("s"), c, b)
	records, _ := scan(t, d, dir)
	if len(records) != 2 || records[0].Effort != "low" || records[1].Output != 20 {
		t.Fatalf("after rewrite: %+v", records)
	}
	// A migrated file starts differently.
	writeLines(t, path, `{"type":"session","version":2,"id":"s","timestamp":"2026-11-14T10:00:00.000Z","cwd":"/w"}`, a, b, c)
	if records, _ := scan(t, d, dir); len(records) != 3 {
		t.Fatalf("after migration: %d", len(records))
	}
}

// Bad lines are counted and skipped; the records around them stay.
func TestScanCountsUnreadableLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	good := resp{id: "a1", provider: "p", model: "m", effort: "high", at: t0, duration: 1000, output: 10}.line()
	crashed := good[:30] + resp{id: "a2", provider: "p", model: "m", effort: "high", at: t0, duration: 1000, output: 99}.line()
	negative := strings.Replace(resp{id: "a3", provider: "p", model: "m", at: t0, output: 1}.line(), `"output":1`, `"output":-1`, 1)
	text := strings.Replace(resp{id: "a4", provider: "p", model: "m", at: t0, output: 1}.line(), `"output":1`, `"output":"1"`, 1)
	noTime := strings.Replace(entryLine("usage", "u1", t0, map[string]any{"provider": "p", "model": "m", "usage": usageMap(1, 1, 0, 0)}), iso(t0), "not a time", 1)
	writeLines(t, path, header("s"), "{not json", crashed, negative, text, noTime, good, "", `{"type":"label","id":"l1"}`)
	records, st := scan(t, &Dir{}, dir)
	if len(records) != 1 || records[0].Output != 10 {
		t.Fatalf("records = %+v", records)
	}
	if st.Unreadable != 5 {
		t.Fatalf("unreadable = %d, want 5", st.Unreadable)
	}
}

func TestScanSkipsOversizedLines(t *testing.T) {
	old := maxLine
	maxLine = 300 << 10
	t.Cleanup(func() { maxLine = old })
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	huge := userLine("u1", t0, strings.Repeat("x", 1<<20))
	big := strings.Replace(resp{id: "a0", provider: "p", model: "m", at: t0, duration: 1, output: 5}.line(), "secret text never read", strings.Repeat("y", 290<<10), 1)
	writeLines(t, path, header("s"), huge, big, resp{id: "a1", provider: "p", model: "m", at: t0, duration: 1000, output: 10}.line())
	records, st := scan(t, &Dir{}, dir)
	if st.Oversized != 1 || len(records) != 2 || records[0].Output != 5 || records[1].Output != 10 {
		t.Fatalf("records = %+v, stats = %+v", records, st)
	}
}

// A forked or branched session copies entries into a new file with their
// IDs: each counts once.
func TestScanCountsCopiedEntriesOnce(t *testing.T) {
	dir := t.TempDir()
	a := resp{id: "a1", provider: "p", model: "m", effort: "high", at: t0, duration: 1000, output: 10}.line()
	b := resp{id: "a2", provider: "p", model: "m", effort: "high", at: t0.Add(time.Minute), duration: 1000, output: 20}.line()
	c := resp{id: "a3", provider: "p", model: "m", effort: "high", at: t0.Add(2 * time.Minute), duration: 1000, output: 30}.line()
	writeLines(t, filepath.Join(dir, "2026-11-14T10-00-00-000Z_one.jsonl"), header("one"), a, b)
	writeLines(t, filepath.Join(dir, "2026-11-14T11-00-00-000Z_two.jsonl"), header("two"), a, b, c)
	records, st := scan(t, &Dir{}, dir)
	if st.Files != 2 || len(records) != 3 {
		t.Fatalf("records = %d, files = %d", len(records), st.Files)
	}
	// The same ID at another time is another entry.
	d := t.TempDir()
	other := resp{id: "a1", provider: "p", model: "m", effort: "high", at: t0.Add(time.Hour), duration: 1000, output: 40}.line()
	writeLines(t, filepath.Join(d, "x.jsonl"), header("x"), a, other)
	if records, _ := scan(t, &Dir{}, d); len(records) != 2 {
		t.Fatalf("distinct entries with one ID = %d", len(records))
	}
}

// Only regular .jsonl files are read; a link is reported, not followed.
// Removed files leave with their records.
func TestScanReadsRegularSessionFilesOnly(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere.jsonl")
	writeLines(t, outside, header("o"), resp{id: "o1", provider: "p", model: "m", at: t0, output: 1}.line())
	if err := os.Symlink(outside, filepath.Join(dir, "link.jsonl")); err != nil {
		t.Skip("no symbolic links here:", err)
	}
	writeLines(t, filepath.Join(dir, "delivery.json"), `{"usage": 1}`)
	keep := filepath.Join(dir, "s.jsonl")
	writeLines(t, keep, header("s"), resp{id: "a1", provider: "p", model: "m", at: t0, output: 2}.line())
	d := &Dir{}
	records, st := scan(t, d, dir)
	if st.Files != 2 || len(records) != 1 || records[0].Output != 2 || len(st.Errors) != 1 || !strings.HasPrefix(st.Errors[0], "link.jsonl: not a regular file") {
		t.Fatalf("records = %+v, stats = %+v", records, st)
	}
	if err := os.Remove(keep); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "link.jsonl")); err != nil {
		t.Fatal(err)
	}
	if records, st := scan(t, d, dir); st.Files != 0 || len(records) != 0 || len(st.Errors) != 0 {
		t.Fatalf("after removal: %+v %+v", records, st)
	}
}

func TestScanStopsWhenCancelled(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, filepath.Join(dir, "s.jsonl"), header("s"), resp{id: "a1", provider: "p", model: "m", at: t0, output: 2}.line())
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&Dir{}).Scan(ctx, root); err == nil {
		t.Fatal("cancelled scan succeeded")
	}
}
