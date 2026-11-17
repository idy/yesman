//go:build unix

package restart

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRejectsLinkAndPipe(t *testing.T) {
	t.Run("link", func(t *testing.T) {
		d := dirAt(t, t0)
		target := filepath.Join(t.TempDir(), "elsewhere.json")
		b, _ := json.Marshal(intent(NewID()))
		_ = os.WriteFile(target, b, 0o600)
		_ = os.MkdirAll(d.Path, 0o700)
		if err := os.Symlink(target, filepath.Join(d.Path, intentName)); err != nil {
			t.Fatal(err)
		}
		_, kept, found, rejected, err := d.Claim()
		if err != nil || !found || !strings.Contains(rejected, "not a regular file") {
			t.Fatalf("Claim link: %q %v %q %v", kept, found, rejected, err)
		}
		if fi, err := os.Lstat(filepath.Join(d.Path, kept)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the link was not kept as a link: %v %v", fi, err)
		}
		if got, _ := os.ReadFile(target); string(got) != string(b) {
			t.Fatal("the link's target changed")
		}
	})
	t.Run("hard link", func(t *testing.T) {
		d := dirAt(t, t0)
		_ = os.MkdirAll(d.Path, 0o700)
		b, _ := json.Marshal(intent(NewID()))
		other := filepath.Join(d.Path, "other.json")
		_ = os.WriteFile(other, b, 0o600)
		if err := os.Link(other, filepath.Join(d.Path, intentName)); err != nil {
			t.Fatal(err)
		}
		if _, _, found, rejected, err := d.Claim(); err != nil || !found || !strings.Contains(rejected, "other links") {
			t.Fatalf("Claim hard link: %v %q %v", found, rejected, err)
		}
	})
	t.Run("fifo", func(t *testing.T) {
		d := dirAt(t, t0)
		_ = os.MkdirAll(d.Path, 0o700)
		if err := syscall.Mkfifo(filepath.Join(d.Path, intentName), 0o600); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		var rejected string
		go func() {
			defer close(done)
			_, _, _, rejected, _ = d.Claim()
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Claim blocked on a pipe")
		}
		if !strings.Contains(rejected, "not a regular file") {
			t.Fatalf("rejected = %q", rejected)
		}
	})
	t.Run("directory replaced by a link", func(t *testing.T) {
		d := dirAt(t, t0)
		real := filepath.Join(t.TempDir(), "real")
		_ = os.MkdirAll(real, 0o700)
		b, _ := json.Marshal(intent(NewID()))
		_ = os.WriteFile(filepath.Join(real, intentName), b, 0o600)
		if err := os.Symlink(real, d.Path); err != nil {
			t.Fatal(err)
		}
		if _, _, found, _, err := d.Claim(); found || err == nil {
			t.Fatalf("Claim through a linked directory: found=%v err=%v", found, err)
		}
		if err := d.Write(intent(NewID())); err == nil {
			t.Fatal("Write through a linked directory")
		}
		if _, err := os.Stat(filepath.Join(real, intentName)); err != nil {
			t.Fatal("the linked directory's file was touched")
		}
	})
}
