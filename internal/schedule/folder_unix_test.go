//go:build unix

package schedule

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A FIFO at plans.json, or in place of the workspace, is refused without
// being opened: nothing waits for a writer, and it is left as it is.
func TestFIFOIsNotOpened(t *testing.T) {
	for _, c := range []struct {
		name string
		path func(e *env) string
	}{
		{"plans.json", func(e *env) string { return filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules", "plans.json") }},
		{"workspace", func(e *env) string { return filepath.Join(e.root, "yesmen", ws) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
			e.save(PlanInput{Schedule: daily("09:00")})
			e.close()
			path := c.path(e)
			if err := os.Rename(path, filepath.Join(e.root, "moved")); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Skip("no FIFOs here")
			}
			e.restart("")
			if v, err := e.svc.View(ws); err == nil && v.Error == "" {
				t.Fatalf("read a FIFO: %+v", v)
			}
			if _, err := e.svc.Save(ws, PlanInput{Title: "T", Instructions: "I", Schedule: daily("10:00"), Timezone: "UTC"}); err == nil {
				t.Fatal("saved over a FIFO")
			}
			if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
				t.Fatalf("the FIFO was replaced: %v", err)
			}
		})
	}
}

// swapInFIFO puts a FIFO at path the first time a file named name is about
// to be opened to be read, once it was found a regular file there: the file
// is kept aside, and put back when the test ends. The channel closes once
// the FIFO is in place.
func swapInFIFO(t *testing.T, name, path string) <-chan struct{} {
	t.Helper()
	in := make(chan struct{})
	var once sync.Once
	beforeOpenRead = func(n string) {
		if n != name {
			return
		}
		once.Do(func() {
			defer close(in)
			if err := os.Rename(path, path+".kept"); err != nil {
				t.Error(err)
				return
			}
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(func() {
		beforeOpenRead = nil
		putBack(t, path)
	})
	return in
}

// putBack puts the file kept aside by swapInFIFO back in place of the FIFO.
func putBack(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path + ".kept"); err != nil {
		return
	}
	if err := os.Remove(path); err != nil {
		t.Error(err)
	}
	if err := os.Rename(path+".kept", path); err != nil {
		t.Error(err)
	}
}

// endsUnwritten reports whether done closes within limit while nothing
// writes to the FIFO at path. If not, a writer comes for a moment so that
// whatever waits on it ends, and the test fails instead of hanging.
func endsUnwritten(t *testing.T, path string, done <-chan struct{}, limit time.Duration) bool {
	t.Helper()
	select {
	case <-done:
		return true
	case <-time.After(limit):
	}
	if fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
		syscall.Close(fd)
	}
	<-done
	return false
}

// noReader checks that nothing holds the FIFO at path open to read it.
func noReader(t *testing.T, path string) {
	t.Helper()
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err == nil {
		syscall.Close(fd)
		t.Error("the FIFO is still open to be read")
	} else if !errors.Is(err, syscall.ENXIO) {
		t.Error(err)
	}
}

// waitIn waits for the FIFO to be put in place.
func waitIn(t *testing.T, in <-chan struct{}) {
	t.Helper()
	select {
	case <-in:
	case <-time.After(10 * time.Second):
		t.Fatal("the file was never opened to be read")
	}
}

// A FIFO put in place of yesman.json or plans.json after it was found a
// regular file, just before it is opened, is refused on its handle at once:
// nothing waits for a writer.
func TestFIFOSwappedInIsNotWaitedOn(t *testing.T) {
	t.Run("yesman.json", func(t *testing.T) {
		e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
		e.save(PlanInput{Schedule: daily("09:00")})
		y, err := e.store.Get(ws)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(y.MetaDir(), "yesman.json")
		kept, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		in := swapInFIFO(t, "yesman.json", path)
		done, errc := make(chan struct{}), make(chan error, 1)
		go func() {
			defer close(done)
			dir, err := openSchedules(y, false)
			if dir != nil {
				dir.Close()
			}
			errc <- err
		}()
		waitIn(t, in)
		if !endsUnwritten(t, path, done, 2*time.Second) {
			t.Fatal("opening the workspace waited for a writer to the FIFO")
		}
		if err := <-errc; !errors.As(err, new(errUnsafePath)) {
			t.Fatalf("the FIFO was not refused: %v", err)
		}
		noReader(t, path)
		if again, err := os.ReadFile(path + ".kept"); err != nil || !bytes.Equal(again, kept) {
			t.Fatal("yesman.json changed")
		}
	})
	t.Run("plans.json", func(t *testing.T) {
		e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
		e.save(PlanInput{Schedule: daily("09:00")})
		y, err := e.store.Get(ws)
		if err != nil {
			t.Fatal(err)
		}
		path := plansPath(y)
		e.restart("")
		in := swapInFIFO(t, "plans.json", path)
		done, viewed := make(chan struct{}), make(chan View, 1)
		go func() {
			defer close(done)
			v, _ := e.svc.View(ws)
			viewed <- v
		}()
		waitIn(t, in)
		if !endsUnwritten(t, path, done, 2*time.Second) {
			t.Fatal("reading the tasks waited for a writer to the FIFO")
		}
		if v := <-viewed; v.Error == "" || len(v.Plans) != 0 {
			t.Fatalf("the FIFO was read: %+v", v)
		}
		noReader(t, path)
	})
}

// Save's last check before it replaces plans.json does not wait on a FIFO
// put there after the file was found the one read: the Save is refused as
// a change on disk, and the file kept aside is left as it was. Put back, it
// reads as before the Save.
func TestSaveDoesNotWaitOnAFIFOSwappedInBeforeItsLastCheck(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
	p := e.save(PlanInput{Schedule: daily("09:00")})
	y, err := e.store.Get(ws)
	if err != nil {
		t.Fatal(err)
	}
	path := plansPath(y)
	kept, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	in := swapInFIFO(t, "plans.json", path)
	done, saved := make(chan struct{}), make(chan error, 1)
	go func() {
		defer close(done)
		_, err := e.svc.Save(ws, PlanInput{ID: p.ID, Revision: p.Revision, Title: "New title", Instructions: "I", Schedule: daily("09:00"), Timezone: "UTC"})
		saved <- err
	}()
	waitIn(t, in)
	if !endsUnwritten(t, path, done, 2*time.Second) {
		t.Fatal("the Save waited for a writer to the FIFO")
	}
	if err := <-saved; !errors.Is(err, ErrConflict) {
		t.Fatalf("the Save over a FIFO: %v", err)
	}
	noReader(t, path)
	if again, err := os.ReadFile(path + ".kept"); err != nil || !bytes.Equal(again, kept) {
		t.Fatal("plans.json changed")
	}
	if names, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp")); len(names) != 0 {
		t.Fatalf("left behind: %v", names)
	}
	beforeOpenRead = nil
	putBack(t, path)
	if got := e.plan(p.ID); got.Revision != p.Revision || got.Title != p.Title {
		t.Fatalf("after the refused Save: %+v", got)
	}
}

// The background check of an app just opened, meeting such a FIFO in place
// of yesman.json, does not wait on it nor hold up Close: it ends at once,
// Close returns, the lock goes. (The app is restarted so that this is its
// only check: in a second one the store's own reads of yesman.json, not the
// scheduler's, would meet the FIFO.)
func TestCloseIsNotHeldByAFIFOSwappedIntoACheck(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.restart("")
	y, err := e.store.Get(ws)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(y.MetaDir(), "yesman.json")
	in := swapInFIFO(t, "yesman.json", path)
	e.svc.Start()
	waitIn(t, in)
	closed := make(chan struct{})
	go func() { e.svc.Close(); close(closed) }()
	if !endsUnwritten(t, path, closed, closeWait+time.Second) {
		t.Fatal("Close waited on a background check held by the FIFO")
	}
	noReader(t, path)
	if locked(t, e.cfg.LockPath) {
		t.Fatal("the lock was kept after the check ended")
	}
}
