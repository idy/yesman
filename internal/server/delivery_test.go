package server

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/chime"
	"github.com/idy/yesman/internal/yesmen"
)

func TestDeliveryConfirmedPlaysOnceAfterSuccessfulBoardCommit(t *testing.T) {
	f := newBoardFixture(t)
	y := f.hire("alice")
	f.seed("alice") // t1, the final delivery task
	lead := f.start("alice/lead")
	if _, err := f.m.StartWorker(context.Background(), "alice", "helper", "Build", "Build the feature"); err != nil {
		t.Fatal(err)
	}
	worker := f.start("alice/helper")
	plays := make(chan []byte, 8)
	p := chime.NewPlayer(func(audio []byte) error { plays <- audio; return nil })
	defer p.Close()
	p.Configure(true, chime.Double)
	var confirmed atomic.Int32
	f.srv.DeliveryConfirmed = func() {
		confirmed.Add(1)
		p.Notify()
	}
	post := func(token, body string, code int) {
		t.Helper()
		if res := f.post(token, body); res.Code != code {
			t.Fatalf("want %d, got %d: %s", code, res.Code, res.Body)
		}
	}
	post(lead.Token, `{"add":[{"title":"Build"},{"title":"Check the result","status":"doing"},{"title":"Unrelated parked work","wait":{"kind":"paused","reason":"user paused"}},{"title":"An unrelated external wait","status":"blocked"},{"title":"Historical done","status":"done"},{"title":"Cancelled work","status":"dropped"}],"update":[{"id":"t1","wait":{"kind":"tasks","tasks":["t2","t3"]}}]}`, 200)
	const deliver = `{"update":[{"id":"t1","status":"done"}],"delivery":{"task":"t1","related":["t2","t3"]}}`
	checkSilent := func() {
		t.Helper()
		if n := confirmed.Load(); n != 0 {
			t.Fatalf("unexpected delivery callback before success: %d", n)
		}
	}
	reject := func(token, body string) {
		t.Helper()
		before := f.boardBytes("alice")
		post(token, body, 400)
		if !bytes.Equal(before, f.boardBytes("alice")) {
			t.Fatalf("refused confirmation changed board: %s", body)
		}
		checkSilent()
	}
	reject(worker.Token, deliver)
	post(worker.Token, `{"update":[{"id":"t2","status":"done"}]}`, 200)
	post(lead.Token, `{"update":[{"id":"t3","note":"still checking"}]}`, 200)
	reject(lead.Token, deliver)
	reject(lead.Token, `{"update":[{"id":"t1","status":"done"}],"delivery":{"task":"t1"}}`) // stored wait still applies
	reject(lead.Token, `{"delivery":{"task":"t6"}}`)
	reject(lead.Token, `{"update":[{"id":"t4","status":"done"}],"delivery":{"task":"t4"}}`)
	reject(lead.Token, `{"update":[{"id":"t7","status":"done"}],"delivery":{"task":"t7"}}`)
	post(lead.Token, `{"update":[{"id":"t3","status":"done"}]}`, 200)
	if err := f.m.StopYesman("alice"); err != nil {
		t.Fatal(err)
	}
	lead = f.start("alice/lead")
	get := httptest.NewRequest("GET", "/v1/board", nil)
	get.Header.Set("Authorization", "Bearer "+lead.Token)
	res := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(res, get)
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	checkSilent()

	if err := os.Mkdir(y.BoardFile()+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	reject(lead.Token, deliver)
	if err := os.Remove(y.BoardFile() + ".tmp"); err != nil {
		t.Fatal(err)
	}
	// Two identical in-flight calls can race after the same lead read.
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() { codes <- f.post(lead.Token, deliver).Code })
	}
	wg.Wait()
	if a, b := <-codes, <-codes; min(a, b) != 200 || max(a, b) != 400 {
		t.Fatalf("expected one commit, got %d and %d", a, b)
	}
	select {
	case audio := <-plays:
		if string(audio[:4]) != "RIFF" {
			t.Fatal("not a synthesized clip")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("commit did not play")
	}
	// A new Store over the same data and a fresh lead see the receipt. A
	// reopen stays editable but doesn't let an old delivery ring again.
	if err := f.m.StopYesman("alice"); err != nil {
		t.Fatal(err)
	}
	f.srv.Yesmen = yesmen.New(filepath.Dir(y.Path), filepath.Join(t.TempDir(), "archive"))
	lead = f.start("alice/lead")
	post(lead.Token, `{"update":[{"id":"t1","status":"doing"}]}`, 200)
	post(lead.Token, deliver, 400)
	post(lead.Token, `{"update":[{"id":"t1","status":"done"}]}`, 200)
	if confirmed.Load() != 1 {
		t.Fatal("duplicate confirmation")
	}

	f.hire("bob")
	f.seed("bob")
	bob := f.start("bob/lead")
	post(bob.Token, `{"update":[{"id":"t1","status":"done"}],"delivery":{"task":"t1"}}`, 200)
	select {
	case <-plays:
	case <-time.After(2 * time.Second):
		t.Fatal("different workspace with same task ID did not play")
	}
	if confirmed.Load() != 2 {
		t.Fatal("wrong delivery count")
	}
}

func TestDeliveryConfirmationHeldAcrossStopIsSilent(t *testing.T) {
	f := newBoardFixture(t)
	f.hire("alice")
	f.seed("alice")
	var confirmed atomic.Int32
	f.srv.DeliveryConfirmed = func() { confirmed.Add(1) }
	old := f.start("alice/lead")
	const delivery = `{"update":[{"id":"t1","status":"done"}],"delivery":{"task":"t1"}}`
	before := f.boardBytes("alice")
	if res := f.held(old.Token, delivery, func() {
		if err := f.m.StopYesman("alice"); err != nil {
			t.Fatal(err)
		}
	}); res.Code != 401 || confirmed.Load() != 0 || !bytes.Equal(f.boardBytes("alice"), before) {
		t.Fatalf("stale request: %d, callbacks %d", res.Code, confirmed.Load())
	}
	newLead := f.start("alice/lead")
	if res := f.post(newLead.Token, delivery); res.Code != 200 || confirmed.Load() != 1 {
		t.Fatalf("fresh delivery: %d %s, callbacks %d", res.Code, res.Body, confirmed.Load())
	}
}
