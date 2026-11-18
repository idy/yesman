package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// Through the agents' real API: yesman_board sends wait as an object; only
// the lead pauses a task or lifts a pause; a worker keeps its own waits.
func TestBoardWaitThroughTheAPI(t *testing.T) {
	f := newBoardFixture(t)
	f.hire("alice")
	changed := 0
	f.srv.BoardChanged = func(string) { changed++ }
	lead := f.start(agent.ID("alice", yesmen.LeadName))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.m.StartWorker(ctx, "alice", "w1", "Work", "a task"); err != nil {
		t.Fatal(err)
	}
	worker := f.start(agent.ID("alice", "w1"))

	res := f.post(lead.Token, `{"add":[{"title":"Publish"},{"title":"History fix","owner":"w1","wait":{"kind":"paused","reason":"The user asked to stop"}}]}`)
	if res.Code != 200 {
		t.Fatalf("lead pause: %d %s", res.Code, res.Body)
	}
	for _, body := range []string{
		`{"update":[{"id":"t2","status":"doing"}]}`,
		`{"update":[{"id":"t2","wait":{"kind":""}}]}`,
		`{"update":[{"id":"t1","wait":{"kind":"paused","reason":"me"}}]}`,
	} {
		if res := f.post(worker.Token, body); res.Code == 200 || !strings.Contains(res.Body.String(), "only the lead") {
			t.Errorf("worker %s: %d %s", body, res.Code, res.Body)
		}
	}
	res = f.post(worker.Token, `{"add":[{"title":"Review after publish","owner":"w1","wait":{"kind":"tasks","tasks":["1"],"reason":"needs the release"}}]}`)
	if res.Code != 200 {
		t.Fatalf("worker wait: %d %s", res.Code, res.Body)
	}
	var b yesmen.Board
	if err := json.Unmarshal(f.boardBytes("alice"), &b); err != nil {
		t.Fatal(err)
	}
	if !b.Tasks[1].Paused() || b.Tasks[1].Status != yesmen.TaskBlocked {
		t.Fatalf("the pause changed: %+v", b.Tasks[1])
	}
	if w := b.Tasks[2].Wait; b.Tasks[2].Status != yesmen.TaskBlocked || w == nil || w.Kind != yesmen.WaitTasks || w.Tasks[0] != "t1" {
		t.Fatalf("worker's wait: %+v", b.Tasks[2])
	}
	if changed != 2 {
		t.Fatalf("BoardChanged ran %d times, want 2 (refused changes are not changes)", changed)
	}
	if res := f.post(lead.Token, `{"update":[{"id":"t2","status":"doing"}]}`); res.Code != 200 {
		t.Fatalf("the lead could not lift the pause: %d %s", res.Code, res.Body)
	}
}
