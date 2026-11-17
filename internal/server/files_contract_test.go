package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// Contract tests of the managed write entry: links are never traversed, an
// edit never creates, a worktree grant is good only for the checkout it was
// made for, unrelated checkouts' metadata is not read, and a started
// process's directories are kept. All in synthetic temporary workspaces.

func readSHA(t *testing.T, f *boardFixture, token, path string) string {
	t.Helper()
	res := postFile(f, token, map[string]any{"op": "read", "path": path})
	var read struct {
		SHA256 string `json:"sha256"`
	}
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &read) != nil || read.SHA256 == "" {
		t.Fatalf("read %s: %d %s", path, res.Code, res.Body)
	}
	return read.SHA256
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable here, not tested: %v", err)
	}
}

func TestManagedWriteAndEditKeepWorking(t *testing.T) {
	f := newBoardFixture(t)
	y := f.hire("alice")
	w := startHelper(t, f)
	scratch, _, _ := f.m.ProcessTemp(w.Token)
	doc := filepath.Join(y.Path, filepath.FromSlash(scratch.Rel), "new", "deep", "doc.txt")
	if code := postFile(f, w.Token, map[string]any{"op": "write", "path": doc, "content": "one\r\ntwo\n"}).Code; code != 200 {
		t.Fatalf("plain write creating directories: %d", code)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(doc, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	sum := readSHA(t, f, w.Token, doc)
	if code := postFile(f, w.Token, map[string]any{"op": "write", "path": doc, "content": "one\r\nTWO\n", "expect": sum}).Code; code != 200 {
		t.Fatalf("edit at the version read: %d", code)
	}
	if got := readString(t, doc); got != "one\r\nTWO\n" {
		t.Fatalf("edited content %q", got)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(doc); info.Mode().Perm() != 0o640 {
			t.Fatalf("edit changed the mode to %v", info.Mode().Perm())
		}
	}
	if code := postFile(f, w.Token, map[string]any{"op": "access", "path": doc}).Code; code != 200 {
		t.Fatalf("access: %d", code)
	}
	if code := postFile(f, w.Token, map[string]any{"op": "mkdir", "path": filepath.Join(filepath.Dir(doc), "more", "dirs")}).Code; code != 200 {
		t.Fatalf("mkdir: %d", code)
	}
}

func TestManagedFilesRefuseLinksInsideTheGrant(t *testing.T) {
	f := newBoardFixture(t)
	y := f.hire("alice")
	w := startHelper(t, f)
	scratch, _, _ := f.m.ProcessTemp(w.Token)
	in := func(name string) string {
		return filepath.Join(y.Path, filepath.FromSlash(scratch.Rel), filepath.FromSlash(name))
	}
	if code := postFile(f, w.Token, map[string]any{"op": "write", "path": in("real/doc.txt"), "content": "KEEP"}).Code; code != 200 {
		t.Fatalf("ordinary write: %d", code)
	}
	sum := readSHA(t, f, w.Token, in("real/doc.txt"))
	symlinkOrSkip(t, "doc.txt", in("real/alias.txt")) // a link to a file in the same directory
	symlinkOrSkip(t, "real", in("dir"))               // a link to a directory in the grant

	refused := []map[string]any{
		{"op": "write", "path": in("real/alias.txt"), "content": "THROUGH LEAF LINK"},
		{"op": "write", "path": in("real/alias.txt"), "content": "THROUGH LEAF LINK", "expect": sum},
		{"op": "write", "path": in("dir/doc.txt"), "content": "THROUGH PARENT LINK"},
		{"op": "write", "path": in("dir/new.txt"), "content": "THROUGH PARENT LINK"},
		{"op": "read", "path": in("real/alias.txt")},
		{"op": "read", "path": in("dir/doc.txt")},
		{"op": "access", "path": in("dir/doc.txt")},
		{"op": "mkdir", "path": in("dir/sub")},
	}
	for _, req := range refused {
		if res := postFile(f, w.Token, req); res.Code == 200 {
			t.Errorf("%v through a link answered 200: %s", req["op"], res.Body)
		}
	}
	if got := readString(t, in("real/doc.txt")); got != "KEEP" {
		t.Fatalf("the linked file changed to %q", got)
	}
	for _, p := range []string{in("real/new.txt"), in("real/sub")} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("%s was created through a link", p)
		}
	}
	if info, err := os.Lstat(in("dir")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link itself was replaced")
	}
	// The real names still work.
	if code := postFile(f, w.Token, map[string]any{"op": "write", "path": in("real/doc.txt"), "content": "EDITED", "expect": sum}).Code; code != 200 {
		t.Fatalf("edit by the real name: %d", code)
	}
}

