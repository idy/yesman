package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// Tests of the managed write entry (files.go) and the workspace temporary
// directories (internal/agent/workspace_temp.go), with a real Manager that
// starts the test binary as pi (board_lifetime_test.go rpcChild).

func postFile(f *boardFixture, token string, body map[string]any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/files", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(res, req)
	return res
}

func startHelper(t *testing.T, f *boardFixture) childTrace {
	t.Helper()
	f.start("alice/lead")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.m.StartWorker(ctx, "alice", "helper", "Helper", "help"); err != nil {
		t.Fatal(err)
	}
	return f.start("alice/helper")
}

func readString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestManagedFilesStayInTheProcessGrants(t *testing.T) {
	f := newBoardFixture(t)
	y := f.hire("alice")
	other := f.hire("bob")
	w := startHelper(t, f)
	scratch, evidence, ok := f.m.ProcessTemp(w.Token)
	if !ok {
		t.Fatal("no reserved directories for the worker's process")
	}
	in := func(d agent.TempDir, name string) string {
		return filepath.Join(y.Path, filepath.FromSlash(d.Rel), filepath.FromSlash(name))
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(y.Path, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) int {
		return postFile(f, w.Token, map[string]any{"op": "write", "path": path, "content": content}).Code
	}

	if code := write(in(scratch, "sub/a.txt"), "hello\r\n"); code != 200 {
		t.Fatalf("scratch write: %d", code)
	}
	if got := readString(t, in(scratch, "sub/a.txt")); got != "hello\r\n" {
		t.Fatalf("scratch content %q", got)
	}
	if code := write(in(evidence, "report.md"), "# ok\n"); code != 200 {
		t.Fatalf("evidence write: %d", code)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(in(scratch, "sub")); info.Mode().Perm() != 0o700 {
			t.Fatalf("new scratch directory mode %v", info.Mode().Perm())
		}
	}

	meta := readString(t, filepath.Join(y.Path, ".yesman", "yesman.json"))
	refused := map[string]string{
		"absolute outside":     sentinel,
		"metadata":             filepath.Join(y.Path, ".yesman", "yesman.json"),
		"existing top level":   filepath.Join(y.Path, "repo", "x.go"),
		"workspace root":       filepath.Join(y.Path, "x.txt"),
		"other workspace":      filepath.Join(other.Path, "x.txt"),
		"dot-dot out of grant": in(scratch, "../x.txt"),
		"another agent":        filepath.Join(y.Path, ".yesman", "tmp", "_agent", "lead", "x.txt"),
		"task directory name":  filepath.Join(y.Path, ".yesman", "tmp", "t1", "helper", "x.txt"),
	}
	for what, path := range refused {
		if code := write(path, "WRITTEN"); code == 200 {
			t.Errorf("%s (%s) was written", what, path)
		}
	}
	if readString(t, filepath.Join(y.Path, ".yesman", "yesman.json")) != meta {
		t.Fatal("yesman.json changed")
	}
	for _, p := range []string{filepath.Join(y.Path, "repo", "x.go"), filepath.Join(y.Path, "x.txt"), filepath.Join(other.Path, "x.txt")} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("%s was created", p)
		}
	}

	// A link inside the grant, a parent swapped for a link after it was
	// used, and a second hard link never take a write outside.
	if err := os.Symlink(outside, in(scratch, "link")); err == nil {
		if code := write(in(scratch, "link/sentinel"), "THROUGH LINK"); code == 200 {
			t.Fatal("wrote through a link")
		}
		if code := write(in(scratch, "swap/a.txt"), "first"); code != 200 {
			t.Fatalf("write before swap: %d", code)
		}
		if err := os.Rename(in(scratch, "swap"), in(scratch, "swapped")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, in(scratch, "swap")); err != nil {
			t.Fatal(err)
		}
		if code := write(in(scratch, "swap/sentinel"), "SWAPPED PARENT"); code == 200 {
			t.Fatal("wrote through a swapped parent")
		}
	} else {
		t.Logf("symlinks unavailable: %v", err)
	}
	if err := os.Link(sentinel, in(scratch, "hardlink")); err == nil {
		if code := write(in(scratch, "hardlink"), "THROUGH HARDLINK"); code == 200 {
			t.Fatal("wrote through a hard link")
		}
	}
	if got := readString(t, sentinel); got != "ORIGINAL" {
		t.Fatalf("outside sentinel changed to %q", got)
	}

	// An edit's write is refused when the file changed since it was read.
	res := postFile(f, w.Token, map[string]any{"op": "read", "path": in(scratch, "sub/a.txt")})
	var read struct {
		SHA256 string `json:"sha256"`
	}
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &read) != nil || read.SHA256 == "" {
		t.Fatalf("read: %d %s", res.Code, res.Body)
	}
	if err := os.WriteFile(in(scratch, "sub/a.txt"), []byte("changed elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := postFile(f, w.Token, map[string]any{"op": "write", "path": in(scratch, "sub/a.txt"), "content": "edited", "expect": read.SHA256}).Code; code == 200 {
		t.Fatal("stale edit was written")
	}
	if got := readString(t, in(scratch, "sub/a.txt")); got != "changed elsewhere" {
		t.Fatalf("stale edit changed the file to %q", got)
	}

	// The lead does not use this entry.
	lead := f.start("alice/lead")
	if code := postFile(f, lead.Token, map[string]any{"op": "write", "path": in(scratch, "lead.txt"), "content": "x"}).Code; code == 200 {
		t.Fatal("the lead wrote through the managed entry")
	}
}

