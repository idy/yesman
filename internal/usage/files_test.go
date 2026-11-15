package usage

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// warmIsCold checks that a Dir read before, read again, holds what a fresh
// read of the directory does.
func warmIsCold(t *testing.T, d *Dir, dir, label string) []Record {
	t.Helper()
	warm, wst := scan(t, d, dir)
	cold, cst := scan(t, &Dir{}, dir)
	if !reflect.DeepEqual(warm, cold) || !reflect.DeepEqual(wst, cst) {
		t.Fatalf("%s: warm %+v (%+v), fresh %+v (%+v)", label, warm, wst, cold, cst)
	}
	return warm
}

// replacementLog is a log whose one response sits between ordinary entries
// that keep its first and last bytes the same when the model changes.
func replacementLog(model string) string {
	prefix := header("s") + "\n" + userLine("u1", t0, strings.Repeat("a", 400)) + "\n"
	suffix := userLine("u2", t0.Add(time.Hour), strings.Repeat("z", 400)) + "\n"
	return prefix + resp{id: "a1", provider: "p", model: model, effort: "high", at: t0, duration: 1000, output: 10}.line() + "\n" + suffix
}

// A log replaced by another file of the same size, by renaming, or
// rewritten in place, is read again: its first and last bytes and its size
// do not make it the same log. This holds even when its time is kept.
func TestScanRereadsReplacedLogs(t *testing.T) {
	for _, settle := range []bool{false, true} {
		t.Run(map[bool]string{false: "just written", true: "written earlier"}[settle], func(t *testing.T) {
			if settle {
				steady(t)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "s.jsonl")
			if err := os.WriteFile(path, []byte(replacementLog("old")), 0o644); err != nil {
				t.Fatal(err)
			}
			d := &Dir{}
			scan(t, d, dir)

			// Renamed over it, with a later time.
			replace := func(model string, keepTime bool) {
				t.Helper()
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				tmp := filepath.Join(dir, "replacement.tmp")
				if err := os.WriteFile(tmp, []byte(replacementLog(model)), 0o644); err != nil {
					t.Fatal(err)
				}
				stamp := info.ModTime().Add(time.Second)
				if keepTime {
					stamp = info.ModTime()
				}
				if err := os.Chtimes(tmp, stamp, stamp); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(tmp, path); err != nil {
					t.Fatal(err)
				}
			}
			replace("new", false)
			if r := warmIsCold(t, d, dir, "renamed over"); len(r) != 1 || r[0].Model != "new" {
				t.Fatalf("renamed over: %+v", r)
			}
			// Renamed over with the very same size and time: another file.
			replace("two", true)
			if r := warmIsCold(t, d, dir, "same size and time"); len(r) != 1 || r[0].Model != "two" {
				t.Fatalf("same size and time: %+v", r)
			}

			// Rewritten in place, same size, the time changing.
			rewrite := func(model string, keepTime bool) {
				t.Helper()
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteString(replacementLog(model)); err != nil {
					t.Fatal(err)
				}
				f.Close()
				stamp := info.ModTime().Add(time.Second)
				if keepTime {
					stamp = info.ModTime()
				}
				if err := os.Chtimes(path, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			rewrite("six", false)
			if r := warmIsCold(t, d, dir, "rewritten in place"); len(r) != 1 || r[0].Model != "six" {
				t.Fatalf("rewritten in place: %+v", r)
			}
			if !settle {
				// Rewritten in place while its time stays, as a file system
				// keeping coarse times does within one tick: it changed too
				// recently to trust its time, so it is read again.
				rewrite("ten", true)
				if r := warmIsCold(t, d, dir, "same time, just written"); len(r) != 1 || r[0].Model != "ten" {
					t.Fatalf("same time: %+v", r)
				}
			}
		})
	}
}

// Shrinking, growing with another start, and growing with other bytes
// before where it was read are all read again; growing as an append reads
// only what was added. Ghost records of the old version do not remain.
func TestScanTellsAppendsFromRewrites(t *testing.T) {
	steady(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	a := resp{id: "a1", provider: "p", model: "m", effort: "high", at: t0, duration: 1000, output: 10}.line()
	b := resp{id: "a2", provider: "p", model: "m", effort: "high", at: t0.Add(time.Minute), duration: 1000, output: 20}.line()
	c := resp{id: "a3", provider: "p", model: "x", effort: "low", at: t0.Add(2 * time.Minute), duration: 1000, output: 30}.line()
	writeLines(t, path, header("s"), a, b)
	// A copy of a in another log counts once, whichever version is read.
	writeLines(t, filepath.Join(dir, "fork.jsonl"), header("fork"), a)
	d := &Dir{}
	scan(t, d, dir)
	f := d.files["s.jsonl"]
	parsed := f.parsed

	appendText(t, path, c+"\n")
	if r := warmIsCold(t, d, dir, "append"); len(r) != 3 || f.parsed-parsed != int64(len(c)+1) {
		t.Fatalf("append: %d records, parsed %d more bytes", len(r), f.parsed-parsed)
	}
	for _, step := range []struct {
		label string
		lines []string
	}{
		{"shrunk", []string{header("s"), a}},
		{"grown with another header", []string{`{"type":"session","version":2,"id":"s","timestamp":"2026-11-14T10:00:00.000Z","cwd":"/w"}`, a, b, c}},
		{"grown with other bytes before the old end", []string{header("s"), strings.Replace(a, `"output":10`, `"output":11`, 1), b, c, c}},
		{"without the copied entry", []string{header("s"), b}},
	} {
		parsed = f.parsed
		writeLines(t, path, step.lines...)
		os.Chtimes(path, time.Now().Add(time.Second), time.Now().Add(time.Second))
		warmIsCold(t, d, dir, step.label)
	}
}

// A log changed too close to when it was read is read again once, then not
// while it is unchanged.
func TestScanRereadsRecentLogsOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	writeLines(t, path, header("s"), resp{id: "a1", provider: "p", model: "m", at: t0, duration: 1000, output: 10}.line())
	d := &Dir{}
	scan(t, d, dir)
	f := d.files["s.jsonl"]
	if !f.racy {
		t.Fatal("a log just written is not taken as recent")
	}
	parsed := f.parsed
	scan(t, d, dir)
	if f.parsed != 2*parsed {
		t.Fatalf("recent log not read again: parsed %d, then %d", parsed, f.parsed)
	}
	// Its time set well back is a change, read once more; then it is steady.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	scan(t, d, dir)
	if f.racy || f.parsed != 3*parsed {
		t.Fatalf("after its time went back: racy %v, parsed %d", f.racy, f.parsed)
	}
	scan(t, d, dir)
	if f.parsed != 3*parsed {
		t.Fatalf("steady log read again: parsed %d", f.parsed)
	}
}

// workspace makes a workspace with an agent's log, and returns its path.
func workspace(t *testing.T, agents map[string]string) string {
	t.Helper()
	ws := t.TempDir()
	for agent, line := range agents {
		writeLines(t, filepath.Join(ws, ".yesman", "sessions", agent, "s.jsonl"), header(agent), line)
	}
	return ws
}

func outsideLog(t *testing.T, output int64) string {
	t.Helper()
	dir := t.TempDir()
	writeLines(t, filepath.Join(dir, "w", "outside.jsonl"), header("o"), resp{id: "o1", provider: "p", model: "outside", at: t0, duration: 1000, output: output}.line())
	writeLines(t, filepath.Join(dir, "outside.jsonl"), header("o"), resp{id: "o2", provider: "p", model: "outside", at: t0, duration: 1000, output: output}.line())
	return dir
}

func link(t *testing.T, target, at string) {
	t.Helper()
	if err := os.RemoveAll(at); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, at); err != nil {
		t.Skip("no symbolic links here:", err)
	}
}

func sessionsScan(t *testing.T, s *Sessions, ws string) (map[string][]Record, DirStats, error) {
	t.Helper()
	return s.Scan(context.Background(), ws)
}

func outputs(records map[string][]Record) int64 {
	var n int64
	for _, rs := range records {
		for _, r := range rs {
			n += r.Output
		}
	}
	return n
}

func hasError(st DirStats, prefix string) bool {
	for _, e := range st.Errors {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// Nothing in a workspace is followed: a linked .yesman fails the read, and
// linked sessions, agents' directories and logs are reported and skipped,
// wherever they lead, while the rest is read. The workspace itself may be
// reached through a link.
func TestSessionsReadOnlyTheWorkspace(t *testing.T) {
	line := resp{id: "a1", provider: "p", model: "m", at: t0, duration: 1000, output: 5}.line()
	outside := outsideLog(t, 777)

	ws := workspace(t, map[string]string{"w": line, "v": line})
	alias := filepath.Join(t.TempDir(), "alias")
	link(t, ws, alias)
	var s Sessions
	records, st, err := sessionsScan(t, &s, alias)
	if err != nil || outputs(records) != 10 || len(st.Errors) != 0 || st.Files != 2 {
		t.Fatalf("through an alias: %v %+v %v", outputs(records), st, err)
	}

	// The agent's directory a link, out of the workspace or within it.
	for _, target := range []string{filepath.Join(outside, "w"), filepath.Join(ws, ".yesman", "sessions", "v")} {
		link(t, target, filepath.Join(ws, ".yesman", "sessions", "w"))
		records, st, err = sessionsScan(t, &s, ws)
		if err != nil || outputs(records) != 5 || !hasError(st, "w: not a directory") {
			t.Fatalf("agent link to %s: %v %+v %v", target, outputs(records), st, err)
		}
	}
	// A log a link, out of the workspace or to another log of it.
	os.Remove(filepath.Join(ws, ".yesman", "sessions", "w"))
	writeLines(t, filepath.Join(ws, ".yesman", "sessions", "w", "s.jsonl"), header("w"), line)
	for _, target := range []string{filepath.Join(outside, "outside.jsonl"), filepath.Join(ws, ".yesman", "sessions", "v", "s.jsonl")} {
		link(t, target, filepath.Join(ws, ".yesman", "sessions", "w", "linked.jsonl"))
		records, st, err = sessionsScan(t, &s, ws)
		if err != nil || outputs(records) != 10 || !hasError(st, "w/linked.jsonl: not a regular file") {
			t.Fatalf("log link to %s: %v %+v %v", target, outputs(records), st, err)
		}
	}
	os.Remove(filepath.Join(ws, ".yesman", "sessions", "w", "linked.jsonl"))
	// sessions a link: out of the workspace, or to another workspace's.
	other := workspace(t, map[string]string{"w": line})
	for _, target := range []string{outside, filepath.Join(other, ".yesman", "sessions")} {
		moved := filepath.Join(t.TempDir(), "sessions")
		if err := os.Rename(filepath.Join(ws, ".yesman", "sessions"), moved); err != nil {
			t.Fatal(err)
		}
		link(t, target, filepath.Join(ws, ".yesman", "sessions"))
		records, st, err = sessionsScan(t, &s, ws)
		if err != nil || len(records) != 0 || !hasError(st, "sessions: not a directory") {
			t.Fatalf("sessions link to %s: %v %+v %v", target, records, st, err)
		}
		os.Remove(filepath.Join(ws, ".yesman", "sessions"))
		if err := os.Rename(moved, filepath.Join(ws, ".yesman", "sessions")); err != nil {
			t.Fatal(err)
		}
	}
	// .yesman a link: nothing is read.
	moved := filepath.Join(t.TempDir(), "meta")
	if err := os.Rename(filepath.Join(ws, ".yesman"), moved); err != nil {
		t.Fatal(err)
	}
	link(t, moved, filepath.Join(ws, ".yesman"))
	if records, _, err := sessionsScan(t, &s, ws); err == nil || !strings.HasPrefix(err.Error(), ".yesman: not a directory") || records != nil {
		t.Fatalf(".yesman link: %v %v", records, err)
	}
	// No sessions yet: nothing, without an error.
	if records, st, err := sessionsScan(t, &Sessions{}, t.TempDir()+string(filepath.Separator)); err == nil {
		t.Fatalf("no .yesman: %v %+v", records, st)
	}
	empty := t.TempDir()
	os.Mkdir(filepath.Join(empty, ".yesman"), 0o755)
	if records, st, err := sessionsScan(t, &Sessions{}, empty); err != nil || len(records) != 0 || len(st.Errors) != 0 {
		t.Fatalf("no sessions: %v %+v %v", records, st, err)
	}
}

// A directory or log replaced by a link between being looked at and being
// opened is not read through the link: one leading out of where it was is
// refused on opening, and one to something else there is not the
// directory or log looked at.
func TestSessionsCatchReplacementWhileOpening(t *testing.T) {
	outside := outsideLog(t, 777)
	for _, c := range []struct{ name, where string }{{"sessions", "outside"}, {"w", "outside"}, {"s.jsonl", "outside"}, {"sessions", "inside"}, {"w", "inside"}, {"s.jsonl", "inside"}} {
		t.Run(c.name+" to "+c.where, func(t *testing.T) {
			ws := t.TempDir()
			sessions := filepath.Join(ws, ".yesman", "sessions")
			writeLines(t, filepath.Join(sessions, "w", "s.jsonl"), header("w"), resp{id: "a1", provider: "p", model: "m", at: t0, duration: 1000, output: 5}.line())
			writeLines(t, filepath.Join(sessions, "v", "v.jsonl"), header("v"), resp{id: "v1", provider: "p", model: "m", at: t0, duration: 1000, output: 7}.line())
			// Other logs beside each, with output 777, that a link there
			// could lead to.
			writeLines(t, filepath.Join(ws, ".yesman", "other", "w", "s.jsonl"), header("o"), resp{id: "o1", provider: "p", model: "other", at: t0, duration: 1000, output: 777}.line())
			writeLines(t, filepath.Join(sessions, "w", "other.jsonl.bak"), header("o"), resp{id: "o2", provider: "p", model: "other", at: t0, duration: 1000, output: 777}.line())
			// Relative links inside stay inside the directory opened.
			targets := map[string][2]string{
				"sessions": {outside, "other"},
				"w":        {filepath.Join(outside, "w"), "v"},
				"s.jsonl":  {filepath.Join(outside, "outside.jsonl"), "other.jsonl.bak"},
			}
			at := map[string]string{"sessions": sessions, "w": filepath.Join(sessions, "w"), "s.jsonl": filepath.Join(sessions, "w", "s.jsonl")}[c.name]
			target := targets[c.name][0]
			if c.where == "inside" {
				target = targets[c.name][1]
			}
			swapped := false
			opening = func(got string) {
				if got == c.name && !swapped {
					swapped = true
					if err := os.Rename(at, filepath.Join(t.TempDir(), "kept")); err != nil {
						t.Error(err)
					}
					if err := os.Symlink(target, at); err != nil {
						t.Skip("no symbolic links here:", err)
					}
				}
			}
			t.Cleanup(func() { opening = nil })
			records, st, err := sessionsScan(t, &Sessions{}, ws)
			// v's own log is read unless sessions was replaced; nothing
			// with output 777 is.
			want := int64(7)
			if c.name == "sessions" {
				want = 0
			}
			if !swapped || err != nil || outputs(records) != want || len(st.Errors) != 1 {
				t.Fatalf("%s replaced: swapped %v, output %d, %+v, %v", c.name, swapped, outputs(records), st, err)
			}
			t.Logf("%s replaced while opening: %v", c.name, st.Errors)
		})
	}
}

// Logs with other hard links are reported and not read: one may be a file
// from anywhere.
func TestSessionsSkipHardLinks(t *testing.T) {
	line := resp{id: "a1", provider: "p", model: "m", at: t0, duration: 1000, output: 5}.line()
	ws := workspace(t, map[string]string{"w": line})
	outside := outsideLog(t, 777)
	if err := os.Link(filepath.Join(outside, "outside.jsonl"), filepath.Join(ws, ".yesman", "sessions", "w", "hard.jsonl")); err != nil {
		t.Skip("no hard links here:", err)
	}
	records, st, err := sessionsScan(t, &Sessions{}, ws)
	if err != nil || outputs(records) != 5 || !hasError(st, "w/hard.jsonl: has other hard links") {
		t.Fatalf("records %d, %+v, %v", outputs(records), st, err)
	}
}
