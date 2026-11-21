package yesmen

import (
	"os"
	"strings"
	"testing"
)

func TestHandoffResponsePersistenceAndRetry(t *testing.T) {
	s := New(t.TempDir(), t.TempDir())
	y, err := s.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []HandoffResponse{
		{Kind: ResponseChoice, Options: []string{"one", "ONE"}},
		{Kind: ResponseChoice, Options: []string{"one"}},
		{Kind: ResponseChoice, Options: []string{"one", "two\nthree"}},
		{Kind: ResponseAction, AllowNote: true},
		{Kind: "run-command"},
	} {
		if _, _, err := s.AddHandoff(y.Name, Handoff{Type: HandoffDecision, Title: "Synthetic", Response: &spec}); err == nil {
			t.Fatalf("accepted invalid spec %#v", spec)
		}
	}
	if _, _, err := s.AddHandoff(y.Name, Handoff{Type: HandoffLogin, Title: "Sign in", Response: &HandoffResponse{Kind: ResponseText}}); err == nil {
		t.Fatal("login offered a text field")
	}
	h, _, err := s.AddHandoff(y.Name, Handoff{Type: HandoffDecision, Title: "Display check", Response: &HandoffResponse{
		Kind: ResponseChoice, Options: []string{"已恢复", "背光全灭", "背光亮但无画面"}, AllowNote: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, answer := range []*HandoffAnswer{nil, {Choice: "other"}, {Choice: "已恢复", Text: "extra"}, {Choice: "已恢复", Note: strings.Repeat("🎛", MaxResponseText+1)}} {
		if _, _, err := s.RespondHandoff(y.Name, h.ID, HandoffDone, answer); err == nil {
			t.Fatalf("accepted invalid response %#v", answer)
		}
	}
	answer := &HandoffAnswer{Choice: "背光亮但无画面", Note: "复查\nSecond line"}
	if _, _, err := s.RespondHandoff(y.Name, h.ID, HandoffNotNeeded, answer); err == nil {
		t.Fatal("dismissal accepted with an answer")
	}
	first, changed, err := s.RespondHandoff(y.Name, h.ID, HandoffDone, answer)
	if err != nil || !changed || first.Open() || *first.Answer != *answer {
		t.Fatalf("first response: %+v, %t, %v", first, changed, err)
	}
	before, err := os.ReadFile(y.HandoffsFile())
	if err != nil {
		t.Fatal(err)
	}
	// A fresh Store represents a process that did not see the first request.
	restarted := New(s.root, s.archive)
	if again, changed, err := restarted.RespondHandoff(y.Name, h.ID, HandoffDone, answer); err != nil || changed || !again.ClosedAt.Equal(first.ClosedAt) || *again.Answer != *answer {
		t.Fatalf("retry after restart: %+v, %t, %v", again, changed, err)
	}
	if _, _, err := restarted.RespondHandoff(y.Name, h.ID, HandoffDone, &HandoffAnswer{Choice: "已恢复"}); err == nil {
		t.Fatal("an edited answer overwrote an existing answer")
	}
	after, err := os.ReadFile(y.HandoffsFile())
	if err != nil || string(before) != string(after) {
		t.Fatal("retry or refusal changed the file")
	}
}