func TestManagedEditDoesNotRecreateWhatWasRemoved(t *testing.T) {
	f := newBoardFixture(t)
	y := f.hire("alice")
	w := startHelper(t, f)
	scratch, _, _ := f.m.ProcessTemp(w.Token)
	dir := filepath.Join(y.Path, filepath.FromSlash(scratch.Rel), "sub")
	doc := filepath.Join(dir, "doc.txt")
	if code := postFile(f, w.Token, map[string]any{"op": "write", "path": doc, "content": "ORIGINAL"}).Code; code != 200 {
		t.Fatalf("write: %d", code)
	}
	sum := readSHA(t, f, w.Token, doc)

	// The file is removed after the edit read it.
	if err := os.Remove(doc); err != nil {
		t.Fatal(err)
	}
	if code := postFile(f, w.Token, map[string]any{"op": "write", "path": doc, "content": "EDITED", "expect": sum}).Code; code == 200 {
		t.Fatal("an edit of a removed file was written")
	}
	if _, err := os.Lstat(doc); err == nil {
		t.Fatal("the refused edit recreated the removed file")
	}
	// Its directory too.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if code := postFile(f, w.Token, map[string]any{"op": "write", "path": doc, "content": "EDITED", "expect": sum}).Code; code == 200 {
		t.Fatal("an edit in a removed directory was written")
	}
	if _, err := os.Lstat(dir); err == nil {
		t.Fatal("the refused edit recreated the removed directory")
	}
	// An empty expectation is still an edit, never a create.
	if code := postFile(f, w.Token, map[string]any{"op": "write", "path": doc, "content": "EDITED", "expect": ""}).Code; code == 200 {
		t.Fatal("an edit with an empty version created the file")
	}
	if _, err := os.Lstat(dir); err == nil {
		t.Fatal("an edit with an empty version created the directory")
	}
	// A plain write still creates both.
	if code := postFile(f, w.Token, map[string]any{"op": "write", "path": doc, "content": "NEW"}).Code; code != 200 {
		t.Fatalf("plain write: %d", code)
	}
	if got := readString(t, doc); got != "NEW" {
		t.Fatalf("content %q", got)
	}
}

// worktreeFixture is a worker with an author checkout of a local pool.
type worktreeFixture struct {
	f  *boardFixture
	cf *cleanupFixture
	y  yesmen.Yesman
	w  childTrace
	me caller
}

func newWorktreeFixture(t *testing.T) *worktreeFixture {
	cf := newCleanupFixture(t)
	f := newBoardFixture(t)
	f.srv.Pool = cf.s.Pool
	y := f.hire("alice")
	w := startHelper(t, f)
	str := func(s string) *string { return &s }
	if _, err := f.store.ChangeBoard(y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: str("Task"), Owner: str("helper"), Status: str("doing")}}}); err != nil {
		t.Fatal(err)
	}
	info, err := f.m.Authenticate(w.Token)
	if err != nil {
		t.Fatal(err)
	}
	return &worktreeFixture{f: f, cf: cf, y: y, w: w, me: caller{agent: info, yesman: y, token: w.Token}}
}

func (x *worktreeFixture) checkout(t *testing.T, dir, branch string) {
	t.Helper()
	body, _ := json.Marshal(CheckoutRequest{Repo: x.cf.repo.Name, Dir: dir, Branch: branch, Task: "t1", Purpose: "author"})
	if _, err := x.f.srv.checkout(context.Background(), x.me, httptest.NewRequest("POST", "/v1/checkout", bytes.NewReader(body))); err != nil {
		t.Fatal(err)
	}
}

