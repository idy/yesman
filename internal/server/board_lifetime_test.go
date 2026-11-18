package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/crew"
	"github.com/idy/yesman/internal/yesmen"
)

// With YESMAN_TEST_RPC_CHILD set, the test binary is the pi a real Manager
// starts: it records the credential the Manager gave it and answers RPC
// commands. It makes no model request and reads no pi configuration. A
// prompt "exit" makes it exit after answering, as a crash would; when its
// input closes, as Stop closes it, it marks that before exiting.
func TestMain(m *testing.M) {
	if os.Getenv("YESMAN_SERVER_FAKE_PI") != "" {
		serverFakePi() // the scripted pi of fakepi_test.go
		os.Exit(0)
	}
	if os.Getenv("YESMAN_TEMP_CHILD") != "" {
		nestedTempChild() // a program an agent's command runs (files_test.go)
		os.Exit(0)
	}
	if dir := os.Getenv("YESMAN_TEST_RPC_CHILD"); dir != "" {
		rpcChild(dir)
		return
	}
	os.Exit(m.Run())
}

type childTrace struct {
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func rpcChild(dir string) {
	trace, _ := json.Marshal(childTrace{Token: os.Getenv("YESMAN_TOKEN"), PID: os.Getpid()})
	name := strings.ReplaceAll(os.Getenv("YESMAN_AGENT"), "/", "_")
	if os.WriteFile(filepath.Join(dir, name+".json"), trace, 0o600) != nil {
		os.Exit(3)
	}
	if os.Getenv("YESMAN_TEST_TEMP_PROBE") != "" {
		probeTemp(dir, name)
	}
	failInit(dir) // files_contract_test.go
	in := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var cmd struct {
			ID, Type, Message string
		}
		if json.Unmarshal(in.Bytes(), &cmd) != nil {
			continue
		}
		data := map[string]any{}
		if cmd.Type == "get_state" {
			data = map[string]any{"sessionId": fmt.Sprint("rpc-child-", os.Getpid()), "model": map[string]any{"id": "fixture", "provider": "fixture"}}
		}
		_ = out.Encode(map[string]any{"type": "response", "id": cmd.ID, "command": cmd.Type, "success": true, "data": data})
		if cmd.Type == "prompt" && cmd.Message == "exit" {
			os.Exit(3)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, fmt.Sprint(name, "-", os.Getpid(), ".closed")), nil, 0o600)
	os.Exit(0)
}

// closed reports whether Stop has closed the input of an agent's process.
func (f *boardFixture) closed(id string, pid int) bool {
	_, err := os.Stat(filepath.Join(f.traces, fmt.Sprint(strings.ReplaceAll(id, "/", "_"), "-", pid, ".closed")))
	return err == nil
}

// boardFixture is the real Manager, Handler and Store in a temporary root.
type boardFixture struct {
	t      *testing.T
	traces string
	store  *yesmen.Store
	m      *agent.Manager
	srv    *Server
	hold   commitGate
}

// commitGate holds the next admitted board change just before it is written.
type commitGate struct {
	armed            atomic.Bool
	entered, release chan struct{}
}

func (g *commitGate) arm() {
	g.entered, g.release = make(chan struct{}), make(chan struct{})
	g.armed.Store(true)
}

func (g *commitGate) wait() {
	if g.armed.CompareAndSwap(true, false) {
		close(g.entered)
		<-g.release
	}
}

func newBoardFixture(t *testing.T) *boardFixture {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f := &boardFixture{t: t, traces: filepath.Join(dir, "traces")}
	if err := os.Mkdir(f.traces, 0o700); err != nil {
		t.Fatal(err)
	}
	f.store = yesmen.New(filepath.Join(dir, "yesmen"), filepath.Join(dir, "archive"))
	f.m = agent.New(agent.Config{Pi: exe, Extension: filepath.Join(dir, "none.ts"), Yesmen: f.store,
		Env: []string{"YESMAN_TEST_RPC_CHILD=" + f.traces, "GORACE=atexit_sleep_ms=0"}})
	f.srv = &Server{Yesmen: f.store, Agents: f.m, beforeBoardChange: f.hold.wait}
	t.Cleanup(f.m.StopAll)
	return f
}

