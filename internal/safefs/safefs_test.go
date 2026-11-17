package safefs_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/idy/yesman/internal/safefs"
	"github.com/idy/yesman/internal/safefs/linktest"
)

func open(t *testing.T, path string) *safefs.Dir {
	t.Helper()
	d, err := safefs.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestSafeFSOrdinaryUse(t *testing.T) {
	dir := t.TempDir()
	d := open(t, dir)
	if err := d.Mkdir("sub", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := d.Mkdir("sub", 0o700); err != nil {
		t.Fatalf("existing directory: %v", err)
	}
	sub, err := d.OpenDir("sub")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	f, err := sub.OpenFile("a.txt", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("one\r\ntwo\n"), 0); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got, _ := os.ReadFile(filepath.Join(dir, "sub", "a.txt")); string(got) != "one\r\ntwo\n" {
		t.Fatalf("content %q", got)
	}
	// Opening an existing file does not truncate it.
	f, err = sub.OpenFile("a.txt", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if data, err := safefs.ReadSmall(sub, "a.txt", 64); err != nil || string(data) != "one\r\ntwo\n" {
		t.Fatalf("read %q %v", data, err)
	}
	if _, err := sub.OpenFile("a.txt", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("exclusive create of an existing file: %v", err)
	}
	if _, err := sub.OpenFile("missing", os.O_RDWR, 0); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "sub", "missing")); err == nil {
		t.Fatal("opening without O_CREATE created the file")
	}
	if _, err := d.OpenDir("missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing directory: %v", err)
	}
	if _, err := safefs.ReadSmall(sub, "a.txt", 4); err == nil {
		t.Fatal("read a file larger than its limit")
	}
	if _, err := d.OpenFile("sub", os.O_RDONLY, 0); err == nil {
		t.Fatal("opened a directory as a file")
	}
	if _, err := sub.OpenDir("a.txt"); err == nil {
		t.Fatal("opened a file as a directory")
	}
	for _, name := range []string{"", ".", "..", "x/y", `x\y`} {
		if _, err := d.OpenDir(name); err == nil {
			t.Errorf("opened %q", name)
		}
	}
	two, err := safefs.OpenPath(d, []string{"sub"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	a, _ := sub.Identity()
	b, _ := two.Identity()
	if a != b || a.Ino == 0 {
		t.Fatalf("identities %+v %+v", a, b)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if !a.HasBirth {
			t.Fatal("no creation time")
		}
	}
	perm := os.FileMode(0o700)
	deep, err := safefs.OpenPath(d, []string{"x", "y"}, &perm)
	if err != nil {
		t.Fatal(err)
	}
	deep.Close()
	if info, err := os.Stat(filepath.Join(dir, "x", "y")); err != nil || !info.IsDir() {
		t.Fatalf("made path: %v", err)
	}
}

// A link at the name opened, of either kind, is refused, and nothing is
// written through it.
func TestSafeFSRefusesLinks(t *testing.T) {
	for _, kind := range []string{"symlink", "junction"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(target, "file.txt")
			if err := os.WriteFile(file, []byte("KEEP"), 0o600); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				linktest.Symlink(t, target, filepath.Join(dir, "dirlink"))
			} else {
				linktest.Junction(t, target, filepath.Join(dir, "dirlink"))
			}
			d := open(t, dir)
			if _, err := d.OpenDir("dirlink"); err == nil {
				t.Fatal("opened a directory link")
			} else {
				t.Logf("directory link refused: %v", err)
			}
			if err := d.Mkdir("dirlink", 0o700); err != nil {
				t.Fatalf("mkdir over an existing link: %v", err)
			}
			if _, err := safefs.OpenPath(d, []string{"dirlink", "sub"}, nil); err == nil {
				t.Fatal("walked through a directory link")
			}
			perm := os.FileMode(0o700)
			if _, err := safefs.OpenPath(d, []string{"dirlink", "sub"}, &perm); err == nil {
				t.Fatal("made a directory through a link")
			}
			if _, err := d.OpenFile("dirlink", os.O_RDWR|os.O_CREATE, 0o600); err == nil {
				t.Fatal("opened a directory link as a file")
			}
			if _, err := os.Lstat(filepath.Join(target, "sub")); err == nil {
				t.Fatal("a directory was made in the link's target")
			}
			if kind == "symlink" {
				linktest.Symlink(t, file, filepath.Join(dir, "filelink"))
				for _, flag := range []int{os.O_RDONLY, os.O_RDWR, os.O_RDWR | os.O_CREATE} {
					if f, err := d.OpenFile("filelink", flag, 0o600); err == nil {
						f.Close()
						t.Fatalf("opened a file link with flag %#x", flag)
					}
				}
				if _, err := safefs.ReadSmall(d, "filelink", 64); err == nil {
					t.Fatal("read through a file link")
				}
			}
			if got, _ := os.ReadFile(file); !bytes.Equal(got, []byte("KEEP")) {
				t.Fatalf("target changed to %q", got)
			}
			if entries, _ := os.ReadDir(target); len(entries) != 1 {
				t.Fatalf("target directory now holds %v", entries)
			}
		})
	}
}
