package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

func (f *boardFixture) topicsBytes(name string) []byte {
	f.t.Helper()
	y, err := f.store.Get(name)
	if err != nil {
		f.t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(y.Path, ".yesman", "topics.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	return raw
}

func (f *boardFixture) postTopics(token, body string) *httptest.ResponseRecorder {
	res := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/topics", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	f.srv.Handler().ServeHTTP(res, req)
	return res
}

// heldTopics sends a topic change whose body is held while between runs.
func (f *boardFixture) heldTopics(token, body string, between func()) *httptest.ResponseRecorder {
	f.t.Helper()
	gate := &gateBody{Reader: strings.NewReader(body), entered: make(chan struct{}), release: make(chan struct{})}
	res := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		req := httptest.NewRequest("POST", "/v1/topics", gate)
		req.Header.Set("Authorization", "Bearer "+token)
		f.srv.Handler().ServeHTTP(res, req)
		close(done)
	}()
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
	return res
}

// The board's counterexamples (TestBoardChangeAcrossRetirement) for topics:
// a lead's topic change authenticated before that lead was stopped and
// started again, or before its yesman was dismissed and hired again, must
// not land once that happened.
func TestTopicChangeAcrossRetirement(t *testing.T) {
	for _, archive := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop-restart", true: "archive-rehire"}[archive], func(t *testing.T) {
			f := newBoardFixture(t)
			oldY := f.hire("alice")
			if _, err := f.store.CreateTopic("alice", oldY.HiredAt, yesmen.TopicByUser, yesmen.TopicInput{Title: "Current topic"}); err != nil {
				t.Fatal(err)
			}
			old := f.start("alice/lead")
			if res := f.postTopics(old.Token, `{"create":{"title":"Current control"}}`); res.Code != 200 {
				t.Fatalf("current caller: %d %s", res.Code, res.Body)
			}
			var before, archived []byte
			var archivePath string
			res := f.heldTopics(old.Token, `{"id":"tp1","patch":{"revision":1,"summary":"stale summary"}}`, func() {
				if archive {
					var err error
					archivePath, err = f.store.Archive("alice", func(y yesmen.Yesman) error { return f.m.StopYesman(y.Name) })
					if err != nil {
						t.Fatal(err)
					}
					f.m.ForgetYesman("alice", oldY.HiredAt)
					y := f.hire("alice")
					if y.HiredAt.Equal(oldY.HiredAt) {
						t.Fatal("same-name hire kept the generation")
					}
					if _, err := f.store.CreateTopic("alice", y.HiredAt, yesmen.TopicByUser, yesmen.TopicInput{Title: "New generation topic"}); err != nil {
						t.Fatal(err)
					}
					if archived, err = os.ReadFile(filepath.Join(archivePath, ".yesman", "topics.json")); err != nil {
						t.Fatal(err)
					}
				} else if err := f.m.StopYesman("alice"); err != nil {
					t.Fatal(err)
				}
				renewed := f.start("alice/lead")
				if renewed.Token == old.Token || renewed.PID == old.PID {
					t.Fatal("no new process generation")
				}
				before = f.topicsBytes("alice")
			})
			if res.Code != 401 {
				t.Errorf("held request from the retired process: %d %s", res.Code, res.Body)
			}
			after := f.topicsBytes("alice")
			if !bytes.Equal(before, after) {
				t.Errorf("topics changed by a retired process:\n%s\n->\n%s", before, after)
			}
			var file struct {
				Topics []yesmen.Topic `json:"topics"`
			}
			if err := json.Unmarshal(after, &file); err != nil || len(file.Topics) == 0 {
				t.Fatalf("topics file: %v %s", err, after)
			}
			if tp1 := file.Topics[0]; tp1.Summary != "" || tp1.Revision != 1 {
				t.Errorf("tp1 changed: %+v", tp1)
			}
			if archive {
				if now, err := os.ReadFile(filepath.Join(archivePath, ".yesman", "topics.json")); err != nil || !bytes.Equal(now, archived) {
					t.Errorf("archived topics changed: %v", err)
				}
			}
		})
	}
}
