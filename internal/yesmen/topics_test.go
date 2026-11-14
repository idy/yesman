package yesmen

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func topicStore(t *testing.T) (*Store, Yesman) {
	t.Helper()
	tmp := t.TempDir()
	s := New(filepath.Join(tmp, "yesmen"), filepath.Join(tmp, "archive"))
	y, err := s.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	return s, y
}

const sendA = "0000000000aa0123456789abcdef0123456789abcdef0123"
const sendB = "0000000000bb0123456789abcdef0123456789abcdef0123"

func TestTopicsCreatePatchAndStableIDs(t *testing.T) {
	s, y := topicStore(t)
	list, err := s.Topics("alice", y.HiredAt)
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty workspace: %v %v", list, err)
	}
	if _, err := os.Stat(y.TopicsFile()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reading must not create the file: %v", err)
	}
	a, err := s.CreateTopic("alice", y.HiredAt, TopicByUser, TopicInput{Title: "  How do  topics relate to tasks? ", Summary: "Scope", Conclusions: []string{"Tasks stay on the board"}, Questions: []string{"Do we need tags?"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "tp1" || a.Title != "How do topics relate to tasks?" || a.Status != TopicOpen || a.Revision != 1 || a.CreatedBy != TopicByUser {
		t.Fatalf("created %+v", a)
	}
	if a.Conclusions[0].ID != "c1" || a.Questions[0].ID != "q2" {
		t.Fatalf("item IDs %+v %+v", a.Conclusions, a.Questions)
	}
	b, err := s.CreateTopic("alice", y.HiredAt, TopicByLead, TopicInput{Title: "Second"})
	if err != nil || b.ID != "tp2" {
		t.Fatalf("second %+v %v", b, err)
	}

	// The lead adds and removes items; IDs of the others stay.
	a, err = s.PatchTopic("alice", y.HiredAt, TopicByLead, "TP1", TopicPatch{Revision: 1, AddConclusions: []string{"Topics link to tasks"}, RemoveQuestions: []string{"q2"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Revision != 2 || len(a.Conclusions) != 2 || a.Conclusions[0].ID != "c1" || a.Conclusions[1].ID != "c3" || len(a.Questions) != 0 || a.UpdatedBy != TopicByLead {
		t.Fatalf("patched %+v", a)
	}

	// A form replaces a list: known IDs stay, unknown/other-kind IDs are new.
	items := []TopicItem{{ID: "c3", Text: "Topics link to tasks, edited"}, {ID: "q9", Text: "new one"}, {ID: "", Text: "  "}}
	a, err = s.PatchTopic("alice", y.HiredAt, TopicByUser, "tp1", TopicPatch{Revision: 2, Conclusions: &items})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Conclusions) != 2 || a.Conclusions[0] != (TopicItem{ID: "c3", Text: "Topics link to tasks, edited"}) || a.Conclusions[1].ID != "c4" {
		t.Fatalf("replaced %+v", a.Conclusions)
	}

	// Archive and restore keep the status it had.
	resolved := "resolved"
	a, _ = s.PatchTopic("alice", y.HiredAt, TopicByUser, "tp1", TopicPatch{Revision: a.Revision, Status: &resolved})
	a, err = s.PatchTopic("alice", y.HiredAt, TopicByUser, "tp1", TopicPatch{Revision: a.Revision, Archive: true})
	if err != nil || a.Status != TopicArchived || a.ArchivedFrom != TopicResolved {
		t.Fatalf("archived %+v %v", a, err)
	}
	archived := "archived"
	if _, err := s.PatchTopic("alice", y.HiredAt, TopicByUser, "tp1", TopicPatch{Revision: a.Revision, Status: &archived}); err == nil {
		t.Fatal("status archived must go through archive")
	}
	a, err = s.PatchTopic("alice", y.HiredAt, TopicByUser, "tp1", TopicPatch{Revision: a.Revision, Restore: true})
	if err != nil || a.Status != TopicResolved || a.ArchivedFrom != "" {
		t.Fatalf("restored %+v %v", a, err)
	}
	c := s.TopicCounts("alice")
	if c.Open != 1 || c.Resolved != 1 || c.Error != "" {
		t.Fatalf("counts %+v", c)
	}
}

func TestTopicCompareAndSet(t *testing.T) {
	s, y := topicStore(t)
	tp, _ := s.CreateTopic("alice", y.HiredAt, TopicByUser, TopicInput{Title: "CAS"})
	user, lead := "user title", "lead title"
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, title := range []*string{&user, &lead} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			by := TopicByUser
			if i == 1 {
				by = TopicByLead
			}
			_, results[i] = s.PatchTopic("alice", y.HiredAt, by, tp.ID, TopicPatch{Revision: tp.Revision, Title: title})
		}()
	}
	wg.Wait()
	var conflict *TopicConflictError
	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case errors.As(err, &conflict):
			if conflict.Current.Revision != tp.Revision+1 {
				t.Fatalf("conflict reports %+v", conflict.Current)
			}
		default:
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("exactly one write wins: %v", results)
	}
	// A stale lead patch never overwrites the user's newer edit.
	if _, err := s.PatchTopic("alice", y.HiredAt, TopicByLead, tp.ID, TopicPatch{Revision: 1, Summary: ptr("old")}); !errors.As(err, &conflict) {
		t.Fatalf("stale patch: %v", err)
	}
	got, _ := s.Topic("alice", y.HiredAt, tp.ID)
	if got.Summary != "" || got.Revision != 2 {
		t.Fatalf("after conflict %+v", got)
	}
}

func TestTopicsPreserveUnknownAndRefuseBadFiles(t *testing.T) {
	s, y := topicStore(t)
	raw := `{"version":1,"next":4,"future":{"x":1},"topics":[{"id":"tp4","title":"Old","status":"open","revision":3,"nextItem":0,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","color":"blue"}]}`
	if err := os.WriteFile(y.TopicsFile(), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	tp, err := s.PatchTopic("alice", y.HiredAt, TopicByUser, "tp4", TopicPatch{Revision: 3, Summary: ptr("now")})
	if err != nil {
		t.Fatal(err)
	}
	if tp.Conclusions == nil || tp.Sends == nil {
		t.Fatal("old topics get empty lists")
	}
	n, _ := s.CreateTopic("alice", y.HiredAt, TopicByUser, TopicInput{Title: "New"})
	if n.ID != "tp5" {
		t.Fatalf("next ID after tp4 = %s", n.ID)
	}
	data, _ := os.ReadFile(y.TopicsFile())
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back["future"] == nil || !strings.Contains(string(data), `"color": "blue"`) {
		t.Fatalf("unknown fields lost: %s", data)
	}

	for name, content := range map[string]string{
		"corrupt":          `{"topics": [`,
		"newer":            `{"version": 9, "topics": []}`,
		"null":             `null`,
		"empty object":     `{}`,
		"no version":       `{"topics": []}`,
		"no topics":        `{"version": 1}`,
		"repeated ID":      `{"version": 1, "topics": [{"id": "tp1", "title": "a", "status": "open", "revision": 1}, {"id": "tp1", "title": "b", "status": "open", "revision": 1}]}`,
		"bad ID":           `{"version": 1, "topics": [{"id": "../x", "title": "a", "status": "open", "revision": 1}]}`,
		"bad status":       `{"version": 1, "topics": [{"id": "tp1", "title": "a", "status": "gone", "revision": 1}]}`,
		"no revision":      `{"version": 1, "topics": [{"id": "tp1", "title": "a", "status": "open"}]}`,
		"repeated item":    `{"version": 1, "topics": [{"id": "tp1", "title": "a", "status": "open", "revision": 1, "conclusions": [{"id": "c1", "text": "x"}], "questions": [{"id": "c1", "text": "y"}]}]}`,
		"repeated send":    `{"version": 1, "topics": [{"id": "tp1", "title": "a", "status": "open", "revision": 1, "sends": [{"id": "` + sendA + `"}, {"id": "` + sendA + `"}]}]}`,
		"null topic entry": `{"version": 1, "topics": [null]}`,
	} {
		if err := os.WriteFile(y.TopicsFile(), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Topics("alice", y.HiredAt); err == nil {
			t.Fatalf("%s: read must fail", name)
		}
		if _, err := s.CreateTopic("alice", y.HiredAt, TopicByUser, TopicInput{Title: "x"}); err == nil {
			t.Fatalf("%s: write must fail", name)
		}
		if c := s.TopicCounts("alice"); c.Error == "" {
			t.Fatalf("%s: counts must report the error", name)
		}
		if data, _ := os.ReadFile(y.TopicsFile()); string(data) != content {
			t.Fatalf("%s: file was rewritten: %s", name, data)
		}
	}
	// A valid file's item counter catches up with item IDs it already has.
	valid := `{"version": 1, "topics": [{"id": "tp4", "title": "a", "status": "open", "revision": 1, "conclusions": [{"id": "c7", "text": "x"}]}]}`
	if err := os.WriteFile(y.TopicsFile(), []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.PatchTopic("alice", y.HiredAt, TopicByUser, "tp4", TopicPatch{Revision: 1, AddQuestions: []string{"next"}})
	if err != nil || got.Questions[0].ID != "q8" {
		t.Fatalf("item IDs after c7: %+v %v", got.Questions, err)
	}
	if next, err := s.CreateTopic("alice", y.HiredAt, TopicByUser, TopicInput{Title: "after tp4"}); err != nil || next.ID != "tp5" {
		t.Fatalf("topic IDs after tp4: %v %v", next.ID, err)
	}

	big := make([]byte, maxTopicsFileRead+10)
	for i := range big {
		big[i] = ' '
	}
	_ = os.WriteFile(y.TopicsFile(), big, 0o644)
	if _, err := s.Topics("alice", y.HiredAt); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("oversize: %v", err)
	}
	if info, _ := os.Stat(y.TopicsFile()); info.Size() != int64(len(big)) {
		t.Fatal("oversize file was changed")
	}
	entries, _ := os.ReadDir(y.MetaDir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".topics-") {
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}
}

func TestTopicValidationAndLimits(t *testing.T) {
	s, y := topicStore(t)
	for _, in := range []TopicInput{
		{Title: ""},
		{Title: strings.Repeat("x", maxTopicTitle+1)},
		{Title: "ok", Summary: "bad\x00"},
		{Title: "ok", Summary: strings.Repeat("y", maxTopicSummary+1)},
		{Title: "ok", Status: "done"},
		{Title: "ok", Sources: []TopicSource{{Kind: "knowledge", Ref: "../../code.go"}}},
		{Title: "ok", Sources: []TopicSource{{Kind: "knowledge", Ref: "/etc/passwd.md"}}},
		{Title: "ok", Sources: []TopicSource{{Kind: "link", Ref: "javascript:alert(1)"}}},
		{Title: "ok", Sources: []TopicSource{{Kind: "message", Role: "system", Timestamp: 1}}},
		{Title: "ok", Sources: []TopicSource{{Kind: "worker", Agent: "../lead"}}},
		{Title: "ok", Sources: []TopicSource{{Kind: "file", Ref: "x"}}},
	} {
		if _, err := s.CreateTopic("alice", y.HiredAt, TopicByUser, in); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
	if _, err := s.CreateTopic("alice", y.HiredAt, "worker", TopicInput{Title: "x"}); err == nil {
		t.Error("unknown editor accepted")
	}
	tp, err := s.CreateTopic("alice", y.HiredAt, TopicByLead, TopicInput{Title: "Sources", Sources: []TopicSource{
		{Kind: "task", Ref: "#5"}, {Kind: "knowledge", Ref: ".yesman/knowledge/icons.md"}, {Kind: "link", Ref: "https://github.com/idy/yesman/pull/1"},
		{Kind: "conversation"}, {Kind: "message", Role: "user", Timestamp: 1700000000000, Label: "  The   question "}, {Kind: "task", Ref: "t5"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tp.Sources) != 5 || tp.Sources[0].Ref != "t5" || tp.Sources[1].Ref != "icons.md" || tp.Sources[3].Agent != LeadName || tp.Sources[4].Label != "The question" {
		t.Fatalf("sources %+v", tp.Sources)
	}
	for _, bad := range []string{"../x", "tp0", "tp1/../../x", "t3", ""} {
		if _, err := s.Topic("alice", y.HiredAt, bad); err == nil {
			t.Errorf("topic ID %q accepted", bad)
		}
	}
	many := make([]string, maxTopicItems+1)
	for i := range many {
		many[i] = "x"
	}
	if _, err := s.PatchTopic("alice", y.HiredAt, TopicByLead, tp.ID, TopicPatch{Revision: tp.Revision, AddQuestions: many}); err == nil {
		t.Error("too many items accepted")
	}
	if got, _ := s.Topic("alice", y.HiredAt, tp.ID); got.Revision != tp.Revision || len(got.Questions) != 0 {
		t.Errorf("a failed patch changed the topic: %+v", got)
	}
}

func TestTopicSendsAreIdempotentAndScoped(t *testing.T) {
	s, y := topicStore(t)
	a, _ := s.CreateTopic("alice", y.HiredAt, TopicByUser, TopicInput{Title: "Alpha"})
	b, _ := s.CreateTopic("alice", y.HiredAt, TopicByUser, TopicInput{Title: "Beta"})
	rec, err := s.RegisterTopicSend("alice", y.HiredAt, a.ID, TopicSend{ID: sendA, ImageCount: 1, AgentSince: y.HiredAt}, "What about images?")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Context != "[Topic tp1: Alpha]" || rec.Preview != "What about images?" || rec.ImageCount != 1 || rec.State != "sending" {
		t.Fatalf("send %+v", rec)
	}
	if got := TopicMessage(rec.Context, "What about images?"); got != "[Topic tp1: Alpha]\n\nWhat about images?" {
		t.Fatalf("message %q", got)
	}
	// A retry after renaming the topic sends the same frozen line.
	a, _ = s.PatchTopic("alice", y.HiredAt, TopicByUser, a.ID, TopicPatch{Revision: a.Revision, Title: ptr("Alpha renamed")})
	again, err := s.RegisterTopicSend("alice", y.HiredAt, a.ID, TopicSend{ID: sendA, ImageCount: 1}, "What about images?")
	if err != nil || again.Context != rec.Context || !again.At.Equal(rec.At) {
		t.Fatalf("retry %+v %v", again, err)
	}
	// Sends do not change the revision, so they never conflict with an edit.
	if got, _ := s.Topic("alice", y.HiredAt, a.ID); got.Revision != a.Revision || len(got.Sends) != 1 {
		t.Fatalf("after send %+v", got)
	}
	if _, err := s.RegisterTopicSend("alice", y.HiredAt, b.ID, TopicSend{ID: sendA}, "x"); err == nil || !strings.Contains(err.Error(), "tp1") {
		t.Fatalf("an ID of another topic: %v", err)
	}
	cmd, err := s.RegisterTopicSend("alice", y.HiredAt, b.ID, TopicSend{ID: sendB}, "/skill:review now")
	if err != nil || cmd.Context != "" || TopicMessage(cmd.Context, "/skill:review now") != "/skill:review now" {
		t.Fatalf("command %+v %v", cmd, err)
	}
	if err := s.SetTopicSendState("alice", y.HiredAt, b.ID, sendB, "accepted"); err != nil {
		t.Fatal(err)
	}
	b, _ = s.Topic("alice", y.HiredAt, b.ID)
	if b.Sends[0].State != "accepted" {
		t.Fatalf("state %+v", b.Sends)
	}
	b, _ = s.PatchTopic("alice", y.HiredAt, TopicByUser, b.ID, TopicPatch{Revision: b.Revision, Archive: true})
	third := strings.Replace(sendB, "bb", "cc", 1)
	if _, err := s.RegisterTopicSend("alice", y.HiredAt, b.ID, TopicSend{ID: third}, "x"); !errors.Is(err, ErrTopicArchived) {
		t.Fatalf("archived topic took a message: %v", err)
	}
	// An unchanged retry of a send made before archiving still finds it.
	if _, err := s.RegisterTopicSend("alice", y.HiredAt, b.ID, TopicSend{ID: sendB}, "/skill:review now"); err != nil {
		t.Fatalf("retry of an earlier send: %v", err)
	}
	if _, err := s.RegisterTopicSend("alice", y.HiredAt, "tp9", TopicSend{ID: third}, "x"); err == nil {
		t.Fatal("missing topic took a message")
	}
	if _, err := s.RegisterTopicSend("alice", y.HiredAt, a.ID, TopicSend{ID: "../../x"}, "x"); err == nil {
		t.Fatal("bad ID accepted")
	}
	// Sends are bounded.
	for i := 0; i < maxTopicSends+5; i++ {
		id := strings.Repeat("0", 40) + strings.Repeat("e", 0) + hex8(i)
		if _, err := s.RegisterTopicSend("alice", y.HiredAt, a.ID, TopicSend{ID: id}, strings.Repeat("長", maxTopicSendText+3)); err != nil {
			t.Fatal(err)
		}
	}
	a, _ = s.Topic("alice", y.HiredAt, a.ID)
	if len(a.Sends) != maxTopicSends || !a.Sends[0].Truncated || len([]rune(a.Sends[0].Preview)) != maxTopicSendText {
		t.Fatalf("bounded sends %d %+v", len(a.Sends), a.Sends[0])
	}
}

func hex8(i int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, 8)
	for j := 7; j >= 0; j-- {
		b[j] = digits[i&15]
		i >>= 4
	}
	return string(b)
}

func TestTopicsGenerationsAndArchive(t *testing.T) {
	s, old := topicStore(t)
	if _, err := s.CreateTopic("alice", old.HiredAt, TopicByUser, TopicInput{Title: "Old generation"}); err != nil {
		t.Fatal(err)
	}
	// Writes racing a dismissal either finish before it or fail; none
	// recreates the archived workspace.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.CreateTopic("alice", old.HiredAt, TopicByLead, TopicInput{Title: "racing"})
		}()
	}
	dest, err := s.Archive("alice", nil)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archived workspace recreated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".yesman", "topics.json")); err != nil {
		t.Fatalf("topics archived with the workspace: %v", err)
	}
	if _, err := s.CreateTopic("alice", old.HiredAt, TopicByUser, TopicInput{Title: "late"}); err == nil {
		t.Fatal("write after archive succeeded")
	}
	time.Sleep(2 * time.Millisecond)
	fresh, err := s.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.HiredAt.Equal(old.HiredAt) {
		t.Fatal("same hiring time")
	}
	if _, err := s.CreateTopic("alice", old.HiredAt, TopicByUser, TopicInput{Title: "stale page"}); err == nil {
		t.Fatal("old generation wrote into the new yesman")
	}
	list, err := s.Topics("alice", fresh.HiredAt)
	if err != nil || len(list) != 0 {
		t.Fatalf("new yesman starts without topics: %v %v", list, err)
	}
	// since zero (the info panel) reads whatever yesman has the name.
	if c := s.TopicCounts("alice"); c.Open != 0 || c.Error != "" {
		t.Fatalf("counts %+v", c)
	}
}

// Topics are read and written only inside the workspace: links out of it,
// whether topics.json or the .yesman directory, are refused, and nothing
// outside is read, created or changed.
func TestTopicsStayInsideTheWorkspace(t *testing.T) {
	outsideFile := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const outside = `{"version": 1, "topics": [{"id": "tp1", "title": "OUTSIDE-TITLE", "summary": "OUTSIDE-SECRET", "status": "open", "revision": 1}]}`

	t.Run("linked file", func(t *testing.T) {
		s, y := topicStore(t)
		target := outsideFile(t, outside)
		if err := os.Symlink(target, y.TopicsFile()); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
		if list, err := s.Topics("alice", y.HiredAt); err == nil {
			t.Fatalf("read through a link out of the workspace: %+v", list)
		}
		if _, err := s.CreateTopic("alice", y.HiredAt, TopicByLead, TopicInput{Title: "x"}); err == nil {
			t.Fatal("wrote through a link out of the workspace")
		}
		if c := s.TopicCounts("alice"); c.Error == "" {
			t.Fatal("counts must report the link")
		}
		if data, _ := os.ReadFile(target); string(data) != outside {
			t.Fatalf("outside file changed: %s", data)
		}
		if info, err := os.Lstat(y.TopicsFile()); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the link was replaced: %v %v", info, err)
		}
	})

	t.Run("linked metadata directory", func(t *testing.T) {
		s, y := topicStore(t)
		out := filepath.Join(t.TempDir(), "meta")
		if err := os.Rename(y.MetaDir(), out); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(out, y.MetaDir()); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
		if _, err := s.CreateTopic("alice", y.HiredAt, TopicByLead, TopicInput{Title: "outside write"}); err == nil {
			t.Fatal("wrote through a linked .yesman")
		}
		entries, _ := os.ReadDir(out)
		for _, e := range entries {
			if strings.Contains(e.Name(), "topics") {
				t.Fatalf("created %s outside the workspace", e.Name())
			}
		}
		if err := os.WriteFile(filepath.Join(out, "topics.json"), []byte(outside), 0o644); err != nil {
			t.Fatal(err)
		}
		if list, err := s.Topics("alice", y.HiredAt); err == nil {
			t.Fatalf("read through a linked .yesman: %+v", list)
		}
	})

	t.Run("link inside the workspace", func(t *testing.T) {
		// topics.json is a file of its own: even a link to another file of
		// .yesman is neither read nor written through.
		s, y := topicStore(t)
		inner := filepath.Join(y.MetaDir(), "kept.json")
		if err := os.WriteFile(inner, []byte(`{"version": 1, "topics": []}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("kept.json", y.TopicsFile()); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
		if _, err := s.CreateTopic("alice", y.HiredAt, TopicByUser, TopicInput{Title: "inside"}); err == nil {
			t.Fatal("wrote through a link")
		}
		if data, _ := os.ReadFile(inner); string(data) != `{"version": 1, "topics": []}` {
			t.Fatalf("the link's target was written: %s", data)
		}
	})

	t.Run("hard link", func(t *testing.T) {
		s, y := topicStore(t)
		target := outsideFile(t, outside)
		if err := os.Link(target, y.TopicsFile()); err != nil {
			t.Skip("hard links unavailable:", err)
		}
		if list, err := s.Topics("alice", y.HiredAt); err == nil || !strings.Contains(err.Error(), "hard link") {
			t.Fatalf("read a hard-linked file: %+v %v", list, err)
		}
		if _, err := s.CreateTopic("alice", y.HiredAt, TopicByLead, TopicInput{Title: "x"}); err == nil {
			t.Fatal("wrote over a hard-linked file")
		}
		if c := s.TopicCounts("alice"); c.Error == "" {
			t.Fatal("counts must report the hard link")
		}
		if data, _ := os.ReadFile(target); string(data) != outside {
			t.Fatalf("outside file changed: %s", data)
		}
		if data, _ := os.ReadFile(y.TopicsFile()); string(data) != outside {
			t.Fatal("the workspace's name for it was replaced")
		}
	})

	// The workspace directory itself replaced by a link, out of the store or
	// to another workspace: neither is that workspace's .yesman.
	for _, to := range []string{"outside", "another workspace"} {
		t.Run("linked workspace to "+to, func(t *testing.T) {
			s, y := topicStore(t)
			dest := filepath.Join(t.TempDir(), "moved")
			if to == "another workspace" {
				bob, err := s.Hire("bob")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(bob.TopicsFile(), []byte(outside), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(y.Path); err != nil {
					t.Fatal(err)
				}
				dest = bob.Path
			} else {
				if err := os.Rename(y.Path, dest); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dest, ".yesman", "topics.json"), []byte(outside), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(dest, y.Path); err != nil {
				t.Skip("symlinks unavailable:", err)
			}
			if list, err := s.Topics("alice", time.Time{}); err == nil {
				t.Fatalf("read through a linked workspace: %+v", list)
			}
			if _, err := s.CreateTopic("alice", time.Time{}, TopicByLead, TopicInput{Title: "x"}); err == nil {
				t.Fatal("wrote through a linked workspace")
			}
			if data, _ := os.ReadFile(filepath.Join(dest, ".yesman", "topics.json")); string(data) != outside {
				t.Fatalf("the link's target changed: %s", data)
			}
		})
	}

	t.Run(".yesman linked inside the workspace", func(t *testing.T) {
		s, y := topicStore(t)
		other := filepath.Join(y.Path, "other")
		if err := os.Rename(y.MetaDir(), other); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("other", y.MetaDir()); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
		if _, err := s.CreateTopic("alice", time.Time{}, TopicByLead, TopicInput{Title: "x"}); err == nil {
			t.Fatal("wrote through a linked .yesman")
		}
		if _, err := os.Stat(filepath.Join(other, "topics.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("created topics.json through the link")
		}
	})

	t.Run("store reached through a link", func(t *testing.T) {
		// The store's own directory may be an alias the user configured.
		real := t.TempDir()
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(real, alias); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
		s := New(alias, filepath.Join(t.TempDir(), "archive"))
		y, err := s.Hire("alice")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateTopic("alice", y.HiredAt, TopicByUser, TopicInput{Title: "via alias"}); err != nil {
			t.Fatal(err)
		}
		if list, err := s.Topics("alice", y.HiredAt); err != nil || len(list) != 1 {
			t.Fatalf("%v %v", list, err)
		}
	})

	t.Run("not a regular file", func(t *testing.T) {
		s, y := topicStore(t)
		if err := os.Mkdir(y.TopicsFile(), 0o755); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(y.TopicsFile(), "keep")
		if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateTopic("alice", y.HiredAt, TopicByUser, TopicInput{Title: "x"}); err == nil || !strings.Contains(err.Error(), "topics.json") {
			t.Fatalf("a directory taken for topics.json: %v", err)
		}
		if data, _ := os.ReadFile(sentinel); string(data) != "keep" {
			t.Fatal("the directory's content changed")
		}
		entries, _ := os.ReadDir(y.MetaDir())
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".topics-") {
				t.Fatalf("temporary file left: %s", e.Name())
			}
		}
	})

}
