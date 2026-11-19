package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/filelock"
	"github.com/idy/yesman/internal/home"
)

func TestRunCLIReadsOneUnchangedMessage(t *testing.T) {
	const prompt = "\t中文\u2028\r\n  second line\n\n"
	path := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(path, []byte(prompt), 0600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{path, "-"} {
		got, err := readRunPrompt(t.Context(), source, strings.NewReader(prompt))
		if err != nil || got != prompt {
			t.Fatalf("%q, %v", got, err)
		}
	}
	for _, bad := range []string{"  \t\n", "\xff"} {
		if _, err := readRunPrompt(t.Context(), "-", strings.NewReader(bad)); err == nil {
			t.Fatal("accepted invalid prompt")
		}
	}
}

func TestRunCLIProfileLockExcludesBothClients(t *testing.T) {
	h := home.Home{Root: t.TempDir()}
	t.Setenv("YESMAN_HOME", h.Root)
	for _, name := range []string{"profile.lock", "schedules.lock"} {
		lock, err := filelock.Try(filepath.Join(h.Runtime(), name))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		code := runCLI(t.Context(), []string{"--workspace", "trial", "--new"}, strings.NewReader("test"), &out, io.Discard)
		lock.Close()
		var r agent.RunResult
		if json.Unmarshal(out.Bytes(), &r) != nil || code != 1 || r.Status != "failure" || !strings.Contains(r.Error, "lock") {
			t.Fatalf("exit %d: %s", code, out.String())
		}
		if _, err := os.Stat(h.Yesmen()); !os.IsNotExist(err) {
			t.Fatal("wrote workspace while locked")
		}
	}
}

func TestRunCLIInterruptedInput(t *testing.T) {
	read, write := io.Pipe()
	defer read.Close()
	defer write.Close()
	for _, want := range []int{124, 130} {
		ctx, cancel := context.WithCancel(t.Context())
		if want == 130 {
			cancel()
		}
		var out bytes.Buffer
		got := runCLI(ctx, []string{"--workspace", "x", "--timeout", "20ms"}, read, &out, io.Discard)
		cancel()
		if got != want {
			t.Fatalf("got %d, want %d: %s", got, want, out.String())
		}
	}
}
