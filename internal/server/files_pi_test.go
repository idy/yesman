package server

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestManagedWriteThroughInstalledPiTools runs the installed pi runtime's
// own write and edit tools with the extension's managed file operations
// against the real API (scripts/check-managed-write-pi.mjs): ordinary write
// and edit work, while a file outside the grants and a file changed after pi
// read it are refused and left unchanged. It needs the prepared runtime, as
// CI's YESMAN_TEST_RUNTIME, and node.
func TestManagedWriteThroughInstalledPiTools(t *testing.T) {
	rt := os.Getenv("YESMAN_TEST_RUNTIME")
	node, err := exec.LookPath("node")
	if rt == "" || err != nil {
		t.Skip("needs YESMAN_TEST_RUNTIME and node")
	}
	if runtime.GOOS == "windows" {
		t.Skip("not yet run against the installed pi runtime on Windows")
	}
	f := newBoardFixture(t)
	y := f.hire("alice")
	w := startHelper(t, f)
	scratch, _, _ := f.m.ProcessTemp(w.Token)
	api := httptest.NewServer(f.srv.Handler())
	defer api.Close()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, filepath.Join("..", "..", "scripts", "check-managed-write-pi.mjs"))
	cmd.Env = append(os.Environ(), "YESMAN_TEST_RUNTIME="+rt, "YESMAN_API="+api.URL, "YESMAN_TOKEN="+w.Token,
		"SCRATCH="+filepath.Join(y.Path, filepath.FromSlash(scratch.Rel)), "OUTSIDE="+outside)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), `{"outsideRefused":true,"staleEditRefused":true,"ordinary":true}`) {
		t.Fatalf("unexpected result: %s", out)
	}
	t.Logf("%s", out)
}
