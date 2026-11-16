package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// copySession makes pi's --fork copy, but whole or not at all: an
// interrupted copy must never be a session the next start resumes.
func TestCopySession(t *testing.T) {
	ws := t.TempDir()
	gone := filepath.Join(t.TempDir(), "H004", "alma")
	dir := t.TempDir()
	src := filepath.Join(dir, "2026-11-13T00-00-00-000Z_old.jsonl")
	header := `{"type":"session","version":2,"id":"old","timestamp":"t","cwd":` + quote(gone) + `,"extra":1}`
	body := `{"type":"message","id":"a1","parentId":null,"timestamp":"t","message":{"role":"user","content":[{"type":"text","text":"中文 \u00e9 \"q\""},{"type":"image","mimeType":"image/png","data":"iVBORw0KGgo="}]},"unknown":{"kept":true}}` + "\r\n" +
		"{broken\n\n" +
		`{"type":"message","id":"a2","parentId":"a1","timestamp":"t","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`
	// pi skips a bad line and blank lines before the header too.
	data := []byte("{bad\n\n" + header + "\n" + body)
	if err := os.WriteFile(src, data, 0o640); err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(data)

	t.Run("whole copy", func(t *testing.T) {
		got, err := copySession(context.Background(), dir, src, ws)
		if err != nil {
			t.Fatal(err)
		}
		copied, _ := os.ReadFile(got)
		line, rest, _ := bytes.Cut(copied, []byte("\n"))
		var h map[string]any
		if err := json.Unmarshal(line, &h); err != nil {
			t.Fatal(err)
		}
		real, _ := filepath.EvalSymlinks(ws)
		valid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
		id, _ := h["id"].(string)
		if h["type"] != "session" || h["version"] != 2.0 || h["cwd"] != real || h["parentSession"] != src || !valid.MatchString(id) || h["extra"] != nil {
			t.Fatalf("header %s", line)
		}
		if !strings.HasSuffix(filepath.Base(got), "_"+id+".jsonl") {
			t.Fatalf("named %s", got)
		}
		// The entries are src's after its header, byte for byte, ended.
		if string(rest) != body+"\n" {
			t.Fatalf("entries %q", rest)
		}
		if after, _ := os.ReadFile(src); sha256.Sum256(after) != before {
			t.Fatal("the original changed")
		}
		if stray, _ := filepath.Glob(filepath.Join(dir, ".yesman-fork-*")); len(stray) != 0 {
			t.Fatalf("staging left: %v", stray)
		}
		// The copy is the session the next start resumes.
		if args := resumeArgs(dir, ws); !slices.Equal(args, []string{"--session", got}) {
			t.Fatalf("resumeArgs = %v", args)
		}
		os.Remove(got)
	})

	t.Run("interrupted", func(t *testing.T) {
		boom := errors.New("no space left on device")
		for _, at := range []int64{1, int64(len(body) / 2), int64(len(body))} {
			forkCopyFault = func(written int64) error {
				if written >= at {
					return boom
				}
				return nil
			}
			_, err := copySession(context.Background(), dir, src, ws)
			forkCopyFault = nil
			if !errors.Is(err, boom) {
				t.Fatalf("at %d: %v", at, err)
			}
			if names, _ := filepath.Glob(filepath.Join(dir, "*")); len(names) != 1 {
				t.Fatalf("at %d: left %v", at, names)
			}
			if args := resumeArgs(dir, ws); !slices.Equal(args, []string{"--fork", src}) {
				t.Fatalf("at %d: resumeArgs = %v", at, args)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := copySession(ctx, dir, src, ws); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled: %v", err)
		}
		if names, _ := filepath.Glob(filepath.Join(dir, "*")); len(names) != 1 {
			t.Fatalf("cancelled: left %v", names)
		}
		if after, _ := os.ReadFile(src); sha256.Sum256(after) != before {
			t.Fatal("the original changed")
		}
	})
}
