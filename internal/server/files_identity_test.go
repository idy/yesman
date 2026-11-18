package server

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/safefs"
	"github.com/idy/yesman/internal/yesmen"
)

// Fixture identities that no directory here has.
var (
	fooIdentity = yesmen.WorktreeRoot{Ino: 0xF00, Birth: 0xF00}
	barIdentity = yesmen.WorktreeRoot{Ino: 0xBA4, Birth: 0xBA4}
)

func setRoot(t *testing.T, x *worktreeFixture, dir string, root *yesmen.WorktreeRoot) {
	t.Helper()
	records, err := x.f.store.WorktreeRecords(x.y.Name)
	if err != nil {
		t.Fatal(err)
	}
	for i := range records {
		if records[i].Dir == dir {
			records[i].Root = root
		}
	}
	if err := x.f.store.SaveWorktreeRecords(x.y.Name, records); err != nil {
		t.Fatal(err)
	}
}

func recordOf(t *testing.T, x *worktreeFixture, dir string) yesmen.WorktreeRecord {
	t.Helper()
	records, _ := x.f.store.WorktreeRecords(x.y.Name)
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Dir == dir {
			return records[i]
		}
	}
	t.Fatalf("no record of %s", dir)
	return yesmen.WorktreeRecord{}
}

// The checkout's record carries its directory's identity from creation, and
// a grant is good only while the opened directory has that identity.
func TestManagedWorktreeIdentityIsRecordedAndChecked(t *testing.T) {
	x := newWorktreeFixture(t)
	x.checkout(t, "mine", "feature")
	mine := filepath.Join(x.y.Path, "mine")
	rec := recordOf(t, x, "mine")
	if rec.Root == nil {
		t.Fatal("the checkout's record has no directory identity")
	}
	d, err := safefs.Open(mine)
	if err != nil {
		t.Fatal(err)
	}
	now, _ := d.Identity()
	d.Close()
	if rec.Root.Ino != now.Ino || rec.Root.Birth != now.Birth {
		t.Fatalf("recorded %+v, directory %+v", *rec.Root, now)
	}
	target := filepath.Join(mine, "feature")
	if code := x.write(target, "owner\n"); code != 200 {
		t.Fatalf("ordinary write: %d", code)
	}
	sum := readSHA(t, x.f, x.w.Token, target)
	if code := postFile(x.f, x.w.Token, map[string]any{"op": "write", "path": target, "content": "edited\n", "expect": sum}).Code; code != 200 {
		t.Fatalf("ordinary edit: %d", code)
	}
	if code := postFile(x.f, x.w.Token, map[string]any{"op": "mkdir", "path": filepath.Join(mine, "newdir")}).Code; code != 200 {
		t.Fatalf("ordinary mkdir: %d", code)
	}
	if code := postFile(x.f, x.w.Token, map[string]any{"op": "access", "path": target}).Code; code != 200 {
		t.Fatalf("ordinary access: %d", code)
	}

	// A grant carrying an identity the directory does not have is refused
	// before any I/O: existing bytes stay, nothing new is made.
	grant, sub, err := x.f.srv.fileGrant(x.me, x.y, target, false)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(target)
	for _, id := range []yesmen.WorktreeRoot{fooIdentity, barIdentity, {Ino: rec.Root.Ino, Birth: rec.Root.Birth + 1}} {
		g := grant
		g.root = &id
		content := "MISMATCH"
		for _, req := range []fileRequest{
			{Op: "write", Content: &content},
			{Op: "read"},
			{Op: "access"},
		} {
			err := x.f.srv.Pool.WithWorktree(mine, func() error {
				_, err := x.f.srv.commitFile(x.y, g, sub, req)
				return err
			})
			if err == nil || !strings.Contains(err.Error(), "not the directory yesman created") {
				t.Fatalf("%s with identity %+v: %v", req.Op, id, err)
			}
		}
		created := "NEW"
		err := x.f.srv.Pool.WithWorktree(mine, func() error {
			_, err := x.f.srv.commitFile(x.y, g, []string{"created-by-mismatch"}, fileRequest{Op: "write", Content: &created})
			return err
		})
		if err == nil {
			t.Fatal("a mismatched grant created a file")
		}
	}
	if after, _ := os.ReadFile(target); !bytes.Equal(before, after) {
		t.Fatalf("target changed to %q", after)
	}
	if _, err := os.Lstat(filepath.Join(mine, "created-by-mismatch")); err == nil {
		t.Fatal("file created under a mismatched grant")
	}

	// The same through the endpoint, with the stored record naming Foo.
	setRoot(t, x, "mine", &fooIdentity)
	if code := x.write(target, "MISMATCH"); code == 200 {
		t.Fatal("wrote with a record naming another directory")
	}
	if after, _ := os.ReadFile(target); !bytes.Equal(before, after) {
		t.Fatalf("target changed to %q", after)
	}
	setRoot(t, x, "mine", rec.Root)
	if code := x.write(target, "restored\n"); code != 200 {
		t.Fatalf("write after the record is right again: %d", code)
	}
}

