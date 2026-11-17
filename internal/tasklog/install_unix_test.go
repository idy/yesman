//go:build !windows

package tasklog

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestActualUnlinkPermissionFailureRecovers(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root cannot reproduce directory unlink denial")
	}
	b := []byte("# Original evidence\n")
	ws := fixture(t, b)
	m, _ := Run(ws, Options{Action: "dry-run"})
	meta := filepath.Join(ws, ".yesman")
	t.Cleanup(func() { _ = os.Chmod(meta, 0o755) })
	_, err := Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256, Hook: func(stage string) error {
		if stage == "install-linked:"+Marker {
			return os.Chmod(meta, 0o500)
		}
		return nil
	}})
	if err == nil {
		t.Fatal("actual unlink denial was ignored")
	}
	if !bytes.Equal(file(t, ws, TOC), b) {
		t.Fatal("failed unlink changed history")
	}
	if err = os.Chmod(meta, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err = Run(ws, Options{Action: "apply", Offline: true, Expect: m.SHA256}); err != nil {
		t.Fatal(err)
	}
	if _, err = Run(ws, Options{Action: "verify"}); err != nil {
		t.Fatal(err)
	}
}
