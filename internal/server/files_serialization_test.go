package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// These ordinary operations exercise the server's workspace serialization,
// independently of the queue in each pi process. The real Manager and Store
// use only synthetic workspaces and the existing RPC test child.
func fileWorker(t *testing.T, f *boardFixture, name string) (childTrace, string) {
	t.Helper()
	y := f.hire(name)
	f.start(name + "/lead")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.m.StartWorker(ctx, name, "helper", "Helper", "help"); err != nil {
		t.Fatal(err)
	}
	w := f.start(name + "/helper")
	scratch, _, ok := f.m.ProcessTemp(w.Token)
	if !ok {
		t.Fatal("worker has no scratch directory")
	}
	return w, filepath.Join(y.Path, filepath.FromSlash(scratch.Rel))
}

func fileResult(t *testing.T, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(10 * time.Second):
		t.Fatal("managed file request did not finish")
		return nil
	}
}

// Signal that the authenticated request reached its body, without holding the
// body reader. The workspace lock, held by the test, is its only remaining gate.
type fileBodyRead struct {
	io.Reader
	once sync.Once
	read chan struct{}
}

func (b *fileBodyRead) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.once.Do(func() { close(b.read) })
	return n, err
}

func queuedFile(t *testing.T, f *boardFixture, token string, body map[string]any) func() *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	b := &fileBodyRead{Reader: bytes.NewReader(raw), read: make(chan struct{})}
	done := make(chan struct{})
	res := httptest.NewRecorder()
	go func() {
		req := httptest.NewRequest("POST", "/v1/files", b)
		req.Header.Set("Authorization", "Bearer "+token)
		f.srv.Handler().ServeHTTP(res, req)
		close(done)
	}()
	join := func() *httptest.ResponseRecorder {
		select {
		case <-done:
			return res
		case <-time.After(10 * time.Second):
			t.Fatal("queued file request did not finish")
			return nil
		}
	}
	t.Cleanup(func() { join() })
	select {
	case <-b.read:
	case <-time.After(10 * time.Second):
		t.Fatal("file request did not read its body")
	}
	// No commit instrumentation or changed-file window: this only checks the
	// ordinary lock contract while the server's workspace mutex is held.
	select {
	case <-done:
		t.Fatalf("file request finished while workspace was locked: %d %s", res.Code, res.Body)
	case <-time.After(25 * time.Millisecond):
	}
	return join
}

func TestManagedFilesSerializeWithinWorkspace(t *testing.T) {
	f := newBoardFixture(t)
	alice, adir := fileWorker(t, f, "alice")
	bob, bdir := fileWorker(t, f, "bob")
	doc := filepath.Join(adir, "doc.txt")
	if res := postFile(f, alice.Token, map[string]any{"op": "write", "path": doc, "content": "one\r\ntwo\n"}); res.Code != 200 {
		t.Fatalf("initial write: %d %s", res.Code, res.Body)
	}
	sum := readSHA(t, f, alice.Token, doc)
	for _, op := range []string{"read", "write", "access", "mkdir"} {
		t.Run(op, func(t *testing.T) {
			l := f.srv.worktreeLock("alice")
			l.Lock()
			unlock := sync.OnceFunc(l.Unlock)
			defer unlock() // unlock before queuedFile's cleanup joins
			req := map[string]any{"op": op, "path": doc}
			if op == "write" {
				req["content"], req["expect"] = "one\r\nTWO\n", sum
			} else if op == "mkdir" {
				req["path"] = filepath.Join(adir, "new", "directory")
			}
			done := queuedFile(t, f, alice.Token, req)
			other := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				other <- postFile(f, bob.Token, map[string]any{"op": "write", "path": filepath.Join(bdir, op+".txt"), "content": "independent"})
			}()
			res := fileResult(t, other)
			if res.Code != 200 || readString(t, filepath.Join(bdir, op+".txt")) != "independent" {
				t.Fatalf("other workspace did not progress: %d %s", res.Code, res.Body)
			}
			unlock()
			res = done()
			if res.Code != 200 {
				t.Fatalf("queued %s: %d %s", op, res.Code, res.Body)
			}
		})
	}
	if got := readString(t, doc); got != "one\r\nTWO\n" {
		t.Fatalf("edited content %q", got)
	}
}