// The identity comparison, on supplied identities: a recorded or current
// identity without a usable creation time (none, or zero, which macOS also
// reports for none) is refused, as is any mismatch; a matching nonzero one
// passes.
func TestSameRootNeedsAUsableCreationTime(t *testing.T) {
	ws := safefs.Identity{Dev: 3, Ino: 1, Birth: 10, HasBirth: true}
	good := safefs.Identity{Dev: 3, Ino: 0xF00, Birth: 0xF00, HasBirth: true}
	if err := sameRoot(good, ws, &fooIdentity); err != nil {
		t.Fatalf("matching identity: %v", err)
	}
	zero := yesmen.WorktreeRoot{Ino: 0xF00, Birth: 0}
	convertedMissing := yesmen.WorktreeRoot{Ino: 0xF00, Birth: 6802270473709551616}
	for name, c := range map[string]struct {
		have safefs.Identity
		rec  *yesmen.WorktreeRoot
		why  string
	}{
		"no record":                  {good, nil, "no recorded"},
		"no current creation time":   {safefs.Identity{Dev: 3, Ino: 0xF00}, &fooIdentity, "no usable creation time"},
		"zero current creation time": {safefs.Identity{Dev: 3, Ino: 0xF00, HasBirth: true}, &fooIdentity, "no usable creation time"},
		"zero record, none now":      {safefs.Identity{Dev: 3, Ino: 0xF00}, &zero, "no usable creation time"},
		"zero record, zero now":      {safefs.Identity{Dev: 3, Ino: 0xF00, HasBirth: true}, &zero, "no usable creation time"},
		"zero record, real time now": {good, &zero, "no usable creation time"},
		"other file system":          {safefs.Identity{Dev: 4, Ino: 0xF00, Birth: 0xF00, HasBirth: true}, &fooIdentity, "not the directory"},
		"other inode":                {safefs.Identity{Dev: 3, Ino: 0xBA4, Birth: 0xF00, HasBirth: true}, &fooIdentity, "not the directory"},
		"other creation time":        {safefs.Identity{Dev: 3, Ino: 0xF00, Birth: 0xF01, HasBirth: true}, &fooIdentity, "not the directory"},
		// Windows' missing creation time (an all-zero FILETIME) once
		// converted to this; the current handle now reports none.
		"converted missing Windows time recorded, none now": {safefs.Identity{Dev: 3, Ino: 0xF00}, &convertedMissing, "no usable creation time"},
	} {
		err := sameRoot(c.have, ws, c.rec)
		if err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A record whose creation time is zero (as a checkout on a file system
// without creation times could have been recorded before) grants nothing:
// the write is refused, the file, the records and the checkout's
// registration are left as they are, and the record is not rebound to the
// directory's current identity.
func TestManagedWorktreeWithZeroCreationTimeIsRefused(t *testing.T) {
	x := newWorktreeFixture(t)
	x.checkout(t, "zero", "feature")
	rec := recordOf(t, x, "zero")
	if rec.Root == nil {
		t.Fatal("the checkout's record has no directory identity")
	}
	setRoot(t, x, "zero", &yesmen.WorktreeRoot{Ino: rec.Root.Ino, Birth: 0})
	dir := filepath.Join(x.y.Path, "zero")
	target := filepath.Join(dir, "feature")
	recordsFile := filepath.Join(x.y.Path, ".yesman", "worktrees.json")
	snapshot := func() [3][]byte {
		var s [3][]byte
		for i, p := range []string{target, recordsFile, filepath.Join(dir, ".git")} {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			s[i] = b
		}
		return s
	}
	before := snapshot()
	for _, req := range []map[string]any{
		{"op": "write", "path": target, "content": "ZERO"},
		{"op": "write", "path": filepath.Join(dir, "made-by-zero"), "content": "ZERO"},
		{"op": "mkdir", "path": filepath.Join(dir, "dir-by-zero")},
	} {
		res := postFile(x.f, x.w.Token, req)
		if res.Code == 200 || !strings.Contains(res.Body.String(), "no usable creation time") {
			t.Fatalf("%v: %d %s", req, res.Code, res.Body)
		}
	}
	after := snapshot()
	for i := range after {
		if !bytes.Equal(after[i], before[i]) {
			t.Fatalf("file %d changed by a refused write", i)
		}
	}
	for _, name := range []string{"made-by-zero", "dir-by-zero"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			t.Fatalf("%s created under a zero-birth record", name)
		}
	}
	if got := recordOf(t, x, "zero").Root; got == nil || got.Ino != rec.Root.Ino || got.Birth != 0 {
		t.Fatalf("record rebound: %+v", got)
	}
}

// A record without an identity (made before identities were kept) grants
// nothing and is not filled in; a new managed checkout is the way back.
func TestManagedWorktreeWithoutIdentityIsNotAdopted(t *testing.T) {
	x := newWorktreeFixture(t)
	x.checkout(t, "old", "feature")
	setRoot(t, x, "old", nil)
	recordsFile := filepath.Join(x.y.Path, ".yesman", "worktrees.json")
	before, err := os.ReadFile(recordsFile)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(x.y.Path, "old", "feature")
	content, _ := os.ReadFile(target)
	res := postFile(x.f, x.w.Token, map[string]any{"op": "write", "path": target, "content": "LEGACY"})
	if res.Code == 200 || !strings.Contains(res.Body.String(), "yesman_checkout") {
		t.Fatalf("legacy record: %d %s", res.Code, res.Body)
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, content) {
		t.Fatal("legacy checkout changed")
	}
	if after, _ := os.ReadFile(recordsFile); !bytes.Equal(before, after) {
		t.Fatal("the records were changed by a refused write")
	}
	if recordOf(t, x, "old").Root != nil {
		t.Fatal("identity adopted from the current directory")
	}
	x.checkout(t, "fresh", "other")
	if code := x.write(filepath.Join(x.y.Path, "fresh", "feature"), "fresh\n"); code != 200 {
		t.Fatalf("new managed checkout: %d", code)
	}
}