func (f *boardFixture) hire(name string) yesmen.Yesman {
	f.t.Helper()
	y, err := f.store.Hire(name)
	if err != nil {
		f.t.Fatal(err)
	}
	return y
}

// start starts an agent, or makes sure it runs, and returns the credential
// and process its Manager gave it.
func (f *boardFixture) start(id string) childTrace {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.m.Prompt(ctx, id, "hello"); err != nil {
		f.t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(f.traces, strings.ReplaceAll(id, "/", "_")+".json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var tr childTrace
	if err := json.Unmarshal(raw, &tr); err != nil || tr.Token == "" {
		f.t.Fatalf("no credential: %v %s", err, raw)
	}
	if info, err := f.m.Authenticate(tr.Token); err != nil || info.ID != id {
		f.t.Fatalf("credential of %s: %+v %v", id, info, err)
	}
	return tr
}

func (f *boardFixture) post(token, body string) *httptest.ResponseRecorder {
	return f.serve(token, strings.NewReader(body))
}

func (f *boardFixture) serve(token string, body io.Reader) *httptest.ResponseRecorder {
	res := httptest.NewRecorder()
	f.serveTo(res, token, body)
	return res
}

func (f *boardFixture) serveTo(res *httptest.ResponseRecorder, token string, body io.Reader) {
	req := httptest.NewRequest("POST", "/v1/board", body)
	req.Header.Set("Authorization", "Bearer "+token)
	f.srv.Handler().ServeHTTP(res, req)
}

func (f *boardFixture) boardBytes(name string) []byte {
	f.t.Helper()
	y, err := f.store.Get(name)
	if err != nil {
		f.t.Fatal(err)
	}
	raw, err := os.ReadFile(y.BoardFile())
	if err != nil {
		f.t.Fatal(err)
	}
	return raw
}

func (f *boardFixture) seed(name string) {
	f.t.Helper()
	s := func(v string) *string { return &v }
	if _, err := f.store.ChangeBoard(name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: s("Current task"), Status: s("doing"), Owner: s("current-owner")}}}); err != nil {
		f.t.Fatal(err)
	}
}

// gateBody holds a request's body at its first read, after authentication
// and before the handler has the change.
type gateBody struct {
	io.Reader
	entered, release chan struct{}
	once             sync.Once
}

func (b *gateBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.Reader.Read(p)
}

// held sends a board change whose body is held while between runs.
func (f *boardFixture) held(token, body string, between func()) *httptest.ResponseRecorder {
	f.t.Helper()
	gate := &gateBody{Reader: strings.NewReader(body), entered: make(chan struct{}), release: make(chan struct{})}
	res := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { f.serveTo(res, token, gate); close(done) }()
	defer func() {
		close(gate.release)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			f.t.Fatal("the held request did not end")
		}
	}()
	select {
	case <-gate.entered:
	case <-done:
		f.t.Fatalf("refused before its body was read: %d %s", res.Code, res.Body)
	case <-time.After(10 * time.Second):
		f.t.Fatal("the body was never read")
	}
	between()
	return res // read only after the deferred join
}

const staleChange = `{"add":[{"title":"Stale task"}],"update":[{"id":"t1","status":"done","owner":"retired-owner","note":"stale"}]}`

