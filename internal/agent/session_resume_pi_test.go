package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// managedPiFixture runs the prepared pinned runtime against a local
// deterministic provider, with no credentials, network or user pi settings.
type managedPiFixture struct {
	cfg  Config
	temp string
}

func newManagedPiFixture(t *testing.T) *managedPiFixture {
	t.Helper()
	runtime := os.Getenv("YESMAN_TEST_RUNTIME")
	if runtime == "" {
		t.Skip("set YESMAN_TEST_RUNTIME to the prepared pinned runtime")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	config := filepath.Join(temp, "config")
	if err = os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"packages": []string{}, "extensions": []string{filepath.Join(root, "scripts", "fixtures", "delivery-provider.ts")}, "skills": []string{}, "promptTemplates": []string{}, "themes": []string{}, "defaultProjectTrust": "no", "defaultProvider": "delivery-test", "defaultModel": "m", "compaction": map[string]bool{"enabled": false}, "retry": map[string]bool{"enabled": false}}
	data, _ := json.Marshal(settings)
	if err = os.WriteFile(filepath.Join(config, "settings.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/context" {
			io.WriteString(w, "Isolated managed pi fixture")
		} else {
			io.WriteString(w, `{"env":{},"hide":[]}`)
		}
	}))
	t.Cleanup(api.Close)
	// The provider answers at once.
	gate := filepath.Join(temp, "gate")
	if err = os.WriteFile(gate, []byte("open"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &managedPiFixture{temp: temp, cfg: Config{Pi: node, Command: func(context.Context) (string, []string, error) {
		return node, []string{filepath.Join(runtime, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "cli.js")}, nil
	}, Extension: filepath.Join(root, "pi", "yesman.ts"),
		Env: append(isolatedNativeEnv(), []string{"HOME=" + temp, "USERPROFILE=" + temp, "APPDATA=" + temp, "LOCALAPPDATA=" + temp, "PI_CODING_AGENT_DIR=" + config, "YESMAN_API=" + api.URL, "DELIVERY_GATE=" + gate, "DELIVERY_AUDIT=" + filepath.Join(temp, "audit")}...)}}
}

type userTurn struct {
	Text       string
	Images     int
	Submission string
}

// userTurns lists the user messages of an agent's history as the page gets
// them: text, image count and the submission that sent each.
func userTurns(t *testing.T, ctx context.Context, m *Manager, id string) []userTurn {
	t.Helper()
	conv, err := m.Messages(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Yesman  *struct {
			Submission string `json:"submission"`
		} `json:"yesman"`
	}
	if err = json.Unmarshal(conv.Messages, &messages); err != nil {
		t.Fatal(err)
	}
	var out []userTurn
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		var blocks []struct{ Type, Text string }
		json.Unmarshal(msg.Content, &blocks)
		var u userTurn
		for _, b := range blocks {
			if b.Type == "text" {
				u.Text += b.Text
			} else if b.Type == "image" {
				u.Images++
			}
		}
		if msg.Yesman != nil {
			u.Submission = msg.Yesman.Submission
		}
		out = append(out, u)
	}
	return out
}

