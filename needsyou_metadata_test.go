package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// A workspace whose yesman.json is too large is reported, never shown as a
// yesman: its requests are not offered, counted or closed, while another
// yesman's are, with its lead still running.
func TestNeedsYouBesideOversizedMetadata(t *testing.T) {
	s, alice, bob, original := metadataServiceFixture(t)
	a := s.app
	a.keepAwake = func(string) func() { return func() {} }
	a.notifier = func(context.Context, string, string, string) {}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ha, _, err := a.yesmen.AddHandoff("alice", yesmen.Handoff{Type: yesmen.HandoffManual, Title: "alice's step"})
	if err != nil {
		t.Fatal(err)
	}
	hb, _, err := a.yesmen.AddHandoff("bob", yesmen.Handoff{Type: yesmen.HandoffLogin, Title: "bob's login"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(alice.MetaDir(), "yesman.json")
	oversize := append(append([]byte{}, original...), bytes.Repeat([]byte(" "), (1<<20)+1-len(original))...)
	if err := os.WriteFile(path, oversize, 0o600); err != nil {
		t.Fatal(err)
	}
	handoffs, _ := os.ReadFile(alice.HandoffsFile())

	st, err := s.State(ctx)
	if err != nil || len(st.Yesmen) != 1 || st.Yesmen[0].Name != "bob" || len(st.WorkspaceErrors) != 1 || st.WorkspaceErrors[0].Name != "alice" {
		t.Fatalf("State: %+v %v", st, err)
	}
	view := st.Yesmen[0]
	if view.HandoffsError != "" || len(view.Handoffs) != 1 || view.Handoffs[0].ID != hb.ID || view.CallMemory == nil {
		t.Fatalf("Bob's requests: %+v", view)
	}
	if encoded, _ := json.Marshal(st); bytes.Contains(encoded, []byte("alice's step")) {
		t.Fatal("State offered a request of the workspace it cannot identify")
	}
	a.countHandoffs()
	if a.waiting != 1 {
		t.Errorf("counted %d waiting, want only Bob's request", a.waiting)
	}
	var issue *yesmen.MetadataError
	if _, err := a.yesmen.Handoffs("alice"); !errors.As(err, &issue) {
		t.Errorf("Alice's handoffs: %v", err)
	}
	if _, err := s.ResolveHandoff("alice", alice.HiredAt, ha.ID, yesmen.HandoffDone); !errors.As(err, &issue) {
		t.Errorf("closing Alice's request: %v", err)
	}
	if after, _ := os.ReadFile(alice.HandoffsFile()); !bytes.Equal(after, handoffs) {
		t.Error("Alice's handoffs changed")
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, oversize) {
		t.Error("Alice's metadata changed")
	}
	closed, err := s.ResolveHandoff("bob", bob.HiredAt, hb.ID, yesmen.HandoffDone)
	if err != nil || closed.Resolution != yesmen.HandoffDone {
		t.Fatalf("closing Bob's request: %+v %v", closed, err)
	}
	if lead, err := a.agents.Get("bob/lead"); err != nil || lead.StartedAt.IsZero() {
		t.Errorf("Bob's lead: %+v %v", lead, err)
	}
}
