package schedule

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// snapshot reads every file under dir, for comparing before and after.
func snapshotFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data, _ := os.ReadFile(path)
			out[path] = string(data)
		}
		return nil
	})
	return out
}

func sameFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// A link anywhere on the way to the scheduler's files, .yesman, schedules
// or runs, pointing out of the workspace or into it, is neither read nor
// written through: Tasks says why, nothing runs, saving is refused, and
// what the link points to is left exactly as it was.
func TestLinkedFoldersAreNotFollowed(t *testing.T) {
	for _, c := range []struct {
		name   string
		link   func(e *env) string // the folder to replace by a link
		inside bool                // the link points into the workspace
	}{
		{"schedules", func(e *env) string { return filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules") }, false},
		{"schedules inside", func(e *env) string { return filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules") }, true},
		{"runs", func(e *env) string { return filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules", "runs") }, false},
		{".yesman", func(e *env) string { return filepath.Join(e.root, "yesmen", ws, ".yesman") }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
			e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
			e.clock.Add(time.Minute)
			e.tick()
			e.waitPrompts(1)
			r := e.run("s1-20261116t0900")
			e.close()
			dir := c.link(e)
			target := filepath.Join(e.root, "external")
			if c.inside {
				target = filepath.Join(e.root, "yesmen", ws, "linked-history")
			}
			if err := os.Rename(dir, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, dir); err != nil {
				t.Skip("no symbolic links here")
			}
			before := snapshotFiles(t, target)
			e.restart("")
			v := e.view()
			if v.Error == "" {
				t.Fatalf("read through the link: %+v", v)
			}
			if c.name != ".yesman" {
				// The board is the store's: with .yesman linked it is gone too.
				e.setTask(r.Board, yesmen.TaskDone)
			}
			e.clock.Add(24 * time.Hour)
			e.tick()
			if _, err := e.svc.Save(ws, PlanInput{Title: "new", Instructions: "x", Schedule: daily("10:00"), Timezone: "UTC"}); err == nil {
				t.Fatal("saved through the link")
			}
			if _, err := e.svc.RunNow(ws, "s1", e.requestID(), ""); err == nil {
				t.Fatal("ran through the link")
			}
			e.svc.Wait()
			if after := snapshotFiles(t, target); !sameFiles(before, after) {
				t.Fatalf("the linked folder changed")
			}
			if n := len(e.prompts()); n != 1 {
				t.Fatalf("%d prompts", n)
			}
		})
	}
}

// A run record replaced by a link reads as damaged and is never written
// through, whatever its board task does.
func TestLinkedRecordIsNotFollowed(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Add(time.Minute)
	e.tick()
	e.waitPrompts(1)
	id := "s1-20261116t0900"
	r := e.run(id)
	e.close()
	outside := filepath.Join(e.root, "outside.json")
	data, _ := os.ReadFile(e.runPath(id))
	if err := os.WriteFile(outside, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(e.runPath(id))
	if err := os.Symlink(outside, e.runPath(id)); err != nil {
		t.Skip("no symbolic links here")
	}
	e.restart("")
	if got := e.run(id); got.State != StateDamaged {
		t.Fatalf("a linked record was read: %+v", got)
	}
	e.setTask(r.Board, yesmen.TaskDone)
	e.tick()
	if _, err := e.svc.Check(ws, id); err != nil {
		t.Logf("check: %v", err)
	}
	if after, _ := os.ReadFile(outside); string(after) != string(data) {
		t.Fatal("written through a linked record")
	}
	if info, err := os.Lstat(e.runPath(id)); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v", err)
	}
}

// Once a linked folder is put back as it was, the history reads again: the
// runs are not taken as gone in the meantime, so nothing runs twice.
func TestHistoryReadsAgainOnceTheLinkIsGone(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Add(time.Minute)
	e.tick()
	e.waitPrompts(1)
	runs := filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules", "runs")
	moved := filepath.Join(e.root, "moved-runs")
	if err := os.Rename(runs, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, runs); err != nil {
		t.Skip("no symbolic links here")
	}
	e.restart("")
	e.tick()
	if v := e.view(); v.Error == "" {
		t.Fatal("read through the link")
	}
	if err := os.Remove(runs); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, runs); err != nil {
		t.Fatal(err)
	}
	e.tick()
	e.tick()
	if v := e.view(); v.Error != "" {
		t.Fatalf("still unread: %s", v.Error)
	}
	if r := e.run("s1-20261116t0900"); r.Kind != RunScheduled || len(r.Attempts) != 1 {
		t.Fatalf("the run: %+v", r)
	}
	if n := len(e.prompts()); n != 1 {
		t.Fatalf("%d prompts", n)
	}
}
