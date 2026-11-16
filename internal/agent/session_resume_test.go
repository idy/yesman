package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func writeSession(t *testing.T, path, header string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(header+"\n"+`{"type":"message","id":"a1","parentId":null,"timestamp":"t","message":{"role":"user","content":"hi"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func sessionHeader(cwd string) string {
	return `{"type":"session","version":3,"id":"s","timestamp":"t","cwd":` + quote(cwd) + `}`
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// A workspace whose absolute path changed, as when the volume holding it
// mounts under another name, must keep resuming its agents' sessions: pi's
// --continue would match none of them and silently start an empty one.
func TestResumeArgs(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	ws := t.TempDir()
	gone := filepath.Join(t.TempDir(), "renamed-volume", "alma")

	t.Run("no session", func(t *testing.T) {
		if got := resumeArgs(filepath.Join(ws, "missing"), ws); got != nil {
			t.Fatalf("missing dir: %v", got)
		}
		empty := t.TempDir()
		os.WriteFile(filepath.Join(empty, "delivery.json"), []byte("{}"), 0o600)
		if got := resumeArgs(empty, ws); got != nil {
			t.Fatalf("no .jsonl: %v", got)
		}
	})
	t.Run("same path opens the newest", func(t *testing.T) {
		dir := t.TempDir()
		old, newest := filepath.Join(dir, "2026-01-01_a.jsonl"), filepath.Join(dir, "2026-01-02_b.jsonl")
		writeSession(t, old, sessionHeader(ws), base)
		writeSession(t, newest, sessionHeader(ws), base.Add(time.Minute))
		if got := resumeArgs(dir, ws); !slices.Equal(got, []string{"--session", newest}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("moved workspace forks the newest", func(t *testing.T) {
		dir := t.TempDir()
		old := filepath.Join(dir, "2026-01-01_a.jsonl")
		writeSession(t, old, sessionHeader(gone), base)
		if got := resumeArgs(dir, ws); !slices.Equal(got, []string{"--fork", old}) {
			t.Fatalf("got %v", got)
		}
		// Once forked, the copy at the current path is newest and is opened;
		// no further copies are made on later restarts.
		fork := filepath.Join(dir, "2026-01-02_fork.jsonl")
		writeSession(t, fork, `{"type":"session","version":3,"id":"f","timestamp":"t","cwd":`+quote(ws)+`,"parentSession":`+quote(old)+`}`, base.Add(time.Minute))
		if got := resumeArgs(dir, ws); !slices.Equal(got, []string{"--session", fork}) {
			t.Fatalf("after fork got %v", got)
		}
	})
	t.Run("newest wins over an older one at this path", func(t *testing.T) {
		// Moved away and back: the newest conversation is the one recorded
		// at the other path, not the stale one recorded here.
		dir := t.TempDir()
		stale, newest := filepath.Join(dir, "a.jsonl"), filepath.Join(dir, "b.jsonl")
		writeSession(t, stale, sessionHeader(ws), base)
		writeSession(t, newest, sessionHeader(gone), base.Add(time.Minute))
		if got := resumeArgs(dir, ws); !slices.Equal(got, []string{"--fork", newest}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("another path to the same directory", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need privileges on Windows")
		}
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(ws, link); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		s := filepath.Join(dir, "a.jsonl")
		writeSession(t, s, sessionHeader(link), base)
		if got := resumeArgs(dir, ws); !slices.Equal(got, []string{"--session", s}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("unreadable newest is skipped like pi", func(t *testing.T) {
		dir := t.TempDir()
		good := filepath.Join(dir, "a.jsonl")
		writeSession(t, good, sessionHeader(ws), base)
		bad := filepath.Join(dir, "b.jsonl")
		os.WriteFile(bad, []byte("not json\n"+`{"type":"message","id":"x"}`+"\n"), 0o644)
		os.Chtimes(bad, base.Add(time.Minute), base.Add(time.Minute))
		blank := filepath.Join(dir, "c.jsonl")
		os.WriteFile(blank, nil, 0o644)
		os.Chtimes(blank, base.Add(2*time.Minute), base.Add(2*time.Minute))
		addSpecialSessionFiles(t, dir, good)
		done := make(chan []string, 1)
		go func() { done <- resumeArgs(dir, ws) }()
		select {
		case got := <-done:
			if !slices.Equal(got, []string{"--session", good}) {
				t.Fatalf("got %v", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("blocked on a special file")
		}
	})
	t.Run("leading blank and malformed lines before the header", func(t *testing.T) {
		dir := t.TempDir()
		s := filepath.Join(dir, "a.jsonl")
		os.WriteFile(s, []byte("\n{broken\nnull\n"+sessionHeader(gone)+"\n"), 0o644)
		if got := resumeArgs(dir, ws); !slices.Equal(got, []string{"--fork", s}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("nothing pi can read leaves the choice to pi", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "a.jsonl"), []byte(`{"type":"message","id":"x"}`+"\n"), 0o644)
		if got := resumeArgs(dir, ws); !slices.Equal(got, []string{"--continue"}) {
			t.Fatalf("got %v", got)
		}
	})
}
