//go:build darwin

package pool

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDesktopLsofOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lsof")
	for _, tc := range []struct {
		name, script string
		clean        bool
	}{
		{"clean", `printf 'p31415\0\nfcwd\0n/somewhere\0\nf3\0n\0\n'`, true},
		{"unnamed-cwd", `printf 'p31415\0\nfcwd\0n\0\n'`, false},
		{"no-pid", `printf 'n/tree\0\n'`, false},
		{"truncated-entry", `printf 'p31415\0\nfcwd\0n/tree'`, false},
		{"truncated-set", `printf 'p31415\0\nfcwd\0n/tree\0'`, false},
		{"missing-name", `printf 'p31415\0\nfcwd\0\n'`, false},
		{"unreadable-fds", `printf 'p31415\0\nfNOFD\0nPermission denied\0\n'`, false},
		{"early-terminator", `printf 'p31415\0\n\0n/tree\0\n'`, false},
		{"nonzero-exit", `printf 'p31415\0\n'; exit 4`, false},
		{"malformed-pid", `printf 'p?\0\n'`, false},
		{"malformed-fd", `printf 'p31415\0\nf?\0n/dev/null\0\n'`, false},
		{"orphan-name-before-valid-pid", `printf 'n/tree\0\np31415\0\n'`, false},
		{"successful-with-diagnostics", `printf 'p31415\0\n'; printf 'fixture: incomplete input\n' >&2`, false},
		{"over-size-limit", `exec /usr/bin/perl -e 'print "p31415\0\n"; print "f1\0n/dev/null\0\n" x 3000000'`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"+tc.script+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			var observed []string
			err := visitLsof(t.Context(), path, func(path string) bool {
				if tc.clean {
					observed = append(observed, path)
				}
				return false
			})
			if tc.clean {
				if err != nil || !reflect.DeepEqual(observed, []string{"/somewhere"}) {
					t.Fatalf("valid listing: %q %v", observed, err)
				}
			} else if !errors.Is(err, errLsofIncomplete) {
				t.Fatalf("wanted unknown file use, got %v", err)
			}
		})
	}
}

func TestDesktopLsofFieldNames(t *testing.T) {
	data := "p123\x00\nfcwd\x00n/self\x00\np456\x00\nfcwd\x00n/path\nwith newline\x00\nf3\x00n/file\x00\n"
	var names []string
	if err := scanLsof(strings.NewReader(data), 123, func(name string) bool { names = append(names, name); return false }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"/path\nwith newline", "/file"}) {
		t.Fatalf("names = %q", names)
	}
}

func TestDesktopLsofRealUsers(t *testing.T) {
	root := t.TempDir()
	cwd, fd, unused := filepath.Join(root, "tree"), filepath.Join(root, "tree-two"), filepath.Join(root, "unused")
	for _, path := range []string{cwd, fd, unused} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(fd, "held")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), "/bin/sh", "-c", `exec 3<"$1"; echo ready; exec /bin/cat >/dev/null`, "fixture", file)
	child.Dir = cwd
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = child.Wait() }()
	ready := make([]byte, 6)
	if _, err := io.ReadFull(output, ready); err != nil || string(ready) != "ready\n" {
		t.Fatalf("fixture did not start: %q %v", ready, err)
	}
	preview := DesktopDiscardPreview(t.Context(), root)
	for _, path := range []string{cwd, fd} {
		var hold *RemovalHold
		if err := checkDesktopWorktreeUse(preview, path, true); !errors.As(err, &hold) {
			t.Fatalf("shared snapshot missed %s: %v", filepath.Base(path), err)
		}
		if err := checkDesktopWorktreeUse(t.Context(), path, false); !errors.As(err, &hold) {
			t.Fatalf("fresh scan missed %s: %v", filepath.Base(path), err)
		}
	}
	if err := checkDesktopWorktreeUse(preview, unused, true); err != nil {
		t.Fatalf("complete native listing: %v", err)
	}
	_ = input.Close()
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := checkDesktopWorktreeUse(preview, cwd, true); err == nil {
		t.Fatal("preview unexpectedly refreshed the listing")
	}
	if err := checkDesktopWorktreeUse(preview, cwd, false); err != nil {
		t.Fatalf("confirmation did not consult a fresh native listing: %v", err)
	}
}
