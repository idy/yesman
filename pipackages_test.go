package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
)

// fakePi runs each pi command once the test answers it.
type fakePi struct {
	mu      sync.Mutex
	running int
	most    int
	calls   chan []string
	answers chan error
}

func newFakePi() *fakePi {
	return &fakePi{calls: make(chan []string, 16), answers: make(chan error)}
}

func (f *fakePi) run(ctx context.Context, args ...string) (string, error) {
	f.mu.Lock()
	f.running++
	f.most = max(f.most, f.running)
	f.mu.Unlock()
	f.calls <- args
	err := <-f.answers
	f.mu.Lock()
	f.running--
	f.mu.Unlock()
	return "", err
}

func (f *fakePi) next(t *testing.T) []string {
	t.Helper()
	select {
	case args := <-f.calls:
		return args
	case <-time.After(5 * time.Second):
		t.Fatal("pi was not run")
		return nil
	}
}

func waitJobs(t *testing.T, j *piJobs, ok func(PiJobs) bool) PiJobs {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if l := j.List(); ok(l) {
			return l
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("jobs = %+v", j.List())
	return PiJobs{}
}

func TestPiJobsRunOneAtATime(t *testing.T) {
	pi := newFakePi()
	var events []PiJobs
	var emu sync.Mutex
	j := newPiJobs(pi.run, func(l PiJobs) { emu.Lock(); events = append(events, l); emu.Unlock() })

	if _, err := j.Queue(PiInstall, "npm:a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Queue(PiRemove, "npm:b", "b"); err != nil {
		t.Fatal(err)
	}
	// The same job again stays as it is.
	l, _ := j.Queue(PiInstall, "npm:a", "")
	if len(l.Jobs) != 2 || l.Jobs[0].State != JobRunning || l.Jobs[1].State != JobQueued || l.Jobs[0].Title != "a" {
		t.Fatalf("jobs = %+v", l)
	}
	if args := pi.next(t); !slices.Equal(args, []string{"install", "npm:a", "--no-approve"}) {
		t.Fatalf("first command = %q", args)
	}
	pi.answers <- nil
	if args := pi.next(t); !slices.Equal(args, []string{"remove", "npm:b", "--no-approve"}) {
		t.Fatalf("second command = %q", args)
	}
	// A job that succeeded leaves the list and marks the change.
	l = j.List()
	if len(l.Jobs) != 1 || l.Jobs[0].Source != "npm:b" || l.ChangedAt.IsZero() {
		t.Fatalf("after the install: %+v", l)
	}
	first := l.ChangedAt
	pi.answers <- errors.New("No matching package found for npm:b")
	l = waitJobs(t, j, func(l PiJobs) bool { return len(l.Jobs) == 1 && l.Jobs[0].State == JobFailed })
	if l.Jobs[0].Error != "No matching package found for npm:b" || !l.ChangedAt.Equal(first) {
		t.Fatalf("after the failure: %+v", l)
	}
	if pi.most != 1 {
		t.Errorf("%d pi commands ran at once", pi.most)
	}

	// Retrying runs it again; dismissing forgets it.
	j.Retry(l.Jobs[0].ID)
	pi.next(t)
	pi.answers <- errors.New("again")
	l = waitJobs(t, j, func(l PiJobs) bool { return len(l.Jobs) == 1 && l.Jobs[0].State == JobFailed })
	// Asking for a failed job again retries it too.
	j.Queue(PiRemove, "npm:b", "b")
	pi.next(t)
	pi.answers <- errors.New("still")
	l = waitJobs(t, j, func(l PiJobs) bool { return len(l.Jobs) == 1 && l.Jobs[0].State == JobFailed })
	j.Dismiss(l.Jobs[0].ID)
	if l := j.List(); len(l.Jobs) != 0 {
		t.Fatalf("after dismissing: %+v", l)
	}
	emu.Lock()
	defer emu.Unlock()
	if len(events) < 6 {
		t.Errorf("only %d events", len(events))
	}
}

func TestPiJobsCheckSources(t *testing.T) {
	j := newPiJobs(func(context.Context, ...string) (string, error) { return "", nil }, nil)
	for _, c := range []struct {
		op     PiOp
		source string
	}{{PiInstall, ""}, {PiInstall, "--help"}, {PiRemove, "pi-web-access"}, {PiOp("exec"), "npm:a"}} {
		if _, err := j.Queue(c.op, c.source, ""); err == nil {
			t.Errorf("Queue(%s, %q) took it", c.op, c.source)
		}
	}
	// Updating all names no source.
	if _, err := j.Queue(PiUpdate, "", "all packages"); err != nil {
		t.Error(err)
	}
}

func TestPiArgs(t *testing.T) {
	for _, c := range []struct {
		op     PiOp
		source string
		want   string
	}{
		{PiInstall, "npm:a", "install npm:a --no-approve"},
		{PiRemove, "git:github.com/a/b", "remove git:github.com/a/b --no-approve"},
		{PiUpdate, "npm:a", "update --extension npm:a --no-approve"},
		{PiUpdate, "", "update --extensions --no-approve"},
	} {
		if got := strings.Join(piArgs(c.op, c.source), " "); got != c.want {
			t.Errorf("piArgs(%s, %q) = %q, want %q", c.op, c.source, got, c.want)
		}
	}
}

func TestInstallTakesBareNames(t *testing.T) {
	pi := newFakePi()
	a := &app{piJobs: newPiJobs(pi.run, nil)}
	s := &service{app: a}
	for in, want := range map[string]string{"pi-web-access": "npm:pi-web-access", "@scope/tools": "npm:@scope/tools", "npm:x@1.0.0": "npm:x@1.0.0", "./here": "./here"} {
		l, err := s.InstallPiPackage(in)
		if err != nil {
			t.Fatalf("InstallPiPackage(%q): %v", in, err)
		}
		if !slices.ContainsFunc(l.Jobs, func(j PiJob) bool { return j.Source == want }) {
			t.Errorf("InstallPiPackage(%q) queued %+v, want %s", in, l.Jobs, want)
		}
	}
	if _, err := s.InstallPiPackage("Not A Name"); err == nil {
		t.Error("took a bad name")
	}
	go func() {
		for range pi.calls {
			pi.answers <- nil
		}
	}()
	waitJobs(t, a.piJobs, func(l PiJobs) bool { return len(l.Jobs) == 0 })
	close(pi.calls)
}

func TestFailureText(t *testing.T) {
	npm := `Installing npm:nope...
npm error code E404
npm error 404 Not Found - GET https://registry.npmjs.org/nope - Not found
npm error 404
npm error 404  The requested resource 'nope@*' could not be found.
npm error A complete log of this run can be found in: /tmp/x.log
Error: npm install nope --prefix /x failed with code 1`
	if got := failureText(npm); got != "404 Not Found - GET https://registry.npmjs.org/nope - Not found\n404  The requested resource 'nope@*' could not be found." {
		t.Errorf("npm failure = %q", got)
	}
	if got := failureText("Removing npm:x...\nnpm warn something\nNo matching package found for npm:x\n"); got != "No matching package found for npm:x" {
		t.Errorf("remove failure = %q", got)
	}
	if got := failureText("Updating...\nError: No matching package found for npm:x\n"); got != "No matching package found for npm:x" {
		t.Errorf("update failure = %q", got)
	}
}

func TestStaleAgents(t *testing.T) {
	changed := time.Now()
	before, after := changed.Add(-time.Minute), changed.Add(time.Minute)
	list := []agent.Info{
		{ID: "a/lead", Status: agent.Idle, StartedAt: before},
		{ID: "b/lead", Status: agent.Working, StartedAt: before},
		{ID: "c/lead", Status: agent.Idle, StartedAt: after},
		{ID: "d/lead", Status: agent.Stopped, StartedAt: before},
		{ID: "e/lead", Status: agent.Starting},
	}
	var ids []string
	for _, i := range staleAgents(list, changed) {
		ids = append(ids, i.ID)
	}
	if !slices.Equal(ids, []string{"a/lead", "b/lead"}) {
		t.Errorf("stale = %v", ids)
	}
	if got := staleAgents(list, time.Time{}); len(got) != 0 {
		t.Errorf("stale with no change = %v", got)
	}
}
