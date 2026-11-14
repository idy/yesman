package githubapp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClientCloseCancelsSharedMint(t *testing.T) {
	started := make(chan struct{})
	c := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/installation") {
			writeJSON(w, installation())
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	})
	done := make(chan error, 1)
	go func() { _, err := c.token(context.Background(), "idy/yesman"); done <- err }()
	<-started
	c.Close()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("closed mint: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tokens) != 0 {
		t.Fatal("closed client cached a minted token")
	}
}
func TestGHOutputCannotExposeInstallationToken(t *testing.T) {
	gh := fixtureGHExecutable(t)
	c := clientFixture(t, connectionHandler)
	pull, err := c.CreateWithGH(context.Background(), gh, PullInput{Repository: "idy/yesman", Head: "test/identity", Base: "main", Title: "test: Redaction", Draft: true})
	if err == nil || strings.Contains(err.Error(), fixtureToken) || pull.URL != "" {
		t.Fatal("raw credential-bearing subprocess output escaped")
	}
}
func TestShimInstallReplacesOneRuntimeBinary(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "Yes Man binary")
	runtimeRoot := filepath.Join(root, "runtime with spaces")
	if err := os.WriteFile(source, []byte("first executable fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := InstallShim(runtimeRoot, source)
	if err != nil {
		t.Fatal(err)
	}
	name := "gh"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	destination := filepath.Join(dir, name)
	data, _ := os.ReadFile(destination)
	if string(data) != "first executable fixture" {
		t.Fatal("wrong installed wrapper")
	}
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacement, []byte("second executable fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, source); err != nil {
		t.Fatal(err)
	}
	dir2, err := InstallShim(runtimeRoot, source)
	if err != nil || dir2 != dir {
		t.Fatal("runtime wrapper accumulated a version directory")
	}
	data, _ = os.ReadFile(destination)
	if string(data) != "second executable fixture" {
		t.Fatal("wrapper was not replaced")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("partial or old runtime wrappers remained")
	}
}
func TestCanonicalPullURLCaseAndBoundary(t *testing.T) {
	if number, err := pullNumber("https://github.com/SomeOwner/MixedRepo/pull/42", "someowner/mixedrepo"); err != nil || number != 42 {
		t.Fatal("canonical repository casing rejected")
	}
	for _, value := range []string{"https://evil.example/idy/yesman/pull/42", "https://github.com/idy/yesman/pull/42?token=fixture", "https://github.com/else/repo/pull/42", "https://github.com/idy/yesman/pull/42/extra"} {
		if _, err := pullNumber(value, "idy/yesman"); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}
