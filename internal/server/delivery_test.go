package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	reject(lead.Token, `{"update":[{"id":"t1","status":"done"}]}`) // ordinary Board staging uses this path
	if err := os.Remove(y.BoardFile() + ".tmp"); err != nil {
		t.Fatal(err)
	}

	// Force the actual delivery rename to fail once after it is staged.
	f.hold.arm()
	release := sync.OnceFunc(func() { close(f.hold.release) })
	defer release()
	failed := make(chan *httptest.ResponseRecorder, 1)
	beforeFailure := f.boardBytes("alice")
	go func() { failed <- f.post(lead.Token, deliver) }()
	select {
	case <-f.hold.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("did not reach the staged rename")
	}
	staged := pendingDeliveryFiles(t, y)
	if len(staged) != 1 {
		t.Fatalf("expected one staged delivery, got %v", staged)
	}
	if err := os.Remove(staged[0]); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case response := <-failed:
		if response.Code != 400 || !bytes.Equal(beforeFailure, f.boardBytes("alice")) {
			t.Fatalf("the failed rename changed delivery state: %d %s", response.Code, response.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("failed rename did not return")
	}
	checkSilent()
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

func pendingDeliveryFiles(t *testing.T, y yesmen.Yesman) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(y.MetaDir(), ".board.json-delivery-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestDeliveryConfirmationStagedWhenUserStops(t *testing.T) {
	f := newBoardFixture(t)
	y := f.hire("alice")
	f.seed("alice")
	lead := f.start("alice/lead")
	var confirmed atomic.Int32
	f.srv.DeliveryConfirmed = func() { confirmed.Add(1) }
	const delivery = `{"update":[{"id":"t1","status":"done"}],"delivery":{"task":"t1"}}`
	before := f.boardBytes("alice")
	f.hold.arm()
	var release sync.Once
	defer release.Do(func() { close(f.hold.release) })
	completed := make(chan *httptest.ResponseRecorder, 1)
	go func() { completed <- f.post(lead.Token, delivery) }()
	select {
	case <-f.hold.entered:
	case res := <-completed:
		t.Fatalf("did not stage the delivery: %d %s", res.Code, res.Body)
	case <-time.After(3 * time.Second):
		t.Fatal("never reached delivery commit")
	}
	if len(pendingDeliveryFiles(t, y)) != 1 {
		t.Fatal("board bytes were not staged")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := f.m.AbortByUser(ctx, "alice/lead"); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(f.hold.release) })
	select {
	case res := <-completed:
		if res.Code != 401 || confirmed.Load() != 0 || !bytes.Equal(f.boardBytes("alice"), before) {
			t.Fatalf("old staged confirmation: %d %s, callbacks %d", res.Code, res.Body, confirmed.Load())
		}
	case <-ctx.Done():
		t.Fatal("staged confirmation did not return")
	}
	if len(pendingDeliveryFiles(t, y)) != 0 {
		t.Fatal("the staged confirmation left its temp file")
	}
	// An ordinary update by the still-running process can land as before.
	if res := f.post(lead.Token, `{"update":[{"id":"t1","note":"control"}]}`); res.Code != 200 {
		t.Fatalf("ordinary Board write: %d %s", res.Code, res.Body)
	}
	if err := f.m.Prompt(ctx, "alice/lead", "Resume work"); err != nil {
		t.Fatal(err)
	}
	if res := f.post(lead.Token, delivery); res.Code != 200 || confirmed.Load() != 1 {
		t.Fatalf("fresh confirmation of the same task: %d %s, callbacks %d", res.Code, res.Body, confirmed.Load())
	}
}

func TestDeliveryConfirmationRequiresReadableHumanWait(t *testing.T) {
	f := newBoardFixture(t)
	y := f.hire("alice")
	f.seed("alice")
	lead := f.start("alice/lead")
	var confirmed atomic.Int32
	f.srv.DeliveryConfirmed = func() { confirmed.Add(1) }
	post := func(body string, code int) string {
		t.Helper()
		res := f.post(lead.Token, body)
		if res.Code != code {
			t.Fatalf("want %d, got %d: %s", code, res.Code, res.Body)
		}
		return res.Body.String()
	}
	const delivery = `{"update":[{"id":"t1","status":"done"}],"delivery":{"task":"t1"}}`
	h, _, err := f.store.AddHandoff("alice", yesmen.Handoff{Type: yesmen.HandoffManual, Title: "Verify the delivery"})
	if err != nil {
		t.Fatal(err)
	}
	post(fmt.Sprintf(`{"update":[{"id":"t1","wait":{"kind":"user","handoff":%q}}]}`, h.ID), 200)
	before := f.boardBytes("alice")
	if msg := post(delivery, 400); !strings.Contains(msg, "still waits") {
		t.Fatal(msg)
	}
	if _, err := f.store.CloseHandoff("alice", h.ID, yesmen.HandoffDone, "user", "verified"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(y.HandoffsFile())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(y.HandoffsFile(), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	post(delivery, 400)
	if err := os.Remove(y.HandoffsFile()); err != nil {
		t.Fatal(err)
	}
	if msg := post(delivery, 400); !strings.Contains(msg, "cannot be verified") {
		t.Fatal(msg)
	}
	if !bytes.Equal(before, f.boardBytes("alice")) || confirmed.Load() != 0 {
		t.Fatal("a refused confirmation changed state")
	}
	if err := os.WriteFile(y.HandoffsFile(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	post(delivery, 200)
	if confirmed.Load() != 1 {
		t.Fatal("closed human wait did not allow delivery")
	}
}
