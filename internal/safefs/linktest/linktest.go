// Package linktest makes the links the managed-file tests refuse: symbolic
// links and, on Windows, directory junctions. Creating one may need a
// privilege or file system the test machine lacks; the test is then
// skipped, or fails when YESMAN_REQUIRE_LINK_TESTS is set (as CI does), so
// a skip is never mistaken for a pass where links can be made.
package linktest

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

func unavailable(t *testing.T, what string, err error) {
	t.Helper()
	if os.Getenv("YESMAN_REQUIRE_LINK_TESTS") != "" {
		t.Fatalf("%s could not be created here, and YESMAN_REQUIRE_LINK_TESTS is set: %v", what, err)
	}
	t.Skipf("%s unavailable here, not tested: %v", what, err)
}

// Symlink makes link, a symbolic link to target.
func Symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		unavailable(t, "symbolic link", err)
	}
}

// Junction makes link, a directory junction to the absolute directory
// target, with the unprivileged mklink /J; on other systems the test is
// skipped as not applicable.
func Junction(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("directory junctions exist only on Windows")
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		unavailable(t, "directory junction", errorf(err, out))
	}
}

type outErr struct {
	err error
	out []byte
}

func (e outErr) Error() string { return e.err.Error() + ": " + string(e.out) }

func errorf(err error, out []byte) error { return outErr{err, out} }
