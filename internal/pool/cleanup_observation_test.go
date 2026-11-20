package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/safefs"
)

func TestCleanupDirectoryIdentityStrength(t *testing.T) {
	old := safefs.Identity{Dev: 7, Ino: 42}
	birth := safefs.Identity{Dev: 7, Ino: 42, Birth: 123, HasBirth: true}
	for _, tc := range []struct {
		name             string
		expected, actual safefs.Identity
		same             bool
	}{
		{"pinned birthless directory", old, old, true},
		{"different inode", old, safefs.Identity{Dev: 7, Ino: 41}, false},
		{"different device", old, safefs.Identity{Dev: 8, Ino: 42}, false},
		{"missing device", safefs.Identity{Ino: 42}, safefs.Identity{Ino: 42}, false},
		{"missing inode", safefs.Identity{Dev: 7}, safefs.Identity{Dev: 7}, false},
		{"known birth lost", birth, old, false},
		{"different birth", birth, safefs.Identity{Dev: 7, Ino: 42, Birth: 124, HasBirth: true}, false},
		{"recorded root without a persisted device", safefs.Identity{Ino: 42, Birth: 123, HasBirth: true}, birth, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if sameCleanupRoot(tc.expected, tc.actual) != tc.same {
				t.Fatal(tc)
			}
		})
	}
}

func TestCleanupPrivateHistoryBoundaries(t *testing.T) {
	p, r, wt, ws := newTestCheckout(t)
	admin := run(t, wt.Path, "rev-parse", "--absolute-git-dir")
	run(t, r.Path, "config", "core.logAllRefUpdates", "false")
	log := filepath.Join(admin, "logs", "HEAD")
	if err := os.Remove(log); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	run(t, wt.Path, "switch", "--detach", "HEAD")
	run(t, wt.Path, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "ORIG_HEAD only")
	old := run(t, wt.Path, "rev-parse", "HEAD")
	run(t, wt.Path, "switch", wt.Branch)
	orig := filepath.Join(admin, "ORIG_HEAD")
	for _, tc := range []struct {
		name, field string
		data        []byte
	}{
		{"oversized reflog", log, []byte(strings.Repeat("x", maxHistoryFile+1))},
		{"truncated reflog", log, []byte(old + " " + wt.Head + " a")},
		{"noncommit", orig, []byte(run(t, wt.Path, "rev-parse", "HEAD^{tree}") + "\n")},
		{"missing object", orig, []byte(strings.Repeat("b", len(old)) + "\n")},
		{"malformed old object", log, []byte(strings.Repeat("?", len(old)) + " " + wt.Head + " a\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(tc.field), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tc.field, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Remove(tc.field) })
			observation, err := p.InspectTemporaryWorktree(t.Context(), ws, wt)
			if observation != nil {
				observation.Close()
			}
			if err == nil {
				t.Fatal("invalid private history was eligible for removal")
			}
			if err := p.RemoveTemporaryWorktree(t.Context(), ws, wt); err == nil {
				t.Fatal("expected to retain checkout")
			}
			if _, err := os.Stat(wt.Path); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := os.WriteFile(orig, []byte(old+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveTemporaryWorktree(t.Context(), ws, wt); err != nil {
		t.Fatal(err)
	}
	run(t, r.Path, "reflog", "expire", "--expire=now", "--all")
	run(t, r.Path, "gc", "--prune=now")
	if got := run(t, r.Path, "show-ref", "--verify", "--hash", "refs/yesman/worktree-history/"+wt.CleanupID+"/"+old); got != old {
		t.Fatal("missing ORIG_HEAD checkpoint", got)
	}
	run(t, r.Path, "cat-file", "-e", old+"^{commit}")
}
