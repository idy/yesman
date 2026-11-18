package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// boardPayloadFixture is the board as the app's real API answers it, which
// scripts/board-tool.test.mjs gives the registered yesman_board tool: so
// the tool is tested with what the app sends, not with a hand-made answer.
// YESMAN_UPDATE_FIXTURES=1 writes it again.
var boardPayloadFixture = filepath.Join("..", "..", "scripts", "fixtures", "board-api-waits.json")

var rfc3339 = regexp.MustCompile(`"\d{4}-\d\d-\d\dT[0-9:.]+(Z|[+-]\d\d:\d\d)"`)

// The board agents read and change, through the app's real API and an
// agent's own credential, says what each blocked task waits for: GET and
// POST alike, lead and worker alike, beside a scheduled run's plan; and
// still nothing of the run handle, done or owner bookkeeping.
func TestBoardResponsesCarryWaits(t *testing.T) {
	f := newBoardFixture(t)
	f.hire("alice")
	const run = "s1-m-0123456789abcdef"
	if _, _, err := f.store.AddScheduledTask("alice", yesmen.TaskSchedule{Plan: "s1", Run: run}, "Morning check", "detail"); err != nil {
		t.Fatal(err)
	}
	lead := f.start(agent.ID("alice", yesmen.LeadName))
	if _, err := f.m.StartWorker(context.Background(), "alice", "w1", "Work", "a task"); err != nil {
		t.Fatal(err)
	}
	worker := f.start(agent.ID("alice", "w1"))

	res := f.post(lead.Token, `{"add":[
		{"title":"Build","status":"doing","owner":"w1"},
		{"title":"Sign off","wait":{"kind":"user","handoff":"N3","reason":"the user signs the release"}},
		{"title":"History fix","wait":{"kind":"paused","reason":"the user asked to stop"}},
		{"title":"Publish","wait":{"kind":"external","reason":"CI on the release tag"}},
		{"title":"Old","status":"done"}],
	"update":[{"id":"t1","wait":{"kind":"tasks","tasks":["t2"],"reason":"after the build"}}]}`)
	if res.Code != 200 {
		t.Fatalf("lead change: %d %s", res.Code, res.Body)
	}
	check := func(who, how string, body []byte) {
		t.Helper()
		var b struct {
			Tasks []struct {
				ID       string          `json:"id"`
				Status   string          `json:"status"`
				Schedule json.RawMessage `json:"schedule"`
				Wait     *struct {
					Kind    string   `json:"kind"`
					Tasks   []string `json:"tasks"`
					Handoff string   `json:"handoff"`
					Reason  string   `json:"reason"`
					Since   string   `json:"since"`
				} `json:"wait"`
			} `json:"tasks"`
		}
		if err := json.Unmarshal(body, &b); err != nil || len(b.Tasks) != 6 {
			t.Fatalf("%s %s: %v %s", who, how, err, body)
		}
		want := map[string]string{"t1": "tasks t2 after the build", "t3": "user n3 the user signs the release", "t4": "paused the user asked to stop", "t5": "external CI on the release tag"}
		for _, task := range b.Tasks {
			w, ok := want[task.ID]
			if !ok {
				if task.Wait != nil {
					t.Errorf("%s %s: %s waits: %+v", who, how, task.ID, task.Wait)
				}
				continue
			}
			if task.Status != yesmen.TaskBlocked || task.Wait == nil || task.Wait.Since == "" {
				t.Errorf("%s %s: %s has no wait: %s", who, how, task.ID, body)
				continue
			}
			got := strings.Join(append(append([]string{task.Wait.Kind}, task.Wait.Tasks...), task.Wait.Handoff, task.Wait.Reason), " ")
			if strings.Join(strings.Fields(got), " ") != w {
				t.Errorf("%s %s: %s waits for %q, want %q", who, how, task.ID, got, w)
			}
		}
		if string(b.Tasks[0].Schedule) != `{"plan":"s1"}` {
			t.Errorf("%s %s: the plan of t1: %s", who, how, b.Tasks[0].Schedule)
		}
		for _, private := range []string{run, "ownerSince", "doneAt", "doneOwner", "submission"} {
			if bytes.Contains(body, []byte(private)) {
				t.Errorf("%s %s: %q reached the agent: %s", who, how, private, body)
			}
		}
	}
	check("lead", "POST", res.Body.Bytes())
	get := func(token string) []byte {
		req := httptest.NewRequest("GET", "/v1/board", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		f.srv.Handler().ServeHTTP(res, req)
		if res.Code != 200 {
			t.Fatalf("GET: %d %s", res.Code, res.Body)
		}
		return res.Body.Bytes()
	}
	read := get(lead.Token)
	check("lead", "GET", read)
	check("worker", "GET", get(worker.Token))
	res = f.post(worker.Token, `{"update":[{"id":"t2","note":"halfway"}]}`)
	if res.Code != 200 {
		t.Fatalf("worker change: %d %s", res.Code, res.Body)
	}
	check("worker", "POST", res.Body.Bytes())

	// The fixture the extension's test reads: this answer, its times fixed.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, rfc3339.ReplaceAll(bytes.TrimSpace(read), []byte(`"2026-11-18T09:00:00Z"`)), "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	if os.Getenv("YESMAN_UPDATE_FIXTURES") == "1" {
		if err := os.WriteFile(boardPayloadFixture, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := os.ReadFile(boardPayloadFixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, pretty.Bytes()) {
		t.Fatalf("%s is not what the API answers now; YESMAN_UPDATE_FIXTURES=1 writes it again:\n%s", boardPayloadFixture, pretty.Bytes())
	}
}
