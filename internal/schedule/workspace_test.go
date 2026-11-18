package schedule

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/yesmen"
)

// A workspace that is itself a link, to a folder out of the store or to one
// in it under another name, is neither read nor written through: Tasks says
// why, nothing runs, saving is refused, and what the link points to is left
// exactly as it was. Once the workspace is a folder again, it reads again.
func TestLinkedWorkspaceIsNotFollowed(t *testing.T) {
	for _, c := range []struct {
		name   string
		target func(e *env) string
	}{
		{"out of the store", func(e *env) string { return filepath.Join(e.root, "outside-workspace") }},
		{"in the store", func(e *env) string { return filepath.Join(e.root, "yesmen", ".alice-real") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
			p := e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
			e.close()
			workspace := filepath.Join(e.root, "yesmen", ws)
			target := c.target(e)
			if err := os.Rename(workspace, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, workspace); err != nil {
				t.Skip("no symbolic links here")
			}
			before := snapshotFiles(t, target)
			e.restart("")
			if v := e.view(); v.Error == "" || len(v.Plans) != 0 {
				t.Fatalf("read through the linked workspace: %+v", v)
			}
			e.clock.Add(time.Minute)
			e.tick()
			if _, err := e.svc.Save(ws, PlanInput{ID: p.ID, Revision: p.Revision, Title: "Written through the link", Instructions: "x", Schedule: daily("09:00"), Timezone: "UTC"}); err == nil {
				t.Fatal("saved through the linked workspace")
			}
			if _, err := e.svc.Save(ws, PlanInput{Title: "new", Instructions: "x", Schedule: daily("10:00"), Timezone: "UTC"}); err == nil {
				t.Fatal("created through the linked workspace")
			}
			if _, err := e.svc.RunNow(ws, p.ID, e.requestID(), ""); err == nil {
				t.Fatal("ran through the linked workspace")
			}
			e.svc.Wait()
			if after := snapshotFiles(t, target); !sameFiles(before, after) {
				t.Fatal("the linked workspace changed")
			}
			if n := len(e.prompts()); n != 0 {
				t.Fatalf("%d prompts", n)
			}
			// Put back as a folder, it is read again, as it was.
			e.close()
			if err := os.Remove(workspace); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(target, workspace); err != nil {
				t.Fatal(err)
			}
			e.restart("")
			v := e.view()
			if v.Error != "" || len(v.Plans) != 1 || v.Plans[0].Revision != p.Revision {
				t.Fatalf("not read again: %+v", v)
			}
		})
	}
}

// The store's own folder may be reached through a link the user chose,
// such as a moved data folder: its workspaces work as usual.
func TestStoreFolderThroughALink(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:59:00Z"))
	e.close()
	alias := filepath.Join(e.root, "alias")
	if err := os.Symlink(filepath.Join(e.root, "yesmen"), alias); err != nil {
		t.Skip("no symbolic links here")
	}
	e.store = yesmen.New(alias, filepath.Join(e.root, "archive"))
	e.open()
	p := e.save(PlanInput{Schedule: daily("09:00"), Enabled: true})
	e.clock.Add(time.Minute)
	e.tick()
	e.waitPrompts(1)
	if v := e.view(); v.Error != "" || len(v.Plans) != 1 || v.Plans[0].ID != p.ID {
		t.Fatalf("view through the store's alias: %+v", v)
	}
	if _, err := os.Stat(filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules", "plans.json")); err != nil {
		t.Fatal(err)
	}
}