// t98's counterexamples: a request authenticated before its lead was
// stopped, or its yesman dismissed and hired again, must not change the
// board once that happened, whatever its body says.
func TestBoardChangeAcrossRetirement(t *testing.T) {
	for _, archive := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop-restart", true: "archive-rehire"}[archive], func(t *testing.T) {
			f := newBoardFixture(t)
			oldY := f.hire("alice")
			f.seed("alice")
			old := f.start("alice/lead")
			if res := f.post(old.Token, `{"add":[{"title":"Current control"}]}`); res.Code != 200 {
				t.Fatalf("current caller: %d %s", res.Code, res.Body)
			}
			var before, archived []byte
			var archivePath string
			res := f.held(old.Token, staleChange, func() {
				if archive {
					var err error
					archivePath, err = f.store.Archive("alice", func(y yesmen.Yesman) error { return f.m.StopYesman(y.Name) })
					if err != nil {
						t.Fatal(err)
					}
					f.m.ForgetYesman("alice", oldY.HiredAt)
					if y := f.hire("alice"); y.HiredAt.Equal(oldY.HiredAt) {
						t.Fatal("same-name hire kept the generation")
					}
					f.seed("alice")
					if archived, err = os.ReadFile(filepath.Join(archivePath, ".yesman", "board.json")); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := f.m.StopYesman("alice"); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := f.m.Authenticate(old.Token); err == nil {
					t.Fatal("the old credential outlived its process")
				}
				unchanged := f.boardBytes("alice")
				if r := f.post(old.Token, `{"add":[{"title":"Retired"}]}`); r.Code != 401 || !bytes.Equal(unchanged, f.boardBytes("alice")) {
					t.Fatalf("fresh retired request: %d %s", r.Code, r.Body)
				}
				renewed := f.start("alice/lead")
				if renewed.Token == old.Token || renewed.PID == old.PID {
					t.Fatal("no new process generation")
				}
				if r := f.post(renewed.Token, `{"add":[{"title":"Fresh current control"}]}`); r.Code != 200 {
					t.Fatalf("fresh current request: %d %s", r.Code, r.Body)
				}
				before = f.boardBytes("alice")
			})
			if res.Code != 401 {
				t.Errorf("held request from the retired process: %d %s", res.Code, res.Body)
			}
			after := f.boardBytes("alice")
			if !bytes.Equal(before, after) {
				t.Errorf("board changed by a retired process:\n%s\n->\n%s", before, after)
			}
			var b yesmen.Board
			if err := json.Unmarshal(after, &b); err != nil {
				t.Fatal(err)
			}
			if t1 := b.Tasks[0]; t1.Status != "doing" || t1.Owner != "current-owner" || !t1.DoneAt.IsZero() || t1.DoneOwner != "" {
				t.Errorf("completion attribution changed: %+v", t1)
			}
			if archive {
				if now, err := os.ReadFile(filepath.Join(archivePath, ".yesman", "board.json")); err != nil || !bytes.Equal(now, archived) {
					t.Errorf("archived board changed: %v", err)
				}
			}
		})
	}
}

// A worker removed and started again under the same name is another
// incarnation: the first one's held request is refused.
func TestBoardChangeAcrossWorkerReplacement(t *testing.T) {
	f := newBoardFixture(t)
	f.hire("alice")
	f.seed("alice")
	ctx := context.Background()
	startWorker := func() childTrace {
		// Its task arrives as its first message, which starts its process.
		if _, err := f.m.StartWorker(ctx, "alice", "helper", "Helper", "help"); err != nil {
			t.Fatal(err)
		}
		return f.start("alice/helper")
	}
	old := startWorker()
	var before []byte
	res := f.held(old.Token, staleChange, func() {
		if err := f.m.RemoveWorker("alice", "helper"); err != nil {
			t.Fatal(err)
		}
		renewed := startWorker()
		if renewed.Token == old.Token {
			t.Fatal("same credential")
		}
		if r := f.post(renewed.Token, `{"add":[{"title":"New worker"}]}`); r.Code != 200 {
			t.Fatalf("new worker: %d %s", r.Code, r.Body)
		}
		before = f.boardBytes("alice")
	})
	if res.Code != 401 || !bytes.Equal(before, f.boardBytes("alice")) {
		t.Fatalf("removed worker's request: %d %s", res.Code, res.Body)
	}
}

