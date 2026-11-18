package schedule

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// R6-F2: a read of a workspace's tasks holds that workspace while it finds
// and reads its scheduler state, as every change does. Dismissing it waits
// for a read under way; another workspace is never held up; and once a
// yesman of the same name is hired, its state is the one registered, its
// runs go, and a read for the dismissed one is refused without touching it.
func TestReadsHoldTheirWorkspaceGeneration(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	old, err := e.store.Get(ws)
	if err != nil {
		t.Fatal(err)
	}
	e.save(PlanInput{Schedule: daily("09:00")})
	if _, err := e.store.Hire("bob"); err != nil {
		t.Fatal(err)
	}
	bob, err := e.store.Get("bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Save("bob", PlanInput{Title: "Bob's", Instructions: "x", Schedule: daily("10:00"), Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}

	// The read waits on the scheduling clock once it holds the workspace.
	reached, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	e.svc.cfg.Now = func() time.Time {
		if armed.CompareAndSwap(true, false) {
			close(reached)
			<-release
		}
		return e.clock.Now()
	}
	type result struct {
		v   View
		err error
	}
	read := make(chan result, 1)
	go func() { v, err := e.svc.At(old.HiredAt).View(ws); read <- result{v, err} }()
	<-reached

	dismissed := make(chan struct{})
	archived := make(chan error, 1)
	go func() {
		_, err := e.store.Archive(ws, func(y yesmen.Yesman) error {
			close(dismissed)
			e.svc.RetireYesman(y.Name, y.HiredAt)
			return e.agents.StopYesman(y.Name)
		})
		archived <- err
	}()
	// Another workspace reads and summarizes while alice's read is under way
	// and her dismissal waits for it.
	if v, err := e.svc.At(bob.HiredAt).View("bob"); err != nil || len(v.Plans) != 1 {
		t.Fatalf("bob was held up: %+v %v", v.Plans, err)
	}
	if s, err := e.svc.At(bob.HiredAt).Summarize("bob"); err != nil || s.Total != 1 {
		t.Fatalf("bob's summary: %+v %v", s, err)
	}
	select {
	case <-dismissed:
		t.Fatal("alice was dismissed while a read of her tasks held her")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	r := <-read
	if r.err != nil || len(r.v.Plans) != 1 {
		t.Fatalf("the read under way: %+v %v", r.v.Plans, r.err)
	}
	select {
	case err := <-archived:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the dismissal did not complete once the read ended")
	}
	e.agents.ForgetYesman(ws, old.HiredAt)

	// A yesman hired under the name: its state is the one registered, and
	// its runs go.
	time.Sleep(10 * time.Millisecond)
	if _, err := e.store.Hire(ws); err != nil {
		t.Fatal(err)
	}
	y, err := e.store.Get(ws)
	if err != nil {
		t.Fatal(err)
	}
	g := e.svc.At(y.HiredAt)
	if v, err := g.View(ws); err != nil || len(v.Plans) != 0 {
		t.Fatalf("the new yesman's view: %+v %v", v.Plans, err)
	}
	p, err := g.Save(ws, PlanInput{Title: "New", Instructions: "x", Schedule: daily("09:00"), Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	// A read for the dismissed one is refused, and leaves the new one's
	// state registered as it was.
	if _, err := e.svc.At(old.HiredAt).View(ws); !errors.Is(err, ErrReplaced) {
		t.Fatalf("a read for the dismissed yesman: %v", err)
	}
	if _, err := e.svc.At(old.HiredAt).Summarize(ws); !errors.Is(err, ErrReplaced) {
		t.Fatalf("a summary for the dismissed yesman: %v", err)
	}
	e.svc.mu.Lock()
	sp := e.svc.spaces[ws]
	e.svc.mu.Unlock()
	if sp == nil || !sp.since.Equal(y.HiredAt) || sp.retired.Load() {
		t.Fatalf("the new yesman's state is not the one registered: %+v", sp)
	}
	prompts := len(e.prompts())
	if _, err := g.RunNow(ws, p.ID, e.requestID(), ""); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if n := len(e.prompts()); n != prompts+1 {
		t.Fatalf("the new yesman's run sent %d prompts, want 1", n-prompts)
	}
	if v, err := e.svc.At(bob.HiredAt).View("bob"); err != nil || len(v.Plans) != 1 {
		t.Fatalf("bob afterwards: %+v %v", v.Plans, err)
	}
}

// heldLookup holds the first receipt read once armed, then reads it as
// the agents do.
type heldLookup struct {
	Submitter
	armed            atomic.Bool
	entered, release chan struct{}
}

func (h *heldLookup) LookupDelivery(ctx context.Context, id, messageID string, since time.Time) (agent.Delivery, agent.DeliveryLookup, error) {
	if h.armed.CompareAndSwap(true, false) {
		close(h.entered)
		<-h.release
	}
	return h.Submitter.LookupDelivery(ctx, id, messageID, since)
}

// R6-F2: Check finds its run with the workspace held and lets it go before
// reading the receipt, so a dismissal never waits on pi. What the receipt
// says is written only to the generation current then: here none, so the
// record that went into the archive stays as it was.
func TestCheckLetsTheWorkspaceGoForItsReceipt(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	old, err := e.store.Get(ws)
	if err != nil {
		t.Fatal(err)
	}
	p := e.save(PlanInput{Schedule: daily("09:00")})
	r, err := e.svc.RunNow(ws, p.ID, e.requestID(), "")
	if err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	if got := e.run(r.ID); got.State != StateSent {
		t.Fatalf("before the check: %+v", got)
	}
	// A check of the current generation reads its receipt as before.
	if c, err := e.svc.At(old.HiredAt).Check(ws, r.ID); err != nil || c.State != StateSent {
		t.Fatalf("a check: %+v %v", c, err)
	}

	h := &heldLookup{Submitter: e.agents, entered: make(chan struct{}), release: make(chan struct{})}
	h.armed.Store(true)
	e.svc.cfg.Agents = h
	checked := make(chan error, 1)
	go func() { _, err := e.svc.At(old.HiredAt).Check(ws, r.ID); checked <- err }()
	<-h.entered
	var dest string
	archived := make(chan error, 1)
	go func() {
		var err error
		dest, err = e.store.Archive(ws, func(y yesmen.Yesman) error {
			e.svc.RetireYesman(y.Name, y.HiredAt)
			return e.agents.StopYesman(y.Name)
		})
		archived <- err
	}()
	select {
	case err := <-archived:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		close(h.release)
		t.Fatal("the dismissal waited for a check reading its receipt")
	}
	record := filepath.Join(dest, ".yesman", "schedules", "runs", r.ID+".json")
	before, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	close(h.release)
	select {
	case <-checked:
	case <-time.After(10 * time.Second):
		t.Fatal("the check did not end")
	}
	if after, _ := os.ReadFile(record); !bytes.Equal(before, after) {
		t.Fatal("the check wrote into the archive")
	}
	if _, err := os.Stat(filepath.Join(e.root, "yesmen", ws)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the check wrote a workspace back: %v", err)
	}
}