// A workspace read before it was dismissed and a same-name one hired is not
// written for the new one, which keeps nothing of the old.
func TestReplacedWorkspaceIsNotWritten(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
	old, err := e.store.Get(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.Archive(ws, func(y yesmen.Yesman) error { e.svc.RetireYesman(y.Name, y.HiredAt); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.Hire(ws); err != nil {
		t.Fatal(err)
	}
	if dir, err := openSchedules(old, true); !errors.Is(err, errReplaced) {
		if dir != nil {
			dir.Close()
		}
		t.Fatalf("opened the new workspace for the dismissed one: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(e.root, "yesmen", ws, ".yesman", "schedules")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the new workspace got a schedules folder: %v", err)
	}
	// The new workspace itself works.
	p := e.save(PlanInput{Schedule: daily("09:00")})
	if v := e.view(); v.Error != "" || len(v.Plans) != 1 || v.Plans[0].ID != p.ID {
		t.Fatalf("new workspace: %+v", v)
	}
}

// A change to plans.json on disk after yesman read it, before a save is
// written, is a conflict, not something saved over: the file stays as it
// was changed, nothing is left behind, and once read again a save keeps it,
// fields this version does not know included.
func TestSaveKeepsAChangeMadeOnDisk(t *testing.T) {
	userEdit := func(t *testing.T, data []byte) []byte {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(data, &obj); err != nil {
			t.Fatal(err)
		}
		obj["userEdit"] = json.RawMessage(`"PRESERVE ME"`)
		out, err := json.MarshalIndent(obj, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, c := range []struct {
		name   string
		change func(t *testing.T, path string) []byte // what is on disk after, nil: no file
	}{
		{"in place", func(t *testing.T, path string) []byte {
			data, _ := os.ReadFile(path)
			edited := userEdit(t, data)
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.Write(edited); err != nil {
				t.Fatal(err)
			}
			return edited
		}},
		{"replaced", func(t *testing.T, path string) []byte {
			data, _ := os.ReadFile(path)
			edited := userEdit(t, data)
			if err := os.Rename(path, path+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, edited, 0o644); err != nil {
				t.Fatal(err)
			}
			return edited
		}},
		{"removed", func(t *testing.T, path string) []byte {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			return nil
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
			p := e.save(PlanInput{Schedule: daily("09:00")})
			y, _ := e.store.Get(ws)
			path := plansPath(y)
			var want []byte
			changed := false
			e.reopen(Config{writePlans: func(string, []byte) error {
				if !changed {
					changed = true
					want = c.change(t, path)
				}
				return nil
			}})
			_, err := e.svc.Save(ws, PlanInput{ID: p.ID, Revision: p.Revision, Title: "Changed in Tasks", Instructions: "I", Schedule: daily("09:00"), Timezone: "UTC"})
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("saved over a change made on disk: %v", err)
			}
			after, rerr := os.ReadFile(path)
			if want == nil && !errors.Is(rerr, os.ErrNotExist) || want != nil && !bytes.Equal(after, want) {
				t.Fatalf("the file changed on disk was not kept as it was: %v\n%s", rerr, after)
			}
			noTemporaryFiles(t, filepath.Dir(path))
			v := e.view()
			if want == nil {
				if v.Error != "" || len(v.Plans) != 0 {
					t.Fatalf("a removed file came back: %+v", v)
				}
				return
			}
			if v.Error != "" || len(v.Plans) != 1 || v.Plans[0].Revision != p.Revision || v.Plans[0].Title == "Changed in Tasks" {
				t.Fatalf("after the conflict: %+v", v)
			}
			saved, err := e.svc.Save(ws, PlanInput{ID: p.ID, Revision: v.Plans[0].Revision, Title: "Changed in Tasks", Instructions: "I", Schedule: daily("09:00"), Timezone: "UTC"})
			if err != nil || saved.Revision != p.Revision+1 {
				t.Fatalf("saving again: %+v %v", saved, err)
			}
			if data, _ := os.ReadFile(path); !bytes.Contains(data, []byte(`"PRESERVE ME"`)) || !bytes.Contains(data, []byte("Changed in Tasks")) {
				t.Fatalf("saving again lost the change made on disk:\n%s", data)
			}
		})
	}
}

// A plans.json made on disk while yesman created the first task, in a
// workspace that had none, is kept too.
func TestCreateKeepsAFileMadeOnDisk(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
	y, _ := e.store.Get(ws)
	path := plansPath(y)
	made := []byte(`{"version": 1, "next": 0, "plans": [], "userEdit": "PRESERVE ME"}`)
	once := false
	e.reopen(Config{writePlans: func(string, []byte) error {
		if !once {
			once = true
			if err := os.WriteFile(path, made, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}})
	in := PlanInput{Title: "T", Instructions: "I", Schedule: daily("09:00"), Timezone: "UTC"}
	if _, err := e.svc.Save(ws, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("created over a file made on disk: %v", err)
	}
	if data, _ := os.ReadFile(path); !bytes.Equal(data, made) {
		t.Fatalf("the file made on disk changed:\n%s", data)
	}
	noTemporaryFiles(t, filepath.Dir(path))
	if _, err := e.svc.Save(ws, in); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); !bytes.Contains(data, []byte(`"PRESERVE ME"`)) {
		t.Fatalf("lost:\n%s", data)
	}
}

// A same-size edit that keeps the file's time, which its stamp does not
// show, is caught by its content: a conflict, then read again.
func TestSaveKeepsAnEditOfTheSameSizeAndTime(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
	p := e.save(PlanInput{Title: "Morning check", Schedule: daily("09:00")})
	e.view() // read, and its stamp kept
	y, _ := e.store.Get(ws)
	path := plansPath(y)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	edited := bytes.Replace(data, []byte("Morning check"), []byte("Evening check"), 1)
	if bytes.Equal(edited, data) {
		t.Fatal("no title to edit")
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(edited, 0); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	in := PlanInput{ID: p.ID, Revision: p.Revision, Title: "Changed in Tasks", Instructions: "I", Schedule: daily("09:00"), Timezone: "UTC"}
	if _, err := e.svc.Save(ws, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("saved over a same-size edit: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, edited) {
		t.Fatal("the edit was not kept")
	}
	if v := e.view(); len(v.Plans) != 1 || v.Plans[0].Title != "Evening check" {
		t.Fatalf("not read again: %+v", v.Plans)
	}
}

// A plans.json with another link, made by the user, is replaced at its
// name, as before: the other link is not written through.
func TestSaveDoesNotWriteThroughAHardLink(t *testing.T) {
	e := newEnv(t, "", utc("2026-11-16T08:00:00Z"))
	p := e.save(PlanInput{Schedule: daily("09:00")})
	y, _ := e.store.Get(ws)
	path := plansPath(y)
	other := filepath.Join(e.root, "backup-plans.json")
	if err := os.Link(path, other); err != nil {
		t.Skip("no hard links here")
	}
	before, _ := os.ReadFile(other)
	if _, err := e.svc.Save(ws, PlanInput{ID: p.ID, Revision: p.Revision, Title: "Changed in Tasks", Instructions: "I", Schedule: daily("09:00"), Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(other); !bytes.Equal(before, after) {
		t.Fatal("written through the other link")
	}
	if data, _ := os.ReadFile(path); !bytes.Contains(data, []byte("Changed in Tasks")) {
		t.Fatal("not saved")
	}
}

func noTemporaryFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range entries {
		if strings.HasSuffix(en.Name(), ".tmp") {
			t.Fatalf("left behind: %s", en.Name())
		}
	}
}
