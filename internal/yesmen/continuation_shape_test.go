package yesmen

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// A damaged record is not an empty one: it is reported, left as it is, and
// never written over. Only a valid record of another hire starts afresh.
func TestContinuationShapeFailsClosed(t *testing.T) {
	s := waitStore(t)
	y, _ := s.Get("alice")
	hired := `"` + y.HiredAt.Format(time.RFC3339Nano) + `"`
	for name, raw := range map[string]string{
		"null":               `null`,
		"array":              `[]`,
		"string":             `"x"`,
		"empty object":       `{}`,
		"no hiredAt":         `{"leadPausedAt":"2026-01-01T00:00:00Z"}`,
		"null hiredAt":       `{"hiredAt":null}`,
		"zero hiredAt":       `{"hiredAt":"0001-01-01T00:00:00Z"}`,
		"bad hiredAt":        `{"hiredAt":5}`,
		"null leadPausedAt":  `{"hiredAt":` + hired + `,"leadPausedAt":null}`,
		"null attempt":       `{"hiredAt":` + hired + `,"attempts":[null]}`,
		"null tasks":         `{"hiredAt":` + hired + `,"tasks":null}`,
		"null task notice":   `{"hiredAt":` + hired + `,"tasks":{"t1":null}}`,
		"array task notice":  `{"hiredAt":` + hired + `,"tasks":{"t1":[]}}`,
		"null notice at":     `{"hiredAt":` + hired + `,"tasks":{"t1":{"sig":"x","at":null}}}`,
		"wrong notice count": `{"hiredAt":` + hired + `,"tasks":{"t1":{"count":"1"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(y.ContinuationFile(), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Continuation("alice"); err == nil {
				t.Fatal("a damaged record read as a valid one")
			}
			_, err := s.UpdateContinuation("alice", y.HiredAt, func(c *Continuation) bool {
				c.LeadPausedAt = time.Now().UTC()
				return true
			})
			after, _ := os.ReadFile(y.ContinuationFile())
			if err == nil || !bytes.Equal(after, []byte(raw)) {
				t.Fatalf("a damaged record was written over: err=%v now %q", err, after)
			}
		})
	}
	t.Run("another hire's valid record starts afresh", func(t *testing.T) {
		raw := `{"hiredAt":"2020-01-01T00:00:00Z","leadPausedAt":"2020-01-02T00:00:00Z","future":1}`
		if err := os.WriteFile(y.ContinuationFile(), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := s.Continuation("alice")
		if err != nil || !c.LeadPausedAt.IsZero() || !c.HiredAt.Equal(y.HiredAt) {
			t.Fatalf("another hire's record: %+v %v", c, err)
		}
	})
}

// A workspace hired before hires were recorded has none: its own record,
// of no hire either, reads and writes as usual.
func TestContinuationOfUnrecordedHire(t *testing.T) {
	s := waitStore(t)
	y, _ := s.Get("alice")
	meta := y.MetaDir() + "/yesman.json"
	if err := os.WriteFile(meta, []byte(`{"displayName":"alice"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	y, _ = s.Get("alice")
	if !y.HiredAt.IsZero() {
		t.Fatal("fixture: the hire is still recorded")
	}
	stamp := time.Now().UTC()
	for range 2 {
		if _, err := s.UpdateContinuation("alice", y.HiredAt, func(c *Continuation) bool {
			c.LeadPausedAt = stamp
			c.Attempts = append(c.Attempts, time.Now().UTC())
			return true
		}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := s.Continuation("alice")
	if err != nil || !c.LeadPausedAt.Equal(stamp) || len(c.Attempts) != 2 {
		t.Fatalf("%+v %v", c, err)
	}
}

// A notice written again for its task is a new notice; the others, and
// one only marked delivered, keep the fields this version does not know.
func TestTaskNoticeUnknownFields(t *testing.T) {
	s := waitStore(t)
	y, _ := s.Get("alice")
	raw := `{"hiredAt":"` + y.HiredAt.Format(time.RFC3339Nano) + `","tasks":{` +
		`"t1":{"sig":"a","progress":"p","count":1,"at":"2026-01-01T00:00:00Z","future":9007199254740993},` +
		`"t2":{"sig":"b","progress":"p","count":1,"at":"2026-01-01T00:00:00Z","later":{"n":12345678901234567890}}}}`
	if err := os.WriteFile(y.ContinuationFile(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateContinuation("alice", y.HiredAt, func(c *Continuation) bool {
		r := c.Tasks["t2"]
		r.Delivered = true
		c.Tasks["t2"] = r
		c.Tasks["t3"] = TaskNotice{Sig: "c"}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(y.ContinuationFile())
	for _, want := range []string{"9007199254740993", "12345678901234567890"} {
		if !strings.Contains(string(after), want) {
			t.Fatalf("lost %s: %s", want, after)
		}
	}
	if _, err := s.UpdateContinuation("alice", y.HiredAt, func(c *Continuation) bool {
		c.Tasks["t1"] = TaskNotice{Sig: "new"}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(y.ContinuationFile())
	if strings.Contains(string(after), "9007199254740993") || !strings.Contains(string(after), "12345678901234567890") {
		t.Fatalf("a renewed notice kept its old fields, or another lost them: %s", after)
	}
}