// A process that exits by itself retires its credential the same way.
func TestBoardChangeAcrossNaturalExit(t *testing.T) {
	f := newBoardFixture(t)
	f.hire("alice")
	f.seed("alice")
	old := f.start("alice/lead")
	var before []byte
	res := f.held(old.Token, staleChange, func() {
		_ = f.m.Prompt(context.Background(), "alice/lead", "exit")
		waitFor(t, "the exit to retire the credential", func() bool { _, err := f.m.Authenticate(old.Token); return err != nil })
		before = f.boardBytes("alice")
	})
	if res.Code != 401 || !bytes.Equal(before, f.boardBytes("alice")) {
		t.Fatalf("exited process's request: %d %s", res.Code, res.Body)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// heldCommit sends a change and holds it once admitted, just before it is
// written; it returns a function that lets it finish and gives its response.
func (f *boardFixture) heldCommit(token, body string) func() *httptest.ResponseRecorder {
	f.t.Helper()
	f.hold.arm()
	entered, release := f.hold.entered, f.hold.release
	var res *httptest.ResponseRecorder
	done := make(chan struct{})
	go func() { res = f.post(token, body); close(done) }()
	select {
	case <-entered:
	case <-done:
		f.t.Fatalf("not admitted: %d %s", res.Code, res.Body)
	case <-time.After(10 * time.Second):
		f.t.Fatal("the change was never admitted")
	}
	var once sync.Once
	return func() *httptest.ResponseRecorder {
		once.Do(func() { close(release) })
		<-done
		return res
	}
}

// Ordering one: a change admitted before Stop is written before Stop
// returns. Meanwhile other callers and workspaces are not held up, and
// after Stop returns nothing more lands.
func TestBoardChangeAdmittedBeforeStopLandsBeforeItReturns(t *testing.T) {
	f := newBoardFixture(t)
	f.hire("alice")
	f.hire("bob")
	f.seed("alice")
	f.seed("bob")
	lead := f.start("alice/lead")
	if _, err := f.m.StartWorker(context.Background(), "alice", "helper", "Helper", "help"); err != nil {
		t.Fatal(err)
	}
	worker := f.start("alice/helper")
	other := f.start("bob/lead")

	finish := f.heldCommit(lead.Token, `{"update":[{"id":"t1","status":"done"}]}`)
	defer finish()
	stopped := make(chan []byte, 1)
	go func() {
		f.m.Stop("alice/lead")
		stopped <- f.boardBytes("alice") // as Stop returns
	}()
	waitFor(t, "Stop to close the process", func() bool { return f.closed("alice/lead", lead.PID) })
	// Once the process has ended, its credential is refused at once, while
	// the change it had admitted is still being written.
	waitRetired(t, f, lead.Token)
	unchanged := f.boardBytes("alice")
	if r := f.post(lead.Token, `{"add":[{"title":"New after the process ended"}]}`); r.Code != 401 || !bytes.Equal(unchanged, f.boardBytes("alice")) {
		t.Fatalf("new request from the ended process: %d %s", r.Code, r.Body)
	}
	// Not held up: the manager, another workspace, the same workspace's
	// other agents, and reads.
	if _, err := f.m.Get("alice/helper"); err != nil {
		t.Fatal(err)
	}
	if r := f.post(other.Token, `{"add":[{"title":"Other workspace"}]}`); r.Code != 200 {
		t.Fatalf("other workspace: %d %s", r.Code, r.Body)
	}
	if r := f.post(worker.Token, `{"add":[{"title":"Same workspace, other agent"}]}`); r.Code != 200 {
		t.Fatalf("other agent: %d %s", r.Code, r.Body)
	}
	if b, err := f.srv.board(context.Background(), caller{yesman: yesmen.Yesman{Name: "alice"}}, nil); err != nil || len(b.(agentBoard).Tasks) != 2 {
		t.Fatalf("read: %v", err)
	}
	select {
	case <-stopped:
		t.Fatal("Stop returned before the change admitted before it was written")
	default:
	}
	if res := finish(); res.Code != 200 {
		t.Fatalf("admitted change: %d %s", res.Code, res.Body)
	}
	var atStop []byte
	select {
	case atStop = <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return")
	}
	var b yesmen.Board
	if err := json.Unmarshal(atStop, &b); err != nil {
		t.Fatal(err)
	}
	if t1 := b.Tasks[0]; t1.Status != "done" || t1.DoneAt.IsZero() || t1.DoneOwner != "current-owner" {
		t.Fatalf("the admitted change had not landed when Stop returned: %+v", t1)
	}
	if r := f.post(lead.Token, `{"add":[{"title":"After Stop"}]}`); r.Code != 401 || !bytes.Equal(atStop, f.boardBytes("alice")) {
		t.Fatalf("after Stop: %d %s", r.Code, r.Body)
	}
}

// waitRetired waits, boundedly, until an ended process's credential no
// longer authenticates.
func waitRetired(t *testing.T, f *boardFixture, token string) {
	t.Helper()
	waitFor(t, "the ended process's credential to stop authenticating", func() bool {
		_, err := f.m.Authenticate(token)
		return err != nil
	})
}

// t101's counterexample: while a change admitted before its lead ended is
// still being written, that lead's credential must not be accepted for
// anything new, such as starting a worker that StopYesman would then miss.
// Whether the process was stopped or exited by itself, a new request is
// refused at once, the admitted change still lands, and no worker runs.
func TestEndedCredentialStartsNothingWhileAdmittedChangeDrains(t *testing.T) {
	for _, mode := range []string{"stop-yesman", "natural-exit"} {
		t.Run(mode, func(t *testing.T) {
			f := newBoardFixture(t)
			f.srv.Crew = func(context.Context) []crew.Member {
				return []crew.Member{{Key: "fixture/fixture", Provider: "fixture", Model: "fixture"}}
			}
			f.hire("alice")
			f.seed("alice")
			lead := f.start("alice/lead")
			finish := f.heldCommit(lead.Token, `{"add":[{"title":"Admitted before the end"}]}`)
			defer finish()
			stopped := make(chan error, 1)
			var atStop []byte
			if mode == "stop-yesman" {
				go func() {
					err := f.m.StopYesman("alice")
					atStop = f.boardBytes("alice") // as StopYesman returns
					stopped <- err
				}()
				waitFor(t, "StopYesman to close the process", func() bool { return f.closed("alice/lead", lead.PID) })
			} else {
				// The fixture's pi exits after answering this prompt.
				_ = f.m.Prompt(context.Background(), "alice/lead", "exit")
			}
			waitRetired(t, f, lead.Token)

			// Synchronously, with the admitted change still held.
			unchanged := f.boardBytes("alice")
			start := f.serveWorkers(lead.Token, `{"name":"after-end","title":"After the end","task":"must not start"}`)
			if start.Code != 401 {
				t.Fatalf("worker start with the ended lead's credential: %d %s", start.Code, start.Body)
			}
			if r := f.post(lead.Token, `{"add":[{"title":"New after the end"}]}`); r.Code != 401 || !bytes.Equal(unchanged, f.boardBytes("alice")) {
				t.Fatalf("board change with the ended lead's credential: %d %s", r.Code, r.Body)
			}
			if ws, err := f.store.Workers("alice"); err != nil || len(ws) != 0 {
				t.Fatalf("workers while draining: %+v %v", ws, err)
			}
			if mode == "stop-yesman" {
				select {
				case err := <-stopped:
					t.Fatalf("StopYesman returned before the admitted change was written: %v", err)
				default:
				}
			}

			if res := finish(); res.Code != 200 {
				t.Fatalf("admitted change: %d %s", res.Code, res.Body)
			}
			if mode == "stop-yesman" {
				select {
				case err := <-stopped:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("StopYesman did not return")
				}
				if !bytes.Contains(atStop, []byte("Admitted before the end")) {
					t.Fatalf("the admitted change had not landed when StopYesman returned: %s", atStop)
				}
			}
			if ws, err := f.store.Workers("alice"); err != nil || len(ws) != 0 {
				t.Fatalf("workers after the end: %+v %v", ws, err)
			}
			if i, err := f.m.Get("alice/after-end"); err == nil {
				t.Fatalf("a worker exists after the end: %+v", i)
			}
			// The lead starts again with a new credential that works.
			renewed := f.start("alice/lead")
			if renewed.Token == lead.Token {
				t.Fatal("same credential after the end")
			}
			if r := f.post(renewed.Token, `{"add":[{"title":"After restart"}]}`); r.Code != 200 {
				t.Fatalf("restarted lead: %d %s", r.Code, r.Body)
			}
			if r := f.post(lead.Token, `{"add":[{"title":"Old again"}]}`); r.Code != 401 {
				t.Fatalf("old credential after restart: %d", r.Code)
			}
		})
	}
}

func (f *boardFixture) serveWorkers(token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/v1/workers", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(res, req)
	return res
}

// Ordering two: Stop before the change is admitted; it is refused.
// Quitting the app (StopAll) waits for an admitted change the same way.
func TestBoardChangeAfterStopAndAcrossShutdown(t *testing.T) {
	f := newBoardFixture(t)
	f.hire("alice")
	f.seed("alice")
	lead := f.start("alice/lead")
	var before []byte
	res := f.held(lead.Token, staleChange, func() {
		f.m.Stop("alice/lead")
		before = f.boardBytes("alice")
	})
	if res.Code != 401 || !bytes.Equal(before, f.boardBytes("alice")) {
		t.Fatalf("change after Stop: %d %s", res.Code, res.Body)
	}

	lead = f.start("alice/lead")
	finish := f.heldCommit(lead.Token, `{"add":[{"title":"Before quitting"}]}`)
	defer finish()
	quit := make(chan struct{})
	go func() { f.m.StopAll(); close(quit) }()
	waitFor(t, "StopAll to close the process", func() bool { return f.closed("alice/lead", lead.PID) })
	select {
	case <-quit:
		t.Fatal("StopAll returned before an admitted change was written")
	default:
	}
	if r := finish(); r.Code != 200 {
		t.Fatalf("admitted change: %d %s", r.Code, r.Body)
	}
	<-quit
	if r := f.post(lead.Token, `{"add":[{"title":"After quitting"}]}`); r.Code != 401 {
		t.Fatalf("after quitting: %d", r.Code)
	}
}

// A current caller keeps every kind of change, and done tasks record when
// and whose they were, as before.
func TestBoardChangeByCurrentCaller(t *testing.T) {
	f := newBoardFixture(t)
	f.hire("alice")
	lead := f.start("alice/lead")
	var changed []string
	f.srv.BoardChanged = func(y string) { changed = append(changed, y) }
	steps := []string{
		`{"add":[{"title":"One","owner":"ann"},{"title":"Two"},{"title":"Three"}]}`,
		`{"update":[{"id":"t1","status":"done"}]}`,
		`{"remove":["t2"]}`,
		`{"order":["t3","t1"]}`,
	}
	var b yesmen.Board
	for _, step := range steps {
		res := f.post(lead.Token, step)
		if res.Code != 200 {
			t.Fatalf("%s: %d %s", step, res.Code, res.Body)
		}
		if err := json.Unmarshal(res.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.Tasks) != 2 || b.Tasks[0].ID != "t3" || b.Tasks[1].ID != "t1" {
		t.Fatalf("board: %+v", b.Tasks)
	}
	// The agents' board leaves out who completed a task and when (see
	// agentBoardOf); the board itself keeps them.
	if t1 := b.Tasks[1]; t1.Status != "done" || !t1.DoneAt.IsZero() || t1.DoneOwner != "" {
		t.Fatalf("completion shown to the agent: %+v", t1)
	}
	if t1 := keptTask(t, f.store, "alice", "t1"); t1.Status != "done" || t1.DoneAt.IsZero() || t1.DoneOwner != "ann" {
		t.Fatalf("completion: %+v", t1)
	}
	if len(changed) != len(steps) {
		t.Fatalf("BoardChanged told %d times", len(changed))
	}
	reopened := f.post(lead.Token, `{"update":[{"id":"t1","status":"doing"}]}`)
	b = yesmen.Board{}
	if err := json.Unmarshal(reopened.Body.Bytes(), &b); err != nil || reopened.Code != 200 {
		t.Fatal(reopened.Code, err)
	}
	if t1 := keptTask(t, f.store, "alice", "t1"); b.Tasks[1].Status != "doing" || t1.Status != "doing" || !t1.DoneAt.IsZero() || t1.DoneOwner != "" {
		t.Fatalf("reopened task kept completion: %+v", t1)
	}
	if res := f.post(lead.Token, `{"update":[{"id":"t9","status":"done"}]}`); res.Code != 400 {
		t.Fatalf("invalid change: %d", res.Code)
	}
	if res := f.post(lead.Token, `{`); res.Code != 400 {
		t.Fatalf("bad body: %d", res.Code)
	}
}

// keptTask is task id as the board of yesman keeps it.
func keptTask(t *testing.T, store *yesmen.Store, yesman, id string) yesmen.Task {
	t.Helper()
	b, err := store.Board(yesman)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range b.Tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("no task %s", id)
	return yesmen.Task{}
}
