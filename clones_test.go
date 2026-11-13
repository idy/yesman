package main

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/pool"
)

// fakeAdds blocks each add until the test releases it.
type fakeAdds struct {
	mu      sync.Mutex
	running map[string][]chan error // per repository, oldest first
	started chan string
}

func newFakeAdds() *fakeAdds {
	return &fakeAdds{running: map[string][]chan error{}, started: make(chan string, 16)}
}

func (f *fakeAdds) add(ctx context.Context, repo string) (pool.Repo, error) {
	done := make(chan error)
	f.mu.Lock()
	f.running[repo] = append(f.running[repo], done)
	f.mu.Unlock()
	f.started <- repo
	select {
	case err := <-done:
		if err != nil {
			return pool.Repo{}, err
		}
	case <-ctx.Done():
		return pool.Repo{}, ctx.Err()
	}
	return pool.Repo{Name: "github.com/" + repo}, nil
}

func (f *fakeAdds) finish(t *testing.T, repo string, err error) {
	t.Helper()
	f.mu.Lock()
	var done chan error
	if q := f.running[repo]; len(q) > 0 {
		done, f.running[repo] = q[0], q[1:]
	}
	if len(f.running[repo]) == 0 {
		delete(f.running, repo)
	}
	f.mu.Unlock()
	if done == nil {
		t.Fatalf("%s is not cloning", repo)
	}
	done <- err
}

func (f *fakeAdds) waitStarted(t *testing.T) string {
	t.Helper()
	select {
	case repo := <-f.started:
		return repo
	case <-time.After(5 * time.Second):
		t.Fatal("no clone started")
		return ""
	}
}

func states(list []Clone) map[string]CloneState {
	m := map[string]CloneState{}
	for _, c := range list {
		m[c.Repo] = c.State
	}
	return m
}