func sendAndSettle(t *testing.T, ctx context.Context, m *Manager, id, submission, text string, images []Image) {
	t.Helper()
	info, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	d, err := m.Submit(ctx, id, submission, text, images, info.Since)
	if err != nil || d.State == "failed" {
		t.Fatalf("submit %q: %+v %v", text, d, err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		turns := userTurns(t, ctx, m, id)
		if i, _ := m.Get(id); i.Status == Idle && len(turns) > 0 && turns[len(turns)-1].Text == text {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q was not answered", text)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func sessionFiles(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	out := map[string][32]byte{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = sha256.Sum256(data)
	}
	return out
}

// A restart after the workspace's absolute path changed — the volume holding
// the home directory mounted under another name — kept the session files but
// showed an empty history: pi's --continue, given yesman's --session-dir,
// resumes only a session recorded at the current path and otherwise silently
// starts a new one. The conversation, its images and the provenance of each
// message must survive the move, and later restarts must keep resuming it.
func TestResumeAfterWorkspaceMovesWithManagedPi(t *testing.T) {
	f := newManagedPiFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	before, after := filepath.Join(f.temp, "H004-volume"), filepath.Join(f.temp, "H002-volume")
	store := yesmen.New(filepath.Join(before, "yesmen"), filepath.Join(before, "archive"))
	if _, err := store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	cfg := f.cfg
	cfg.Yesmen = store
	m := New(cfg)
	id := ID("alice", yesmen.LeadName)
	image := Image{Type: "image", MimeType: "image/png", Data: "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGMQ7Aj9TwlmGDVg1IBRA4aLAQBbKO0Q8TW4KAAAAABJRU5ErkJggg=="}
	first, second := deliveryTestID(20001), deliveryTestID(20002)
	sendAndSettle(t, ctx, m, id, first, "before the move 中文", []Image{image})
	sendAndSettle(t, ctx, m, id, second, "second before the move", nil)
	want := userTurns(t, ctx, m, id)
	if len(want) != 2 || want[0].Submission != first || want[0].Images != 1 || want[1].Submission != second {
		t.Fatalf("fixture history: %+v", want)
	}
	m.StopAll()

	// The same workspace, now at another absolute path.
	if err := os.Rename(before, after); err != nil {
		t.Fatal(err)
	}
	moved := yesmen.New(filepath.Join(after, "yesmen"), filepath.Join(after, "archive"))
	sessions := filepath.Join(after, "yesmen", "alice", ".yesman", "sessions", "lead")
	original := sessionFiles(t, sessions)
	cfg.Yesmen = moved
	m = New(cfg)
	defer func() { m.StopAll() }()
	if got := userTurns(t, ctx, m, id); !slices.Equal(got, want) {
		t.Fatalf("history after the move = %+v, want %+v", got, want)
	}
	third := deliveryTestID(20003)
	sendAndSettle(t, ctx, m, id, third, "after the move", []Image{image, image})
	want = append(want, userTurn{"after the move", 2, third})
	if got := userTurns(t, ctx, m, id); !slices.Equal(got, want) {
		t.Fatalf("history after a new message = %+v, want %+v", got, want)
	}
	// The model got the whole conversation, not just the new message.
	audit, _ := os.ReadFile(filepath.Join(f.temp, "audit"))
	lines := strings.Split(strings.TrimSpace(string(audit)), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "before the move") || !strings.Contains(last, "after the move") {
		t.Fatalf("model context lost the earlier conversation")
	}
	m.StopAll()

	// Later restarts resume the same session; nothing is copied again and
	// the original session file is untouched.
	files := sessionFiles(t, sessions)
	for name, sum := range original {
		if files[name] != sum {
			t.Fatalf("original session %s changed", name)
		}
	}
	m = New(cfg)
	if got := userTurns(t, ctx, m, id); !slices.Equal(got, want) {
		t.Fatalf("history after another restart = %+v, want %+v", got, want)
	}
	if again := sessionFiles(t, sessions); len(again) != len(files) {
		t.Fatalf("restart made another session: %d files, had %d", len(again), len(files))
	}
}

// A copy interrupted at the move, by a full disk, a crash or a kill, must
// not become the session the next start resumes: pi's --fork wrote its copy
// in place, and the next start resumed the shorter copy, silently missing
// the rest of the conversation. Both kinds of copy are interrupted at the
// same byte: pi's by a file size limit, yesman's by the fault hook.
func TestInterruptedCopyIsNotResumedWithManagedPi(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh for the file size limit")
	}
	f := newManagedPiFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	before, after := filepath.Join(f.temp, "H004-volume"), filepath.Join(f.temp, "H002-volume")
	store := yesmen.New(filepath.Join(before, "yesmen"), filepath.Join(before, "archive"))
	if _, err := store.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	var limit atomic.Int64 // blocks of 512 bytes pi may write; 0 is none
	cfg := f.cfg
	command := cfg.Command
	cfg.Command = func(ctx context.Context) (string, []string, error) {
		name, args, err := command(ctx)
		if n := limit.Load(); n > 0 && err == nil {
			return "sh", append([]string{"-c", fmt.Sprintf(`ulimit -f %d && exec "$0" "$@"`, n), name}, args...), nil
		}
		return name, args, err
	}
	cfg.Yesmen = store
	m := New(cfg)
	id := ID("alice", yesmen.LeadName)
	for i := range 12 {
		sendAndSettle(t, ctx, m, id, deliveryTestID(30001+i), fmt.Sprintf("turn %d %s", i, strings.Repeat("x", 4000)), nil)
	}
	want := userTurns(t, ctx, m, id)
	m.StopAll()
	if err := os.Rename(before, after); err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(after, "yesmen", "alice", ".yesman", "sessions", "lead")
	original := sessionFiles(t, sessions)
	if len(original) != 1 {
		t.Fatalf("fixture has %d sessions", len(original))
	}
	var size int64
	for name := range original {
		info, _ := os.Stat(filepath.Join(sessions, name))
		size = info.Size()
	}
	// sh counts the limit in blocks of its own size.
	probe := filepath.Join(f.temp, "probe")
	_ = exec.Command("sh", "-c", `ulimit -f 1 && head -c 4096 /dev/zero > "$0"`, probe).Run()
	unit := int64(512)
	if info, err := os.Stat(probe); err == nil && info.Size() > 0 {
		unit = info.Size()
	}
	blocks := size / 2 / unit
	cut := blocks * unit // about half the copy

	// The first start at the new path is interrupted halfway through the copy.
	limit.Store(blocks)
	forkCopyFault = func(written int64) error {
		if written >= cut {
			return errors.New("file too large")
		}
		return nil
	}
	cfg.Yesmen = yesmen.New(filepath.Join(after, "yesmen"), filepath.Join(after, "archive"))
	m = New(cfg)
	if _, err := m.Messages(ctx, id); err == nil {
		t.Fatal("the interrupted start succeeded")
	} else {
		t.Logf("interrupted start: %v", err)
	}
	m.StopAll()
	all, _ := os.ReadDir(sessions)
	for _, e := range all {
		if info, err := e.Info(); err == nil {
			t.Logf("left: %s %d bytes (original %d, cut at %d)", e.Name(), info.Size(), size, cut)
		}
	}
	limit.Store(0)
	forkCopyFault = nil

	// The next start shows the whole conversation, from a whole copy.
	m = New(cfg)
	defer func() { m.StopAll() }()
	got := userTurns(t, ctx, m, id)
	t.Logf("after an interrupted copy the history has %d of %d turns", len(got), len(want))
	if !slices.Equal(got, want) {
		t.Fatalf("history after an interrupted copy = %d turns, want %d", len(got), len(want))
	}
	m.StopAll()
	// Every entry of the conversation is in the session resumed, the newest.
	var originalIDs []string
	for name := range original {
		originalIDs = entryIDs(t, filepath.Join(sessions, name))
	}
	newest := ""
	var newestTime time.Time
	for name := range sessionFiles(t, sessions) {
		info, _ := os.Stat(filepath.Join(sessions, name))
		if info.ModTime().After(newestTime) {
			newest, newestTime = name, info.ModTime()
		}
	}
	resumed := map[string]bool{}
	for _, id := range entryIDs(t, filepath.Join(sessions, newest)) {
		resumed[id] = true
	}
	missing := 0
	for _, id := range originalIDs {
		if !resumed[id] {
			missing++
		}
	}
	t.Logf("the resumed session holds %d of the %d original entries", len(originalIDs)-missing, len(originalIDs))
	if missing > 0 {
		t.Fatalf("the resumed session is missing %d of the %d original entries", missing, len(originalIDs))
	}
	files := sessionFiles(t, sessions)
	for name, sum := range original {
		if files[name] != sum {
			t.Fatalf("original session %s changed", name)
		}
	}
	if len(files) != 2 {
		t.Fatalf("%d session files after the move, want the original and one whole copy", len(files))
	}
	if stray, _ := filepath.Glob(filepath.Join(sessions, ".yesman-fork-*")); len(stray) != 0 {
		t.Fatalf("staging left: %v", stray)
	}
}

// entryIDs lists the IDs of a session file's entries, in order.
func entryIDs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, line := range strings.Split(string(data), "\n") {
		var e struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if json.Unmarshal([]byte(line), &e) == nil && e.Type != "session" && e.ID != "" {
			ids = append(ids, e.ID)
		}
	}
	return ids
}
