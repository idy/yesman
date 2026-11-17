package restart

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func intent(id string) Intent {
	return Intent{Version: Version, ID: id, Kind: KindRestart, CreatedAt: t0, FromVersion: "0.1.47",
		Agents: []Agent{
			{Yesman: "demo", Name: "lead", Role: "lead", WorkspaceHiredAt: t0.Add(-time.Hour), AgentSince: t0.Add(-time.Hour), Reason: ReasonWorking},
			{Yesman: "demo", Name: "fixer", Role: "worker", WorkspaceHiredAt: t0.Add(-time.Hour), AgentSince: t0.Add(-time.Minute), Reason: ReasonReport},
		}}
}

func dirAt(t *testing.T, now time.Time) *Dir {
	d := Open(filepath.Join(t.TempDir(), "restart"))
	d.now = func() time.Time { return now }
	return d
}

func TestClaimOnce(t *testing.T) {
	d := dirAt(t, t0.Add(time.Minute))
	id := NewID()
	if err := d.Write(intent(id)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(d.Path, intentName))
	if err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("intent file %v %v", fi, err)
	}
	in, claim, found, rejected, err := d.Claim()
	if err != nil || !found || rejected != "" || in.ID != id || len(in.Agents) != 2 || !strings.HasPrefix(claim, "claimed-") {
		t.Fatalf("Claim = %+v %q %v %q %v", in, claim, found, rejected, err)
	}
	// A second start, or one after a crash before Finish, finds nothing.
	if _, _, found, _, err := d.Claim(); found || err != nil {
		t.Fatalf("second Claim found=%v err=%v", found, err)
	}
	res := Result{ID: id, ResumedAt: t0.Add(time.Minute), Agents: []Outcome{{Yesman: "demo", Name: "lead", Role: "lead", Reason: ReasonWorking, Outcome: Resumed}}}
	if err := d.Finish(claim, res); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(d.Path, claim)); !os.IsNotExist(err) {
		t.Fatalf("claimed file left: %v", err)
	}
	last, ok, err := d.Last()
	if err != nil || !ok || last.ID != id || last.Agents[0].Outcome != Resumed {
		t.Fatalf("Last = %+v %v %v", last, ok, err)
	}
}

func TestNoIntent(t *testing.T) {
	d := dirAt(t, t0)
	if _, _, found, _, err := d.Claim(); found || err != nil {
		t.Fatalf("Claim without a directory: found=%v err=%v", found, err)
	}
	if _, ok, err := d.Last(); ok || err != nil {
		t.Fatalf("Last without a directory: %v %v", ok, err)
	}
	if err := d.Cancel(NewID()); err != nil {
		t.Fatal(err)
	}
}

func TestCancelOnlyOwnIntent(t *testing.T) {
	d := dirAt(t, t0)
	mine, other := NewID(), NewID()
	if err := d.Write(intent(other)); err != nil {
		t.Fatal(err)
	}
	if err := d.Cancel(mine); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.Path, intentName)); err != nil {
		t.Fatalf("another restart's intent was removed: %v", err)
	}
	if err := d.Cancel(other); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.Path, intentName)); !os.IsNotExist(err) {
		t.Fatalf("own intent kept: %v", err)
	}
}

// rejectedFile returns the one rejected file and its bytes.
func rejectedFile(t *testing.T, d *Dir) (string, []byte) {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(d.Path, "rejected-*.json"))
	if len(matches) != 1 {
		t.Fatalf("rejected files: %v", matches)
	}
	b, _ := os.ReadFile(matches[0])
	return matches[0], b
}

func TestRejectsAndKeeps(t *testing.T) {
	raw := func(in Intent) []byte { b, _ := json.Marshal(in); return b }
	future := intent(NewID())
	future.CreatedAt = t0.Add(10 * time.Minute)
	v2 := intent(NewID())
	v2.Version = 2
	dup := intent(NewID())
	dup.Agents = append(dup.Agents, dup.Agents[0])
	leadReport := intent(NewID())
	leadReport.Agents[0].Reason = ReasonReport
	noIdentity := intent(NewID())
	noIdentity.Agents[1].AgentSince = time.Time{}
	badName := intent(NewID())
	badName.Agents[0].Yesman = "../x"
	for _, c := range []struct {
		name string
		now  time.Time
		data []byte
		want string
	}{
		{"expired", t0.Add(MaxAge + time.Second), raw(intent(NewID())), "older than"},
		{"future", t0, raw(future), "future"},
		{"version", t0, raw(v2), "version 2"},
		{"duplicate", t0, raw(dup), "twice"},
		{"lead report", t0, raw(leadReport), "only a worker"},
		{"identity", t0, raw(noIdentity), "identity"},
		{"name", t0, raw(badName), "malformed"},
		{"corrupt", t0, []byte(`{"version":1,`), "malformed"},
		{"unknown field", t0, []byte(strings.Replace(string(raw(intent(NewID()))), `"version"`, `"token":"x","version"`, 1)), "malformed"},
		{"oversize", t0, make([]byte, maxIntentBytes+1), "larger"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := dirAt(t, c.now)
			if err := os.MkdirAll(d.Path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d.Path, intentName), c.data, 0o600); err != nil {
				t.Fatal(err)
			}
			in, kept, found, rejected, err := d.Claim()
			if err != nil || !found || !strings.Contains(rejected, c.want) || in.ID != "" {
				t.Fatalf("Claim = %+v %q found=%v rejected=%q err=%v", in, kept, found, rejected, err)
			}
			name, b := rejectedFile(t, d)
			if filepath.Base(name) != kept || string(b) != string(c.data) {
				t.Fatalf("the rejected intent was not kept as it was: %s", name)
			}
			if _, _, found, _, _ := d.Claim(); found {
				t.Fatal("a rejected intent was seen again")
			}
		})
	}
}

func TestRejectsLinkAndPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no FIFOs; links need privileges")
	}
	t.Run("link", func(t *testing.T) {
		d := dirAt(t, t0)
		target := filepath.Join(t.TempDir(), "elsewhere.json")
		b, _ := json.Marshal(intent(NewID()))
		_ = os.WriteFile(target, b, 0o600)
		_ = os.MkdirAll(d.Path, 0o700)
		if err := os.Symlink(target, filepath.Join(d.Path, intentName)); err != nil {
			t.Fatal(err)
		}
		_, kept, found, rejected, err := d.Claim()
		if err != nil || !found || !strings.Contains(rejected, "not a regular file") {
			t.Fatalf("Claim link: %q %v %q %v", kept, found, rejected, err)
		}
		if fi, err := os.Lstat(filepath.Join(d.Path, kept)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the link was not kept as a link: %v %v", fi, err)
		}
		if got, _ := os.ReadFile(target); string(got) != string(b) {
			t.Fatal("the link's target changed")
		}
	})
	t.Run("hard link", func(t *testing.T) {
		d := dirAt(t, t0)
		_ = os.MkdirAll(d.Path, 0o700)
		b, _ := json.Marshal(intent(NewID()))
		other := filepath.Join(d.Path, "other.json")
		_ = os.WriteFile(other, b, 0o600)
		if err := os.Link(other, filepath.Join(d.Path, intentName)); err != nil {
			t.Fatal(err)
		}
		if _, _, found, rejected, err := d.Claim(); err != nil || !found || !strings.Contains(rejected, "other links") {
			t.Fatalf("Claim hard link: %v %q %v", found, rejected, err)
		}
	})
	t.Run("fifo", func(t *testing.T) {
		d := dirAt(t, t0)
		_ = os.MkdirAll(d.Path, 0o700)
		if err := syscall.Mkfifo(filepath.Join(d.Path, intentName), 0o600); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		var rejected string
		go func() {
			defer close(done)
			_, _, _, rejected, _ = d.Claim()
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Claim blocked on a pipe")
		}
		if !strings.Contains(rejected, "not a regular file") {
			t.Fatalf("rejected = %q", rejected)
		}
	})
	t.Run("directory replaced by a link", func(t *testing.T) {
		d := dirAt(t, t0)
		real := filepath.Join(t.TempDir(), "real")
		_ = os.MkdirAll(real, 0o700)
		b, _ := json.Marshal(intent(NewID()))
		_ = os.WriteFile(filepath.Join(real, intentName), b, 0o600)
		if err := os.Symlink(real, d.Path); err != nil {
			t.Fatal(err)
		}
		if _, _, found, _, err := d.Claim(); found || err == nil {
			t.Fatalf("Claim through a linked directory: found=%v err=%v", found, err)
		}
		if err := d.Write(intent(NewID())); err == nil {
			t.Fatal("Write through a linked directory")
		}
		if _, err := os.Stat(filepath.Join(real, intentName)); err != nil {
			t.Fatal("the linked directory's file was touched")
		}
	})
}

func TestWriteKeepsNoSecretsAndValidates(t *testing.T) {
	d := dirAt(t, t0)
	bad := intent("not-an-id")
	if err := d.Write(bad); err == nil {
		t.Fatal("wrote a malformed intent")
	}
	if err := d.Write(intent(NewID())); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(d.Path, intentName))
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for k := range fields {
		switch k {
		case "version", "id", "kind", "createdAt", "fromVersion", "toVersion", "agents":
		default:
			t.Errorf("unexpected field %q", k)
		}
	}
	entries, _ := os.ReadDir(d.Path)
	if len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
}