func TestManagedFilesRefuseAReplacedDirectoryAndAnEndedProcess(t *testing.T) {
	f := newBoardFixture(t)
	y := f.hire("alice")
	w := startHelper(t, f)
	scratch, _, _ := f.m.ProcessTemp(w.Token)
	dir := filepath.Join(y.Path, filepath.FromSlash(scratch.Rel))

	// A copy at the same path carrying the same owner.json is not the
	// reserved directory.
	owner := readString(t, filepath.Join(dir, "owner.json"))
	if err := os.Rename(dir, dir+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "owner.json"), []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := postFile(f, w.Token, map[string]any{"op": "write", "path": filepath.Join(dir, "a.txt"), "content": "x"}).Code; code == 200 {
		t.Fatal("wrote into a replacement of the reserved directory")
	}
	if _, err := os.Lstat(filepath.Join(dir, "a.txt")); err == nil {
		t.Fatal("file created in the replacement")
	}

	// After its process stops, its credential writes nothing, neither in
	// its own old directory nor in the next process's.
	stopAgent(t, f, "alice/helper")
	if code := postFile(f, w.Token, map[string]any{"op": "write", "path": filepath.Join(dir+".moved", "late.txt"), "content": "late"}).Code; code != 401 {
		t.Fatalf("late write of a stopped process: %d", code)
	}
	renewed := f.start("alice/helper")
	next, _, ok := f.m.ProcessTemp(renewed.Token)
	if !ok || next.Rel == scratch.Rel {
		t.Fatalf("the new process did not get a new directory: %v %q", ok, next.Rel)
	}
	nextDir := filepath.Join(y.Path, filepath.FromSlash(next.Rel))
	if code := postFile(f, w.Token, map[string]any{"op": "write", "path": filepath.Join(nextDir, "late.txt"), "content": "late"}).Code; code == 200 {
		t.Fatal("an old credential wrote into the new process's directory")
	}
	if code := postFile(f, renewed.Token, map[string]any{"op": "write", "path": filepath.Join(dir+".moved", "x.txt"), "content": "x"}).Code; code == 200 {
		t.Fatal("the new process wrote into the old process's directory")
	}
	for _, p := range []string{filepath.Join(dir+".moved", "late.txt"), filepath.Join(dir+".moved", "x.txt"), filepath.Join(nextDir, "late.txt")} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("%s was written", p)
		}
	}
}

func stopAgent(t *testing.T, f *boardFixture, id string) {
	t.Helper()
	f.m.Stop(id)
}