// waitFor polls the list until it matches.
func waitForClones(t *testing.T, c *clones, want map[string]CloneState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := states(c.List())
		if len(got) == len(want) {
			same := true
			for k, v := range want {
				if got[k] != v {
					same = false
				}
			}
			if same {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("clones = %v, want %v", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestClonesQueueInTheBackground(t *testing.T) {
	f := newFakeAdds()
	var mu sync.Mutex
	arrived := 0
	c := newClones(f.add, func(_ []Clone, a bool) {
		if a {
			mu.Lock()
			arrived++
			mu.Unlock()
		}
	})

	list, err := c.Queue([]string{"a/one", "a/two", "https://github.com/a/three", "a/four", "a/one"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 {
		t.Fatalf("Queue listed %v", list)
	}
	// Three clone at once; the fourth waits for a slot.
	for range cloneSlots {
		f.waitStarted(t)
	}
	select {
	case repo := <-f.started:
		t.Fatalf("%s started beyond the slots", repo)
	case <-time.After(50 * time.Millisecond):
	}
	f.mu.Lock()
	var cloning []string
	for repo := range f.running {
		cloning = append(cloning, repo)
	}
	f.mu.Unlock()
	if len(cloning) != cloneSlots {
		t.Fatalf("cloning %v", cloning)
	}

	// A URL keeps its protocol on the way to add.
	waitForClones(t, c, map[string]CloneState{"a/one": CloneCloning, "a/two": CloneCloning, "a/three": CloneCloning, "a/four": CloneQueued})
	f.finish(t, "a/one", nil)
	f.finish(t, "https://github.com/a/three", errors.New("git clone: repository not found"))
	if f.waitStarted(t) != "a/four" {
		t.Fatal("the queued clone did not take the free slot")
	}
	waitForClones(t, c, map[string]CloneState{"a/two": CloneCloning, "a/three": CloneFailed, "a/four": CloneCloning})
	for _, cl := range c.List() {
		if cl.Repo == "a/three" && cl.Error != "git clone: repository not found" {
			t.Errorf("failed clone's error = %q", cl.Error)
		}
	}

	// A failure is tried again when it is added again, or dismissed.
	if _, err := c.Queue([]string{"a/three"}); err != nil {
		t.Fatal(err)
	}
	f.finish(t, "a/two", nil)
	f.waitStarted(t)
	f.finish(t, "a/three", errors.New("still missing"))
	waitForClones(t, c, map[string]CloneState{"a/three": CloneFailed, "a/four": CloneCloning})
	c.Dismiss("A/Three")
	f.finish(t, "a/four", nil)
	waitForClones(t, c, map[string]CloneState{})

	mu.Lock()
	defer mu.Unlock()
	if arrived != 3 {
		t.Errorf("%d repositories arrived, want 3", arrived)
	}
}

func TestClonesQueueChecksEveryInputFirst(t *testing.T) {
	c := newClones(newFakeAdds().add, nil)
	if _, err := c.Queue([]string{"a/one", "gitlab.com/a/two"}); err == nil {
		t.Fatal("a GitLab repository was queued")
	}
	if list := c.List(); len(list) != 0 {
		t.Errorf("queued %v", list)
	}
}

// An agent's addition takes over a clone of the same repository the page
// queued, so that clone does not wait on after the repository arrived.
func TestClonesAddTakesOverAQueuedClone(t *testing.T) {
	f := newFakeAdds()
	c := newClones(f.add, nil)
	if _, err := c.Queue([]string{"a/one", "a/two", "a/three", "a/four"}); err != nil {
		t.Fatal(err)
	}
	for range cloneSlots {
		f.waitStarted(t)
	}
	errs := make(chan error)
	go func() {
		_, err := c.Add(context.Background(), "a/four")
		errs <- err
	}()
	if repo := f.waitStarted(t); repo != "a/four" {
		t.Fatalf("the agent's add started %s", repo)
	}
	waitForClones(t, c, map[string]CloneState{"a/one": CloneCloning, "a/two": CloneCloning, "a/three": CloneCloning, "a/four": CloneCloning})
	f.finish(t, "a/four", nil)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	waitForClones(t, c, map[string]CloneState{"a/one": CloneCloning, "a/two": CloneCloning, "a/three": CloneCloning})
	for _, repo := range []string{"a/one", "a/two", "a/three"} {
		f.finish(t, repo, nil)
	}
	waitForClones(t, c, map[string]CloneState{})
	select {
	case repo := <-f.started:
		t.Fatalf("%s cloned again", repo)
	case <-time.After(50 * time.Millisecond):
	}
}

// An agent's addition waits for its clone, shows while it runs, and
// leaves nothing behind when it fails.
func TestClonesAddForAnAgent(t *testing.T) {
	f := newFakeAdds()
	c := newClones(f.add, nil)
	errs := make(chan error)
	go func() {
		_, err := c.Add(context.Background(), "a/one")
		errs <- err
	}()
	f.waitStarted(t)
	waitForClones(t, c, map[string]CloneState{"a/one": CloneCloning})
	f.finish(t, "a/one", errors.New("denied"))
	if err := <-errs; err == nil || err.Error() != "denied" {
		t.Fatalf("Add = %v", err)
	}
	waitForClones(t, c, map[string]CloneState{})
}

// Withdraw stops every clone but those kept and an agent's: queued and
// failed ones go at once, one under way once its cancelled clone ends.
func TestClonesWithdraw(t *testing.T) {
	f := newFakeAdds()
	c := newClones(f.add, nil)
	if _, err := c.Queue([]string{"a/keep", "a/run", "a/fail", "a/wait1", "a/wait2"}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		f.waitStarted(t)
	}
	f.finish(t, "a/fail", errors.New("boom"))
	f.waitStarted(t) // a/wait1 takes the slot
	agent := make(chan error, 1)
	go func() {
		_, err := c.Add(context.Background(), "a/agent")
		agent <- err
	}()
	f.waitStarted(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopped, held, err := c.Withdraw(ctx, func(repo string) bool { return repo == "a/keep" })
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/run", "a/fail", "a/wait1", "a/wait2"}; !slices.Equal(stopped, want) {
		t.Errorf("stopped %q, want %q", stopped, want)
	}
	if !slices.Equal(held, []string{"a/agent"}) {
		t.Errorf("held %q", held)
	}
	// A cancelled clone leaves no failure behind, and wait2 never starts.
	waitForClones(t, c, map[string]CloneState{"a/keep": CloneCloning, "a/agent": CloneCloning})
	f.finish(t, "a/keep", nil)
	f.finish(t, "a/agent", nil)
	if err := <-agent; err != nil {
		t.Errorf("the agent's clone = %v", err)
	}
	waitForClones(t, c, map[string]CloneState{})
}

// An agent that joins a clone the page started keeps it from Withdraw.
func TestClonesWithdrawKeepsJoinedClone(t *testing.T) {
	f := newFakeAdds()
	c := newClones(f.add, nil)
	if _, err := c.Queue([]string{"a/shared"}); err != nil {
		t.Fatal(err)
	}
	f.waitStarted(t)
	agent := make(chan error, 1)
	go func() {
		_, err := c.Add(context.Background(), "a/shared")
		agent <- err
	}()
	f.waitStarted(t) // the agent's own add, which the pool would make wait
	stopped, held, err := c.Withdraw(context.Background(), func(string) bool { return false })
	if err != nil || len(stopped) != 0 || !slices.Equal(held, []string{"a/shared"}) {
		t.Fatalf("Withdraw = %q, %q, %v; want the joined clone held", stopped, held, err)
	}
	waitForClones(t, c, map[string]CloneState{"a/shared": CloneCloning})
	f.finish(t, "a/shared", nil)
	f.finish(t, "a/shared", nil)
	if err := <-agent; err != nil {
		t.Errorf("the agent's clone = %v", err)
	}
	waitForClones(t, c, map[string]CloneState{})
	// Once the agent is done, the clone no longer counts as its.
	if _, err := c.Queue([]string{"a/shared"}); err != nil {
		t.Fatal(err)
	}
	f.waitStarted(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if stopped, held, err := c.Withdraw(ctx, func(string) bool { return false }); err != nil || !slices.Equal(stopped, []string{"a/shared"}) || len(held) != 0 {
		t.Errorf("Withdraw after the agent = %q, %q, %v", stopped, held, err)
	}
}
