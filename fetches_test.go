package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/pool"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// poolWith makes a pool holding a clone of each named origin.
func poolWith(t *testing.T, names ...string) (*pool.Pool, map[string]string) {
	t.Helper()
	root := t.TempDir()
	p := pool.New(filepath.Join(root, "pool"))
	origins := map[string]string{}
	for _, name := range names {
		dir := filepath.Join(root, "src", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "init", "--quiet", "--initial-branch=main")
		gitIn(t, dir, "commit", "--quiet", "--allow-empty", "-m", "first")
		r, err := p.Add(context.Background(), dir, "")
		if err != nil {
			t.Fatal(err)
		}
		origins[r.Name] = dir
	}
	return p, origins
}

// stateLog keeps every state the fetcher reports.
type stateLog struct {
	mu   sync.Mutex
	last FetchState
	seen []FetchState
}

func (l *stateLog) add(s FetchState) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.last = s
	l.seen = append(l.seen, s)
}

func (l *stateLog) wait(t *testing.T, done func(FetchState) bool) FetchState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		l.mu.Lock()
		s := l.last
		l.mu.Unlock()
		if done(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("fetch state %+v", s)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFetchAllReportsEachRepository(t *testing.T) {
	p, origins := poolWith(t, "one", "two", "three", "four")
	log := &stateLog{}
	f := newFetcher(p, log.add)

	// One repository's remote goes missing: its error is kept, the others
	// fetch.
	broken := "local/two"
	if err := os.RemoveAll(origins[broken]); err != nil {
		t.Fatal(err)
	}
	if err := f.FetchAll(true); err != nil {
		t.Fatal(err)
	}
	s := log.wait(t, func(s FetchState) bool { return s.Fetching != nil && len(s.Fetching) == 0 })
	if len(s.Errors) != 1 || s.Errors[broken] == "" {
		t.Fatalf("errors = %v", s.Errors)
	}
	if first := log.seen[0]; len(first.Fetching) != 4 {
		t.Errorf("fetching all first showed %v", first.Fetching)
	}
	r, _ := p.Get("local/one")
	if time.Since(r.FetchedAt) > time.Minute {
		t.Errorf("one was fetched at %v", r.FetchedAt)
	}

	// The remote is back: fetching it alone clears its error.
	if err := os.MkdirAll(origins[broken], 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, origins[broken], "init", "--quiet", "--initial-branch=main")
	gitIn(t, origins[broken], "commit", "--quiet", "--allow-empty", "-m", "again")
	if _, err := f.Fetch(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
	if s := f.State(); len(s.Errors) != 0 || len(s.Fetching) != 0 {
		t.Errorf("after fetching two: %+v", s)
	}
}

func TestFetchSchedule(t *testing.T) {
	p, _ := poolWith(t, "one")
	log := &stateLog{}
	f := newFetcher(p, log.add)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.Schedule(ctx)

	time.Sleep(100 * time.Millisecond)
	if n := len(log.seen); n != 0 {
		t.Fatalf("fetched %d times with no schedule", n)
	}
	f.SetInterval(50 * time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for {
		log.mu.Lock()
		n := len(log.seen)
		log.mu.Unlock()
		if n >= 4 { // two rounds: start and finish each
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the schedule fetched %d times", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.SetInterval(0)
	time.Sleep(60 * time.Millisecond) // a round under way finishes
	log.mu.Lock()
	n := len(log.seen)
	log.mu.Unlock()
	time.Sleep(200 * time.Millisecond)
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.seen) != n {
		t.Errorf("fetched after the schedule stopped")
	}
}
