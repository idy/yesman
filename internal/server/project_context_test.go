package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/crew"
	"github.com/idy/yesman/internal/schedule"
	"github.com/idy/yesman/internal/yesmen"
)

func projectFixture(t *testing.T) (projectCatalog, yesmen.Yesman) {
	t.Helper()
	root := t.TempDir()
	s := yesmen.New(filepath.Join(root, "yesmen"), filepath.Join(root, "archive"))
	y, err := s.Hire("sample")
	if err != nil {
		t.Fatal(err)
	}
	return projectCatalog{server: &Server{Yesmen: s}, workspace: y}, y
}

func projectWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func projectOne(t *testing.T, c projectCatalog, q projectQuery) projectHit {
	t.Helper()
	r := c.search(context.Background(), q)
	if r.Status != "ok" || len(r.Hits) != 1 {
		t.Fatalf("search %+v: %+v", q, r)
	}
	return r.Hits[0]
}

func TestProjectContextSourceProjection(t *testing.T) {
	c, y := projectFixture(t)
	ctx := context.Background()
	projectWrite(t, filepath.Join(y.KnowledgeDir(), "note.md"), "# Font rules\n\nUse 界面字库.\n")
	projectWrite(t, y.TaskLogPath(), "# Legacy log\n\n## 2026-05-11 Releases\nLegacy validation info\n\n## 2026-05-12 Releases\nOther validation info\n")
	projectWrite(t, filepath.Join(y.MetaDir(), "task-log", "2026-05-13-t1-notes.md"), "# Verification\n\nGood build\n")
	projectWrite(t, y.BoardFile(), `{"next":2,"tasks":[{"id":"t1","title":"Ship release","status":"blocked","detail":"build target","wait":{"kind":"external","reason":"await build signal"},"schedule":{"plan":"s1","run":"PRIVATE-RUN-HANDLE"},"secret":"RAW-UNKNOWN"}]}`)
	topic, err := c.server.Yesmen.CreateTopic(y.Name, y.HiredAt, yesmen.TopicByUser, yesmen.TopicInput{
		Title: "Choice", Summary: "interim explanation", Questions: []string{"still open"}, Conclusions: []string{"confirmed explanation"},
		Sources: []yesmen.TopicSource{{Kind: "link", Ref: "https://example.invalid/PRIVATE-URL"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := schedule.New(schedule.Config{Yesmen: c.server.Yesmen})
	defer svc.Close()
	plan, err := svc.Save(y.Name, schedule.PlanInput{Title: "Weekly fixture", Instructions: "Prepare a build", Timezone: "UTC", Schedule: schedule.Spec{Kind: "daily", Time: "10:00"}})
	if err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(y.MetaDir(), "schedules", "runs")
	if err := os.MkdirAll(runs, 0700); err != nil {
		t.Fatal(err)
	}
	projectWrite(t, filepath.Join(runs, "s1-fixture.json"), `{"id":"s1-fixture","plan":"s1","kind":"manual","at":"2026-05-14T12:00:00Z","state":"sent","title":"Weekly fixture","instructions":"Retained run marker","board":"t1","claimer":"PRIVATE-CLAIMER","attempts":[{"id":"PRIVATE-ATTEMPT"}]}`)
	cases := []struct {
		q               projectQuery
		field, contains string
	}{
		{projectQuery{Query: "界面字库", Source: "knowledge"}, "Font rules", "Use 界面字库"},
		{projectQuery{Query: "validation", Source: "working_logs", From: "2026-05-11", To: "2026-05-11"}, "2026-05-11 Releases", "Legacy validation"},
		{projectQuery{Query: "good BUILD", Source: "working_logs"}, "Verification", "Good build"},
		{projectQuery{Source: "board", Query: "await build"}, "Blocker", "await build signal"},
		{projectQuery{Source: "topics", Query: "confirmed"}, "Confirmed conclusion", "confirmed explanation"},
		{projectQuery{Source: "topics", Query: "interim"}, "Interim summary", "interim explanation"},
		{projectQuery{Source: "tasks", ID: plan.ID}, "Instructions", "Prepare a build"},
		{projectQuery{Source: "tasks", Query: "Retained run"}, "Instructions at run time", "Retained run marker"},
	}
	for _, tt := range cases {
		hit := projectOne(t, c, tt.q)
		read := c.read(ctx, projectRead{Ref: hit.Ref})
		if read.Status != "ok" || !strings.Contains(read.Item.Excerpt, tt.contains) || !strings.HasPrefix(read.Item.Field, tt.field) {
			t.Errorf("%+v returned %+v", tt.q, read.Item)
		}
	}
	for _, word := range []string{"PRIVATE-URL", "PRIVATE-CLAIMER", "PRIVATE-ATTEMPT", "PRIVATE-RUN-HANDLE", "RAW-UNKNOWN"} {
		if r := c.search(ctx, projectQuery{Query: word}); r.Status != "no_match" {
			t.Errorf("private field searched: %+v", r)
		}
	}
	old := projectOne(t, c, projectQuery{Source: "topics", ID: topic.ID})
	resolved := yesmen.TopicResolved
	if _, err := c.server.Yesmen.PatchTopic(y.Name, y.HiredAt, yesmen.TopicByUser, topic.ID, yesmen.TopicPatch{Revision: topic.Revision, Status: &resolved}); err != nil {
		t.Fatal(err)
	}
	if r := c.search(ctx, projectQuery{Source: "topics", Query: topic.ID}); r.Status != "no_match" {
		t.Fatalf("closed topic leaked to default search: %+v", r)
	}
	if r := c.read(ctx, projectRead{Ref: old.Ref}); r.Status != "changed" {
		t.Fatalf("resolved previous ref: %+v", r)
	}
	closed := projectOne(t, c, projectQuery{Source: "topics", ID: topic.ID})
	if closed.Status != resolved || c.read(ctx, projectRead{Ref: closed.Ref}).Status != "ok" {
		t.Fatal("exact ID cannot find a resolved Topic")
	}
	for _, r := range []projectResponse{c.search(ctx, projectQuery{Source: "history"}), c.read(ctx, projectRead{Ref: "history"})} {
		if r.Status != "not_integrated" {
			t.Fatalf("History status: %+v", r)
		}
	}
}

func TestProjectContextStablePagingAndCaps(t *testing.T) {
	c, y := projectFixture(t)
	ctx := context.Background()
	body := strings.Repeat("长<界\\\"\x01", 440) + "精确定位" + strings.Repeat("长<界\\\"\x01", 700)
	for i := 0; i < 11; i++ {
		projectWrite(t, filepath.Join(y.KnowledgeDir(), fmt.Sprintf("note-%02d.md", i)), "# Project note\n\n## Target section\n"+body+"\n## Unrelated\nOther\n")
	}
	q := projectQuery{Source: "knowledge", Query: "精确定位"}
	r := c.search(ctx, q)
	if len(r.Hits) == 0 || len(r.Hits) > 5 || len(contextJSON(r)) > searchPageBytes || r.Cursor == "" {
		t.Fatalf("first page hits=%d cursor=%q bytes=%d", len(r.Hits), r.Cursor, len(contextJSON(r)))
	}
	ref := r.Hits[0].Ref
	excerpt := r.Hits[0].Excerpt
	if n := utf8.RuneCountInString(excerpt); n < 160 || n > 240 || !strings.Contains(excerpt, "精确定位") || !strings.HasPrefix(excerpt, "…") || !strings.HasSuffix(excerpt, "…") {
		t.Fatalf("unexpected excerpt: %q", excerpt)
	}
	for _, large := range []bool{false, true} {
		limit := readPageBytes
		if large {
			limit = maxReadBytes
		}
		rd := c.read(ctx, projectRead{Ref: ref, Large: large})
		if rd.Status != "ok" || rd.Cursor == "" || len(contextJSON(rd)) > limit || !strings.Contains(rd.Item.Excerpt, "精确定位") || strings.Contains(rd.Item.Excerpt, "Unrelated") {
			t.Fatalf("read status=%s continuation=%v bytes=%d", rd.Status, rd.Cursor != "", len(contextJSON(rd)))
		}
		more := c.read(ctx, projectRead{Ref: ref, Cursor: rd.Cursor, Large: large})
		if more.Status != "ok" || len(contextJSON(more)) > limit || strings.Contains(more.Item.Excerpt, "精确定位") {
			t.Fatalf("read cursor did not move forward: %s", more.Status)
		}
		if denied := c.read(ctx, projectRead{Ref: ref, Cursor: rd.Cursor + "A"}); denied.Status != "denied" {
			t.Fatal("invalid cursor was accepted")
		}
	}
	projectWrite(t, filepath.Join(y.KnowledgeDir(), "unrelated.md"), "No match here")
	renamed, err := c.server.Yesmen.Rename(y.Name, "Renamed 工程")
	if err != nil {
		t.Fatal(err)
	}
	c = projectCatalog{server: c.server, workspace: renamed}
	seen := map[string]bool{}
	for {
		for _, h := range r.Hits {
			if seen[h.ID] {
				t.Fatalf("repeat hit %s", h.ID)
			}
			seen[h.ID] = true
		}
		if r.Cursor == "" {
			break
		}
		q.Cursor = r.Cursor
		r = c.search(ctx, q)
		if r.Status != "ok" || len(contextJSON(r)) > searchPageBytes {
			t.Fatalf("stable continuation status=%s", r.Status)
		}
	}
	if len(seen) != 11 || c.read(ctx, projectRead{Ref: ref}).Status != "ok" {
		t.Fatal("unrelated append or workspace display rename invalidated references")
	}
	key := filepath.Join(y.KnowledgeDir(), "note-00.md")
	projectWrite(t, key, "# Edited\n\n精确定位 change")
	if c.read(ctx, projectRead{Ref: ref}).Status != "changed" || c.search(ctx, q).Status != "changed" {
		t.Fatal("edit did not invalidate the old reference and search cursor")
	}
	renameRef := projectOne(t, c, projectQuery{Source: "knowledge", ID: "note-00.md"}).Ref
	if err := os.Rename(key, filepath.Join(y.KnowledgeDir(), "renamed.md")); err != nil {
		t.Fatal(err)
	}
	if c.read(ctx, projectRead{Ref: renameRef}).Status != "deleted" {
		t.Fatal("the old filename should report a missing original")
	}
}

func TestProjectContextBriefBoundsAndBinding(t *testing.T) {
	c, y := projectFixture(t)
	ctx := context.Background()
	w := agent.ContextActor{Info: agent.Info{Role: agent.RoleWorker, Name: "w", Since: y.HiredAt}, Worker: yesmen.Worker{
		Title: strings.Repeat("界", 80), Task: strings.Repeat("旧背景", 7000), Dispatch: yesmen.Dispatch{
			BoardTask: "t2", Goal: "Keep goal", Constraints: "Keep constraint", Deliverables: "Keep deliverable", Sources: []string{"pc1.example"},
		},
	}}
	board := yesmen.Board{Tasks: []yesmen.Task{
		{ID: "t1", Title: "Same worker name, previous generation", Status: "blocked", Owner: "w", OwnerSince: y.HiredAt.Add(-time.Hour)},
		{ID: "t2", Title: "Current worker assignment", Status: "blocked", Owner: "w", OwnerSince: y.HiredAt, Wait: &yesmen.Wait{Kind: "external", Reason: "Current blocker"}},
	}}
	brief := buildProjectBrief(ctx, c, w, board, nil, nil, "")
	for _, want := range []string{"Keep goal", "Keep constraint", "Keep deliverable", "pc1.example", "Linked Board task t2", "Current blocker"} {
		if !strings.Contains(brief, want) {
			t.Errorf("worker brief lacks %q", want)
		}
	}
	if strings.Contains(brief, "Same worker name, previous generation") || strings.Contains(brief, "旧背景") {
		t.Fatal("worker brief included unrelated context")
	}
	for i := 3; i <= 70; i++ {
		board.Tasks = append(board.Tasks, yesmen.Task{ID: fmt.Sprintf("t%d", i), Status: "todo", Title: strings.Repeat("界<\\\"", 100)})
	}
	lead := agent.ContextActor{Info: agent.Info{Role: agent.RoleLead, Name: "lead"}}
	brief = buildProjectBrief(ctx, c, lead, board, nil, &crew.Routing{}, "")
	if strings.Count(brief, "\n- t") != 5 || len(brief) > projectBriefBytes || !utf8.ValidString(brief) {
		t.Fatalf("Lead brief violated its budget (%d bytes)", len(brief))
	}
	w.Worker.Dispatch = yesmen.Dispatch{}
	w.Worker.Task = strings.Repeat("界<\\\"", 12000)
	brief = buildProjectBrief(ctx, c, w, board, nil, nil, "")
	if len(brief) > projectBriefBytes || !utf8.ValidString(brief) || !strings.Contains(brief, "Current blocker") || !strings.Contains(brief, "…") {
		t.Fatalf("legacy dispatch or linked status not bounded (%d bytes)", len(brief))
	}
}

func TestProjectContextPermissionAndCancellation(t *testing.T) {
	f := newBoardFixture(t)
	y, other := f.hire("alpha"), f.hire("beta")
	if err := f.store.AddWorker(y.Name, yesmen.Worker{Name: "doer", Task: "test", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	lead := f.start(agent.ID(y.Name, "lead"))
	worker := f.start(agent.ID(y.Name, "doer"))
	outsider := f.start(agent.ID(other.Name, "lead"))
	c := projectCatalog{server: f.srv, workspace: y}
	projectWrite(t, filepath.Join(y.KnowledgeDir(), "public.md"), "# Visible\nmarker")
	hit := projectOne(t, c, projectQuery{Source: "knowledge", Query: "marker"})
	call := func(ctx context.Context, token, path string, body any) projectResponse {
		r := httptest.NewRequest("POST", "/v1/context/"+path, strings.NewReader(string(contextJSON(body)))).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.srv.Handler().ServeHTTP(w, r)
		var out projectResponse
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	ctx := context.Background()
	for _, tc := range []struct{ token, status string }{{lead.Token, "ok"}, {worker.Token, "ok"}, {outsider.Token, "denied"}, {"invalid", "denied"}} {
		if r := call(ctx, tc.token, "read", projectRead{Ref: hit.Ref}); r.Status != tc.status {
			t.Fatalf("expected %s, got %s", tc.status, r.Status)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if r := call(cancelled, lead.Token, "read", projectRead{Ref: hit.Ref}); r.Item != nil {
		t.Fatal("cancelled context disclosed data")
	}
	private := filepath.Join(other.KnowledgeDir(), "private.md")
	projectWrite(t, private, "OUTSIDE SENTINEL")
	name := filepath.Join(y.KnowledgeDir(), "public.md")
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(private, name); err == nil {
		if r := call(ctx, lead.Token, "read", projectRead{Ref: hit.Ref}); r.Status != "denied" || r.Item != nil {
			t.Fatal("hard-linked replacement was not denied")
		}
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(private, name); err == nil {
		if r := call(ctx, lead.Token, "read", projectRead{Ref: hit.Ref}); r.Status != "denied" || r.Item != nil {
			t.Fatalf("linked replacement was not denied: %+v", r)
		}
		if r := call(ctx, lead.Token, "search", projectQuery{Query: "OUTSIDE", Source: "knowledge"}); r.Status != "partial" || len(r.Hits) > 0 {
			t.Fatalf("link in search: %+v", r)
		}
	}
	projectWrite(t, y.TopicsFile(), "invalid JSON")
	if r := call(ctx, worker.Token, "search", projectQuery{Query: "a", Source: "topics"}); r.Status != "source_unavailable" {
		t.Fatalf("corrupt source: %+v", r)
	}
	for _, ref := range []string{"https://example.invalid/", "../board.json", contextToken("pc1.", projectLocator{Scope: contextScope(y), Source: "knowledge", Key: "../task-log.md"})} {
		if r := call(ctx, worker.Token, "read", projectRead{Ref: ref}); r.Status != "denied" {
			t.Errorf("unsafe reference accepted: %s", r.Status)
		}
	}
	f.m.StopAll()
	if r := call(ctx, worker.Token, "read", projectRead{Ref: hit.Ref}); r.Status != "denied" {
		t.Fatalf("retired worker: %+v", r)
	}
}
