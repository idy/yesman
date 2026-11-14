package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Exercise the actual compatibility executable at an installation path with
// spaces, reached through another directory's CLI symlink on Unix. The child
// reports what the OS delivered; this catches Windows quoting and Unix exec
// regressions instead of comparing a constructed command string.
func TestLauncherTransfersArguments(t *testing.T) {
	install := filepath.Join(t.TempDir(), "Application with spaces")
	cwd := filepath.Join(t.TempDir(), "Working directory")
	for _, dir := range []string{install, cwd} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	legacy := filepath.Join(install, "yesman"+ext)
	target := filepath.Join(install, "Yes Man"+ext)
	build := func(out, pkg string) {
		t.Helper()
		cmd := exec.Command("go", "build", "-o", out, pkg)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v: %s", err, b)
		}
	}
	build(legacy, ".")
	build(target, "./testdata/record")
	entry := legacy
	if runtime.GOOS != "windows" {
		entry = filepath.Join(t.TempDir(), "yesman")
		if err := os.Symlink(legacy, entry); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(install, "result.json")
	args := []string{"", "a b", `a"b`, `C:\some folder\`, `trailing\\`, "中文", "$(touch must-not-exist)", "line\nbreak"}
	cmd := exec.Command(entry, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "YESMAN_LAUNCH_REPORT="+report, "YESMAN_LAUNCH_SENTINEL=preserved")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	var b []byte
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(report); err == nil {
			b = data
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var got struct {
		Exe, Cwd, Sentinel string
		Args               []string
		PID                int
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("report: %v: %s", err, b)
	}
	realTarget, _ := filepath.EvalSymlinks(target)
	realCwd, _ := filepath.EvalSymlinks(cwd)
	if got.Exe != realTarget || got.Cwd != realCwd || got.Sentinel != "preserved" {
		t.Fatalf("environment/path changed: %+v", got)
	}
	if runtime.GOOS != "windows" && got.PID != pid {
		t.Fatalf("launcher stayed in front of the app: %d != %d", got.PID, pid)
	}
	if a, _ := json.Marshal(got.Args); string(a) != string(mustJSON(args)) {
		t.Fatalf("arguments changed: %s", b)
	}
	if _, err := os.Stat(filepath.Join(cwd, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatal("arguments were executed by a shell")
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
