package yesmen

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReviewR2ContinuationInPlaceConflict(t *testing.T) {
	s := waitStore(t)
	y, _ := s.Get("alice")
	if _, err := s.UpdateContinuation("alice", y.HiredAt, func(c *Continuation) bool { c.LeadPausedAt = time.Now().UTC(); return true }); err != nil {
		t.Fatal(err)
	}
	var edited []byte
	_, err := s.UpdateContinuation("alice", y.HiredAt, func(c *Continuation) bool {
		raw, e := os.ReadFile(y.ContinuationFile())
		if e != nil {
			t.Fatal(e)
		}
		var obj map[string]json.RawMessage
		if e := json.Unmarshal(raw, &obj); e != nil {
			t.Fatal(e)
		}
		obj["newerUserField"] = json.RawMessage(`"MUST KEEP"`)
		edited, e = json.Marshal(obj)
		if e != nil {
			t.Fatal(e)
		}
		// Same inode, real in-place edit after the opened snapshot, before any replace checks.
		if e := os.WriteFile(y.ContinuationFile(), edited, 0600); e != nil {
			t.Fatal(e)
		}
		c.Attempts = append(c.Attempts, time.Now().UTC())
		return true
	})
	after, _ := os.ReadFile(y.ContinuationFile())
	if err == nil || !bytes.Equal(after, edited) {
		t.Fatalf("concurrent in-place edit overwritten: err=%v sentinel survives=%v", err, bytes.Contains(after, []byte("MUST KEEP")))
	}
}

func TestReviewR2ContinuationReplacementAndEncodedBudget(t *testing.T) {
	s := waitStore(t)
	y, _ := s.Get("alice")
	stamp := time.Now().UTC()
	if _, err := s.UpdateContinuation("alice", y.HiredAt, func(c *Continuation) bool {
		c.LeadPausedAt = stamp
		c.extra = map[string]json.RawMessage{"future": json.RawMessage(`9007199254740993`)}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(y.ContinuationFile())
	_, err := s.UpdateContinuation("alice", y.HiredAt, func(c *Continuation) bool {
		c.extra["oversize"] = json.RawMessage(`"` + strings.Repeat("\\u0000", 200000) + `"`)
		return true
	})
	after, _ := os.ReadFile(y.ContinuationFile())
	if err == nil || !bytes.Equal(original, after) {
		t.Fatal("encoded oversize changed file")
	}
	var replacement []byte
	_, err = s.UpdateContinuation("alice", y.HiredAt, func(c *Continuation) bool {
		replacement = append([]byte(" \n"), original...)
		p := y.ContinuationFile() + ".fixture"
		if e := os.WriteFile(p, replacement, 0600); e != nil {
			t.Fatal(e)
		}
		if e := os.Rename(p, y.ContinuationFile()); e != nil {
			t.Fatal(e)
		}
		c.Attempts = append(c.Attempts, stamp)
		return true
	})
	after, _ = os.ReadFile(y.ContinuationFile())
	if err == nil || !bytes.Equal(replacement, after) {
		t.Fatal("replacement conflict changed file")
	}
	c, e := s.Continuation("alice")
	if e != nil || string(c.extra["future"]) != "9007199254740993" || !c.LeadPausedAt.Equal(stamp) {
		t.Fatalf("cold record: %+v %v", c, e)
	}
}

func TestReviewR2ContinuationNestedUnknownRetained(t *testing.T) {
	s := waitStore(t)
	y, _ := s.Get("alice")
	raw := `{"hiredAt":"` + y.HiredAt.Format(time.RFC3339Nano) + `","tasks":{"t1":{"sig":"x","progress":"p","count":1,"at":"2026-01-01T00:00:00Z","future":9007199254740993}}}`
	if err := os.WriteFile(y.ContinuationFile(), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateContinuation("alice", y.HiredAt, func(c *Continuation) bool { c.LeadPausedAt = time.Now().UTC(); return true }); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(y.ContinuationFile())
	if !bytes.Contains(after, []byte("9007199254740993")) {
		t.Fatal("unrelated pause write dropped future field within retained task notice")
	}
}