func TestManagedFilesRevisionAndErrorRelease(t *testing.T) {
	f := newBoardFixture(t)
	w, dir := fileWorker(t, f, "alice")
	doc := filepath.Join(dir, "doc.txt")
	if res := postFile(f, w.Token, map[string]any{"op": "write", "path": doc, "content": "original"}); res.Code != 200 {
		t.Fatalf("initial write: %d %s", res.Code, res.Body)
	}
	sum := readSHA(t, f, w.Token, doc)
	// Two ordinary clients editing the same read version: exactly one commit
	// succeeds; the other must reread. Run only against the corrected contract.
	results := make(chan *httptest.ResponseRecorder, 2)
	for _, text := range []string{"first\r\n", "second\r\n"} {
		go func() {
			results <- postFile(f, w.Token, map[string]any{"op": "write", "path": doc, "content": text, "expect": sum})
		}()
	}
	succeeded := 0
	for range 2 {
		res := fileResult(t, results)
		if res.Code == 200 {
			succeeded++
		} else if !strings.Contains(res.Body.String(), "changed since edit read it") {
			t.Fatalf("unexpected edit refusal: %d %s", res.Code, res.Body)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful edits = %d, want 1", succeeded)
	}
	before := readString(t, doc)
	if before != "first\r\n" && before != "second\r\n" {
		t.Fatalf("unexpected committed content %q", before)
	}
	for _, req := range []map[string]any{
		{"op": "write", "path": doc, "content": "stale", "expect": sum},
		{"op": "write", "path": doc},
		{"op": "read", "path": filepath.Join(dir, "missing.txt")},
	} {
		res := postFile(f, w.Token, req)
		if res.Code == 200 || readString(t, doc) != before {
			t.Fatalf("refused operation changed file: %d %s", res.Code, res.Body)
		}
		// Each error releases the lock, and a read returns content and its hash
		// from one complete managed version.
		res = postFile(f, w.Token, map[string]any{"op": "read", "path": doc})
		var got struct{ Data, SHA256 string }
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &got) != nil {
			t.Fatalf("read after refusal: %d %s", res.Code, res.Body)
		}
		data, err := base64.StdEncoding.DecodeString(got.Data)
		want := sha256.Sum256([]byte(before))
		if err != nil || string(data) != before || got.SHA256 != hex.EncodeToString(want[:]) {
			t.Fatalf("incoherent read: %+v, %v", got, err)
		}
	}
}

func TestManagedWorktreeOrdinarySerializedIO(t *testing.T) {
	x := newWorktreeFixture(t)
	x.checkout(t, "mine", "feature")
	doc := filepath.Join(x.y.Path, "mine", "doc.txt")
	if code := x.write(doc, "one\r\n"); code != 200 {
		t.Fatalf("owned checkout write: %d", code)
	}
	sum := readSHA(t, x.f, x.w.Token, doc)
	l := x.f.srv.worktreeLock(x.y.Name)
	l.Lock()
	unlock := sync.OnceFunc(l.Unlock)
	defer unlock()
	done := queuedFile(t, x.f, x.w.Token, map[string]any{"op": "write", "path": doc, "content": "two\r\n", "expect": sum})
	unlock()
	if res := done(); res.Code != 200 {
		t.Fatalf("owned checkout edit: %d %s", res.Code, res.Body)
	}
	if got := readString(t, doc); got != "two\r\n" {
		t.Fatalf("owned checkout content %q", got)
	}
	want := sha256.Sum256([]byte("two\r\n"))
	if got := readSHA(t, x.f, x.w.Token, doc); got != hex.EncodeToString(want[:]) {
		t.Fatalf("owned checkout hash %q", got)
	}
}

func TestManagedFilesRevalidateAfterWorkspaceWait(t *testing.T) {
	for _, archive := range []bool{false, true} {
		t.Run(map[bool]string{false: "process-stopped", true: "workspace-rehired"}[archive], func(t *testing.T) {
			f := newBoardFixture(t)
			w, dir := fileWorker(t, f, "alice")
			doc := filepath.Join(dir, "doc.txt")
			if res := postFile(f, w.Token, map[string]any{"op": "write", "path": doc, "content": "keep"}); res.Code != 200 {
				t.Fatalf("initial write: %d %s", res.Code, res.Body)
			}
			old, err := f.store.Get("alice")
			if err != nil {
				t.Fatal(err)
			}
			l := f.srv.worktreeLock("alice")
			l.Lock()
			unlock := sync.OnceFunc(l.Unlock)
			defer unlock()
			done := queuedFile(t, f, w.Token, map[string]any{"op": "write", "path": doc, "content": "later"})
			retired := make(chan error, 1)
			var kept string
			go func() {
				if archive {
					var err error
					kept, err = f.store.Archive("alice", func(y yesmen.Yesman) error { return f.m.StopYesman(y.Name) })
					retired <- err
				} else {
					retired <- f.m.StopYesman("alice")
				}
			}()
			select {
			case err := <-retired:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("retirement waited on a file request that has not been admitted")
			}
			if archive {
				f.m.ForgetYesman("alice", old.HiredAt)
				if y := f.hire("alice"); y.HiredAt.Equal(old.HiredAt) {
					t.Fatal("rehire did not change workspace identity")
				}
				rel, err := filepath.Rel(old.Path, doc)
				if err != nil {
					t.Fatal(err)
				}
				doc = filepath.Join(kept, rel)
			}
			unlock()
			res := done()
			if res.Code != 401 || readString(t, doc) != "keep" {
				t.Fatalf("retired request: %d %s", res.Code, res.Body)
			}
			// A new caller still makes progress after the queued refusal.
			var fresh childTrace
			if archive {
				fresh = startHelper(t, f)
			} else {
				fresh = f.start("alice/helper")
			}
			next, _, ok := f.m.ProcessTemp(fresh.Token)
			if !ok {
				t.Fatal("new process has no scratch directory")
			}
			y, err := f.store.Get("alice")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(y.Path, filepath.FromSlash(next.Rel), "new.txt")
			if res := postFile(f, fresh.Token, map[string]any{"op": "write", "path": path, "content": "current"}); res.Code != 200 {
				t.Fatalf("new process write: %d %s", res.Code, res.Body)
			}
		})
	}
}