func TestManagedFilesGrantOnlyOwnAuthorWorktrees(t *testing.T) {
	cf := newCleanupFixture(t)
	f := newBoardFixture(t)
	f.srv.Pool = cf.s.Pool
	y := f.hire("alice")
	w := startHelper(t, f)
	str := func(s string) *string { return &s }
	if _, err := f.store.ChangeBoard(y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: str("Task"), Owner: str("helper"), Status: str("doing")}}}); err != nil {
		t.Fatal(err)
	}
	info, err := f.m.Authenticate(w.Token)
	if err != nil {
		t.Fatal(err)
	}
	me := caller{agent: info, yesman: y, token: w.Token}
	checkout := func(purpose, dir string) {
		req := CheckoutRequest{Repo: cf.repo.Name, Dir: dir, Task: "t1", Purpose: purpose}
		if purpose == "review" {
			req.Ref = cf.head
		} else {
			req.Branch = "feature"
		}
		body, _ := json.Marshal(req)
		if _, err := f.srv.checkout(context.Background(), me, httptest.NewRequest("POST", "/v1/checkout", bytes.NewReader(body))); err != nil {
			t.Fatal(err)
		}
	}
	checkout("author", "mine")
	checkout("review", "reviewed")
	write := func(path string) int {
		return postFile(f, w.Token, map[string]any{"op": "write", "path": path, "content": "changed\n"}).Code
	}
	if code := write(filepath.Join(y.Path, "mine", "feature")); code != 200 {
		t.Fatalf("own author worktree: %d", code)
	}
	if got := readString(t, filepath.Join(y.Path, "mine", "feature")); got != "changed\n" {
		t.Fatalf("content %q", got)
	}
	if code := write(filepath.Join(y.Path, "mine", ".git")); code == 200 {
		t.Fatal("wrote the worktree's .git")
	}
	if code := write(filepath.Join(y.Path, "reviewed", "feature")); code == 200 {
		t.Fatal("wrote a review checkout")
	}
	records, _ := f.store.WorktreeRecords(y.Name)
	for i := range records {
		if records[i].Dir == "mine" {
			records[i].Removed = true
		}
	}
	if err := f.store.SaveWorktreeRecords(y.Name, records); err != nil {
		t.Fatal(err)
	}
	if code := write(filepath.Join(y.Path, "mine", "feature")); code == 200 {
		t.Fatal("wrote a worktree whose record was removed")
	}
}

