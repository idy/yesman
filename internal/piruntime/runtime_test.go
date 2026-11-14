package piruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPinnedManifest(t *testing.T) {
	var lock struct {
		Packages map[string]struct{ Version, Resolved, Integrity string }
	}
	b, _ := assets.ReadFile("assets/package-lock.json")
	if err := json.Unmarshal(b, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Packages["node_modules/@earendil-works/pi-coding-agent"].Version != "1.5.2" {
		t.Fatal("pi version is not pinned")
	}
	for path, p := range lock.Packages {
		if path == "" {
			continue
		}
		if !strings.HasPrefix(p.Resolved, "https://registry.npmjs.org/") || !strings.HasPrefix(p.Integrity, "sha512-") {
			t.Fatalf("unpinned dependency %s", path)
		}
	}
	var patch struct {
		Patches []struct {
			Path, Before, After string
			Edits               []struct{ Before, After string }
		}
	}
	b, _ = assets.ReadFile("assets/patch.json")
	if err := json.Unmarshal(b, &patch); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, p := range patch.Patches {
		if seen[p.Path] || len(p.Before) != 64 || len(p.After) != 64 || len(p.Edits) == 0 || strings.Contains(p.Path, "..") || filepath.IsAbs(p.Path) {
			t.Fatalf("invalid patch %s", p.Path)
		}
		seen[p.Path] = true
	}
	if len(seen) != 9 {
		t.Fatalf("review changed source-file scope: %d", len(seen))
	}
}
func TestExternalWrappersArePreserved(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pi.cmd")
	target := `node_modules\@earendil-works\pi-coding-agent\dist\bundle\cli.js`
	pkg := filepath.Join(root, "node_modules", "@earendil-works", "pi-coding-agent")
	if err := os.MkdirAll(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@earendil-works/pi-coding-agent"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "echo custom-wrapper\n"} {
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(npmWindowsShim(target)+suffix, "\n", "\r\n")), 0600); err != nil {
			t.Fatal(err)
		}
		if got := OfficialNPM(path); got != (suffix == "") {
			t.Fatalf("wrapper recognized=%v suffix=%q", got, suffix)
		}
	}
	path = filepath.Join(root, "pi")
	_ = os.WriteFile(path, []byte("#!/bin/sh\nexec node custom.js \"$@\"\n"), 0700)
	if OfficialNPM(path) {
		t.Fatal("custom wrapper replaced")
	}
}
func TestPreparationWaitHonorsContext(t *testing.T) {
	r := &Runtime{gate: make(chan struct{}, 1)}
	r.gate <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, _, err := r.Command(ctx); err == nil {
		t.Fatal("preparation ignored deadline")
	}
}
func TestPreparedRuntimeIntegrity(t *testing.T) {
	// The same app build produced by tools/pi-runtime. Fast readonly check,
	// enabled by CI after first-use preparation on each OS.
	root := os.Getenv("YESMAN_TEST_RUNTIME")
	if root == "" {
		t.Skip("prepare runtime first")
	}
	if err := verify(root); err != nil {
		t.Fatal(err)
	}
	// Changed embedded assets fail before an existing cache can run. Never alter
	// the prepared shared fixture: a small copied manifest is enough to test it.
	broken := t.TempDir()
	entries, _ := assets.ReadDir("assets")
	for _, e := range entries {
		b, _ := assets.ReadFile("assets/" + e.Name())
		_ = os.WriteFile(filepath.Join(broken, e.Name()), b, 0600)
	}
	_ = os.WriteFile(filepath.Join(broken, "patch.json"), []byte(`{}`), 0600)
	if err := verify(broken); err == nil || !strings.Contains(err.Error(), "asset mismatch") {
		t.Fatalf("changed patch was not rejected: %v", err)
	}
}
