package server

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/schedule"
)

// A lead's draft follows the board's change lifetime, with the real
// Manager, Handler, Store and scheduler: one admitted before its lead's
// Stop is saved before Stop returns; one not yet admitted then, or sent
// with the credential of a process that ended, saves nothing.

const draftBody = `{"title":"Morning check","instructions":"Check overnight CI.","kind":"daily","time":"09:00","timezone":"UTC"}`

func newScheduleFixture(t *testing.T) (*boardFixture, *schedule.Service) {
	t.Helper()
	f := newBoardFixture(t)
	svc := schedule.New(schedule.Config{Yesmen: f.store, Agents: f.m})
	t.Cleanup(svc.Close)
	f.srv.Schedules = svc
	f.srv.beforeScheduleChange = f.hold.wait
	return f, svc
}

func (f *boardFixture) draft(token string, body io.Reader) *httptest.ResponseRecorder {
	res := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/schedules/draft", body)
	req.Header.Set("Authorization", "Bearer "+token)
	f.srv.Handler().ServeHTTP(res, req)
	return res
}

func plans(t *testing.T, svc *schedule.Service, name string) int {
	t.Helper()
	v, err := svc.View(name)
	if err != nil {
		t.Fatal(err)
	}
	return len(v.Plans)
}

func TestDraftAdmittedBeforeStopIsSavedBeforeItReturns(t *testing.T) {
	f, svc := newScheduleFixture(t)
	f.hire("alice")
	f.hire("bob")
	lead := f.start("alice/lead")
	other := f.start("bob/lead")

	f.hold.arm()
	entered, release := f.hold.entered, f.hold.release
	var res *httptest.ResponseRecorder
	done := make(chan struct{})
	go func() { res = f.draft(lead.Token, strings.NewReader(draftBody)); close(done) }()
	select {
	case <-entered:
	case <-done:
		t.Fatalf("not admitted: %d %s", res.Code, res.Body)
	case <-time.After(10 * time.Second):
		t.Fatal("the draft was never admitted")
	}
	stopped := make(chan int, 1)
	go func() {
		f.m.Stop("alice/lead")
		v, _ := svc.View("alice") // as Stop returns
		stopped <- len(v.Plans)
	}()
	waitFor(t, "Stop to close the process", func() bool { return f.closed("alice/lead", lead.PID) })
	waitRetired(t, f, lead.Token)
	// The ended process asks for nothing new; another workspace is not held
	// up meanwhile.
	if r := f.draft(lead.Token, strings.NewReader(draftBody)); r.Code != 401 {
		t.Fatalf("a new draft from the ended process: %d %s", r.Code, r.Body)
	}
	if r := f.draft(other.Token, strings.NewReader(draftBody)); r.Code != 200 || plans(t, svc, "bob") != 1 {
		t.Fatalf("other workspace: %d %s", r.Code, r.Body)
	}
	select {
	case <-stopped:
		t.Fatal("Stop returned before the draft admitted before it was saved")
	default:
	}
	close(release)
	<-done
	if res.Code != 200 {
		t.Fatalf("admitted draft: %d %s", res.Code, res.Body)
	}
	select {
	case n := <-stopped:
		if n != 1 {
			t.Fatalf("%d plans when Stop returned, want the admitted draft", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return")
	}
	if r := f.draft(lead.Token, strings.NewReader(draftBody)); r.Code != 401 || plans(t, svc, "alice") != 1 {
		t.Fatalf("after Stop: %d %s", r.Code, r.Body)
	}
}

// A draft authenticated before its lead stopped, and not admitted yet,
// saves nothing once Stop returned, even after the lead runs again. (The
// code before R5-F3 refused it too, with 400; it is now refused as the
// board refuses it, with 401.)
func TestDraftNotAdmittedBeforeStopSavesNothing(t *testing.T) {
	for _, restart := range []bool{false, true} {
		f, svc := newScheduleFixture(t)
		f.hire("alice")
		lead := f.start("alice/lead")
		gate := &gateBody{Reader: strings.NewReader(draftBody), entered: make(chan struct{}), release: make(chan struct{})}
		var res *httptest.ResponseRecorder
		done := make(chan struct{})
		go func() { res = f.draft(lead.Token, gate); close(done) }()
		select {
		case <-gate.entered:
		case <-done:
			t.Fatalf("refused before its body was read: %d %s", res.Code, res.Body)
		case <-time.After(10 * time.Second):
			t.Fatal("the body was never read")
		}
		f.m.Stop("alice/lead")
		if restart {
			if next := f.start("alice/lead"); next.Token == lead.Token {
				t.Fatal("the restarted lead has the old credential")
			}
		}
		close(gate.release)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the held draft did not end")
		}
		if res.Code == 200 || plans(t, svc, "alice") != 0 {
			t.Fatalf("restart %v: a draft admitted after Stop returned: %d %s", restart, res.Code, res.Body)
		}
	}
}