func TestAgentTempEnvironmentReachesNestedPrograms(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	traces := filepath.Join(dir, "traces")
	os.Mkdir(traces, 0o700)
	store := yesmen.New(filepath.Join(dir, "yesmen"), filepath.Join(dir, "archive"))
	y, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	system := os.TempDir()
	home := os.Getenv("HOME")
	// The app's own environment tries to move every managed variable
	// elsewhere, in other cases too; none of them may win.
	m := agent.New(agent.Config{Pi: exe, Extension: filepath.Join(dir, "none.ts"), Yesmen: store, Env: []string{
		"YESMAN_TEST_RPC_CHILD=" + traces, "YESMAN_TEST_TEMP_PROBE=1", "GORACE=atexit_sleep_ms=0",
		"TMP=" + system, "TEMP=" + system, "GOTMPDIR=" + system, "GOCACHE=" + system, "NPM_CONFIG_CACHE=" + system, "npm_config_cache=" + system, "UNRELATED_SETTING=kept",
	}})
	t.Cleanup(m.StopAll)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := m.Prompt(ctx, "alice/lead", "hello"); err != nil {
		t.Fatal(err)
	}
	var probe map[string]string
	raw, err := os.ReadFile(filepath.Join(traces, "alice_lead.env.json"))
	if err != nil || json.Unmarshal(raw, &probe) != nil {
		t.Fatalf("no probe: %v %s", err, raw)
	}
	ws, _ := filepath.EvalSymlinks(y.Path)
	under := func(key, rel string) {
		t.Helper()
		got, _ := filepath.EvalSymlinks(probe[key])
		want := filepath.Join(ws, filepath.FromSlash(rel))
		if got == "" || !strings.HasPrefix(strings.ToLower(got), strings.ToLower(want)) {
			t.Errorf("%s = %q, want under %s", key, probe[key], want)
		}
	}
	for _, key := range []string{"TMPDIR", "TMP", "TEMP", "GOTMPDIR", "YESMAN_TMP", "nested-go", "nested-tempdir"} {
		under(key, ".yesman/tmp/_agent/lead")
	}
	under("YESMAN_EVIDENCE", ".yesman/evidence/_agent/lead")
	under("GOCACHE", ".yesman/tmp/_cache/go-build")
	under("GOMODCACHE", ".yesman/tmp/_cache/go-mod")
	under("npm_config_cache", ".yesman/tmp/_cache/npm")
	for _, key := range []string{"nested-python", "nested-node"} {
		if probe[key] != "" {
			under(key, ".yesman/tmp/_agent/lead")
		}
	}
	if os.Getenv("YESMAN_TEST_RUNTIME") != "" && probe["nested-node"] != "" && runtime.GOOS != "windows" {
		if probe["pi-output-file"] == "" {
			t.Errorf("the runtime's bash tool kept no full output: %s", probe["pi-output-file-error"])
		} else {
			under("pi-output-file", ".yesman/tmp/_agent/lead")
			t.Logf("pi's full output of a truncated command: %s", probe["pi-output-file"])
		}
	}
	if probe["npm-cache-variants"] != "1" {
		t.Errorf("npm cache variables in the agent's environment: %s", probe["npm-cache-variants"])
	}
	if probe["HOME"] != home || probe["UNRELATED_SETTING"] != "kept" {
		t.Errorf("HOME or unrelated settings changed: %q %q", probe["HOME"], probe["UNRELATED_SETTING"])
	}
}