func (x *worktreeFixture) write(path, content string) int {
	return postFile(x.f, x.w.Token, map[string]any{"op": "write", "path": path, "content": content}).Code
}

func TestManagedWorktreeGrantIsForTheCheckoutItWasMadeFor(t *testing.T) {
	x := newWorktreeFixture(t)
	x.checkout(t, "mine", "feature")
	mine := filepath.Join(x.y.Path, "mine")
	if code := x.write(filepath.Join(mine, "feature"), "first\n"); code != 200 {
		t.Fatalf("current owner's checkout: %d", code)
	}

	// A grant chosen before the checkout was replaced is not good for the
	// replacement: the commit checks the instance of what it opened.
	grant, sub, err := x.f.srv.fileGrant(x.me, x.y, filepath.Join(mine, "feature"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(mine, mine+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mine, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "feature"), []byte("REPLACEMENT"), 0o644); err != nil {
		t.Fatal(err)
	}
	content := "STALE GRANT"
	err = x.f.srv.Pool.WithWorktree(mine, func() error {
		_, err := x.f.srv.commitFile(x.y, grant, sub, fileRequest{Op: "write", Content: &content})
		return err
	})
	if err == nil {
		t.Fatal("a grant made for the checkout wrote into its replacement")
	}
	if got := readString(t, filepath.Join(mine, "feature")); got != "REPLACEMENT" {
		t.Fatalf("replacement changed to %q", got)
	}
	if code := x.write(filepath.Join(mine, "feature"), "x"); code == 200 {
		t.Fatal("a new request wrote into the replacement")
	}
	// Put the checkout back: it is the owner's again.
	if err := os.RemoveAll(mine); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(mine+".moved", mine); err != nil {
		t.Fatal(err)
	}
	if code := x.write(filepath.Join(mine, "feature"), "second\n"); code != 200 {
		t.Fatalf("restored checkout: %d", code)
	}

	// The pool's removal waits for a write in progress, and a write waits
	// for the removal.
	held, release := make(chan struct{}), make(chan struct{})
	go x.f.srv.Pool.WithWorktree(mine, func() error { close(held); <-release; return nil })
	<-held
	removed := make(chan error, 1)
	go func() { removed <- x.f.srv.Pool.RemoveWorktree(context.Background(), mine, true) }()
	select {
	case err := <-removed:
		t.Fatalf("removal did not wait for the write in progress: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	if code := x.write(filepath.Join(mine, "feature"), "after removal"); code == 200 {
		t.Fatal("wrote into a removed checkout")
	}
	if _, err := os.Lstat(mine); err == nil {
		t.Fatal("a write recreated the removed checkout's directory")
	}

	// A new checkout at the same place is a new instance: the old grant is
	// refused, a new request by the current owner is granted.
	x.checkout(t, "mine", "feature")
	err = x.f.srv.Pool.WithWorktree(mine, func() error {
		_, err := x.f.srv.commitFile(x.y, grant, sub, fileRequest{Op: "write", Content: &content})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "not the checkout") {
		t.Fatalf("the old instance's grant on the new checkout: %v", err)
	}
	if code := x.write(filepath.Join(mine, "feature"), "third\n"); code != 200 {
		t.Fatalf("the new checkout of the current owner: %d", code)
	}
	if got := readString(t, filepath.Join(mine, "feature")); got != "third\n" {
		t.Fatalf("content %q", got)
	}
}

func TestManagedFilesDoNotReadUnrelatedCheckouts(t *testing.T) {
	x := newWorktreeFixture(t)
	x.checkout(t, "mine", "feature")
	x.checkout(t, "other", "other")
	other := filepath.Join(x.y.Path, "other")
	// The unrelated checkout's .git becomes something no reader may block
	// on or take whole: a large file here, a FIFO in the Unix test.
	if err := os.Remove(filepath.Join(other, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, ".git"), bytes.Repeat([]byte("x"), maxGitFile+1), 0o644); err != nil {
		t.Fatal(err)
	}
	scratch, _, _ := x.f.m.ProcessTemp(x.w.Token)
	if code := x.write(filepath.Join(x.y.Path, filepath.FromSlash(scratch.Rel), "a.txt"), "scratch"); code != 200 {
		t.Fatalf("scratch write next to a broken checkout: %d", code)
	}
	if code := x.write(filepath.Join(x.y.Path, "mine", "feature"), "mine\n"); code != 200 {
		t.Fatalf("own checkout next to a broken one: %d", code)
	}
	if code := x.write(filepath.Join(other, "feature"), "x"); code == 200 {
		t.Fatal("wrote a checkout whose .git is not a small regular file")
	}
	if got := readString(t, filepath.Join(other, "feature")); got == "x" {
		t.Fatal("the broken checkout's file changed")
	}
}

func TestStartedProcessKeepsItsDirectoriesWhenStartFails(t *testing.T) {
	for _, how := range []string{"fail", "hang"} {
		t.Run(how, func(t *testing.T) {
			exe, _ := os.Executable()
			dir := t.TempDir()
			store := yesmen.New(filepath.Join(dir, "yesmen"), filepath.Join(dir, "archive"))
			y, err := store.Hire("alice")
			if err != nil {
				t.Fatal(err)
			}
			traces := filepath.Join(dir, "traces")
			os.Mkdir(traces, 0o700)
			// The first start writes its diagnostics, then fails before
			// pi answers, or never answers until Stop cancels the start.
			if err := os.WriteFile(filepath.Join(traces, "init-"+how), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			m := agent.New(agent.Config{Pi: exe, Extension: filepath.Join(dir, "none.ts"), Yesmen: store, Env: []string{"YESMAN_TEST_RPC_CHILD=" + traces, "GORACE=atexit_sleep_ms=0"}})
			t.Cleanup(m.StopAll)
			started := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				started <- m.Prompt(ctx, "alice/lead", "hello")
			}()
			if how == "hang" {
				// The child has run and written its diagnostics; Stop
				// cancels the start that waits for pi's answer.
				deadline := time.Now().Add(10 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(traces, "init-"+how+".done")); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("the child did not write its diagnostics")
					}
					time.Sleep(10 * time.Millisecond)
				}
				m.Stop("alice/lead")
			}
			if err := <-started; err == nil {
				t.Fatal("the start did not fail")
			}
			var kept []string
			for _, root := range []string{".yesman/tmp/_agent/lead", ".yesman/evidence/_agent/lead"} {
				entries, _ := os.ReadDir(filepath.Join(y.Path, filepath.FromSlash(root)))
				if len(entries) != 1 {
					t.Fatalf("%s after a failed start: %v", root, entries)
				}
				diag := filepath.Join(y.Path, filepath.FromSlash(root), entries[0].Name(), "diagnostic.txt")
				if got := readString(t, diag); got != "KEEP" {
					t.Fatalf("diagnostic %q", got)
				}
				kept = append(kept, entries[0].Name())
			}
			// The next start gets new directories; the kept ones stay.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := m.Prompt(ctx, "alice/lead", "hello"); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(filepath.Join(y.Path, ".yesman", "tmp", "_agent", "lead"))
			if len(entries) != 2 || (entries[0].Name() != kept[0] && entries[1].Name() != kept[0]) {
				t.Fatalf("after the next start: %v", entries)
			}
		})
	}
}

// failInit runs in the pi stand-in: when its trace directory asks for it,
// once, it writes diagnostics into its directories and then fails, or
// stops answering, before pi would have started.
func failInit(dir string) {
	for _, how := range []string{"fail", "hang"} {
		flag := filepath.Join(dir, "init-"+how)
		if os.Remove(flag) != nil {
			continue
		}
		for _, key := range []string{"YESMAN_TMP", "YESMAN_EVIDENCE"} {
			_ = os.WriteFile(filepath.Join(os.Getenv(key), "diagnostic.txt"), []byte("KEEP"), 0o600)
		}
		_ = os.WriteFile(flag+".done", nil, 0o600)
		if how == "fail" {
			os.Exit(4)
		}
		buf := make([]byte, 1)
		for {
			if _, err := os.Stdin.Read(buf); err != nil {
				os.Exit(0)
			}
		}
	}
}