func TestAgentDoesNotStartWithoutItsTemporaryDirectory(t *testing.T) {
	outside := t.TempDir()
	for _, kind := range []string{"link", "file"} {
		t.Run(kind, func(t *testing.T) {
			exe, _ := os.Executable()
			dir := t.TempDir()
			store := yesmen.New(filepath.Join(dir, "yesmen"), filepath.Join(dir, "archive"))
			y, err := store.Hire("alice")
			if err != nil {
				t.Fatal(err)
			}
			tmp := filepath.Join(y.Path, ".yesman", "tmp")
			if kind == "link" {
				if err := os.Symlink(outside, tmp); err != nil {
					t.Skip("symlinks unavailable:", err)
				}
			} else if err := os.WriteFile(tmp, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			traces := filepath.Join(dir, "traces")
			os.Mkdir(traces, 0o700)
			m := agent.New(agent.Config{Pi: exe, Extension: filepath.Join(dir, "none.ts"), Yesmen: store, Env: []string{"YESMAN_TEST_RPC_CHILD=" + traces}})
			t.Cleanup(m.StopAll)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := m.Prompt(ctx, "alice/lead", "hello"); err == nil {
				t.Fatal("started without a usable temporary directory")
			}
			if _, err := os.Lstat(filepath.Join(traces, "alice_lead.json")); err == nil {
				t.Fatal("the process ran")
			}
			if entries, _ := os.ReadDir(outside); len(entries) != 0 {
				t.Fatalf("created outside: %v", entries)
			}
		})
	}
}

func TestFailedStartReleasesOnlyItsReservation(t *testing.T) {
	dir := t.TempDir()
	store := yesmen.New(filepath.Join(dir, "yesmen"), filepath.Join(dir, "archive"))
	y, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	m := agent.New(agent.Config{Pi: filepath.Join(dir, "no-such-pi"), Extension: filepath.Join(dir, "none.ts"), Yesmen: store})
	t.Cleanup(m.StopAll)
	keep := filepath.Join(y.Path, ".yesman", "tmp", "_agent", "lead", "earlier")
	if err := os.MkdirAll(keep, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.Prompt(ctx, "alice/lead", "hello"); err == nil {
		t.Fatal("started a missing pi")
	}
	tmp, _ := os.ReadDir(filepath.Join(y.Path, ".yesman", "tmp", "_agent", "lead"))
	evidence, _ := os.ReadDir(filepath.Join(y.Path, ".yesman", "evidence", "_agent", "lead"))
	if len(tmp) != 1 || tmp[0].Name() != "earlier" || len(evidence) != 0 {
		t.Fatalf("after a failed start: tmp %v evidence %v", tmp, evidence)
	}
}

// probeTemp runs in the pi stand-in: it records the managed variables and
// the temporary paths a program it runs, and that program's runtimes,
// choose, as an agent's command would.
func probeTemp(dir, name string) {
	out := map[string]string{}
	for _, k := range append(append([]string{}, agent.ManagedEnvKeys...), "HOME", "UNRELATED_SETTING") {
		out[k] = os.Getenv(k)
	}
	npm := 0
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.EqualFold(k, "npm_config_cache") {
			npm++
		}
	}
	out["npm-cache-variants"] = string(rune('0' + npm))
	exe, _ := os.Executable()
	nested := filepath.Join(dir, name+".nested")
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "YESMAN_TEMP_CHILD="+nested)
	if cmd.Run() == nil {
		if data, err := os.ReadFile(nested); err == nil {
			parts := strings.SplitN(string(data), "\n", 2)
			out["nested-go"] = parts[0]
			if len(parts) == 2 {
				out["nested-tempdir"] = parts[1]
			}
		}
	}
	if py, err := exec.LookPath("python3"); err == nil {
		if b, err := exec.Command(py, "-I", "-c", "import tempfile,os;fd,p=tempfile.mkstemp();os.close(fd);print(p)").Output(); err == nil {
			out["nested-python"] = strings.TrimSpace(string(b))
		}
	}
	if node, err := exec.LookPath("node"); err == nil {
		if b, err := exec.Command(node, "-e", "const fs=require('fs'),os=require('os'),p=require('path');console.log(fs.mkdtempSync(p.join(os.tmpdir(),'n-')))").Output(); err == nil {
			out["nested-node"] = strings.TrimSpace(string(b))
		}
	}
	// pi's own output files (the full output of a truncated command, binary
	// MCP resources, codemode images) go to os.tmpdir() of the pi process
	// itself (dist/utils/output-files.js), not to a command's TMP: run the
	// installed runtime's bash tool in this stand-in's environment.
	if rt, node := os.Getenv("YESMAN_TEST_RUNTIME"), out["nested-node"]; rt != "" && node != "" && runtime.GOOS != "windows" {
		index := filepath.Join(rt, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "index.js")
		script := `const {pathToFileURL}=await import('node:url');const {createBashToolDefinition}=await import(pathToFileURL(process.argv[1]).href);` +
			`const r=await createBashToolDefinition(process.cwd(),{exposeSessionEnvironment:false}).execute('1',{command:'seq 1 60000'},undefined,undefined,{cwd:process.cwd()});console.log(r.details?.fullOutputPath??'')`
		if path, err := exec.LookPath("node"); err == nil {
			if b, err := exec.Command(path, "--input-type=module", "-e", script, index).Output(); err == nil {
				out["pi-output-file"] = strings.TrimSpace(string(b))
			} else {
				out["pi-output-file-error"] = err.Error()
			}
		}
	}
	b, _ := json.Marshal(out)
	_ = os.WriteFile(filepath.Join(dir, name+".env.json"), b, 0o600)
}

// nestedTempChild is a program an agent's command runs: it makes a
// temporary file where its runtime chooses.
func nestedTempChild() {
	f, err := os.CreateTemp("", "nested-*")
	if err != nil {
		os.Exit(2)
	}
	f.Close()
	_ = os.WriteFile(os.Getenv("YESMAN_TEMP_CHILD"), []byte(f.Name()+"\n"+os.TempDir()), 0o600)
}
