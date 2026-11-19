package yesmen

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRunWorkspaceBindsRealDirectoryAndResumesByName(t *testing.T) {
	profile, project := t.TempDir(), t.TempDir()
	store := New(filepath.Join(profile, "yesmen"), filepath.Join(profile, "archive"))
	if err := os.Mkdir(store.root, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(project, "existing.txt")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	bound, y, err := store.RunWorkspace("trial", project, "provider/only:max", true)
	if err != nil {
		t.Fatal(err)
	}
	if !y.TaskRoot || y.Name != "trial" || y.Model != "provider/only:max" || bound.TaskRootInfo() == nil {
		t.Fatalf("bound: %+v", y)
	}
	if _, err := bound.Get("other"); err == nil {
		t.Fatal("scoped Store exposed a sibling")
	}
	if ys, err := store.List(); err != nil || len(ys) != 0 {
		t.Fatalf("desktop exposed an external root: %v %v", ys, err)
	}
	if _, err := bound.Archive(y.Name, nil); err == nil {
		t.Fatal("archived a user-owned directory")
	}
	for _, tc := range []struct {
		name, dir string
		create    bool
	}{{"trial", project, true}, {"other", project, true}, {"other", project, false}} {
		if _, _, err := store.RunWorkspace(tc.name, tc.dir, "", tc.create); err == nil {
			t.Fatalf("accepted collision %+v", tc)
		}
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "original" {
		t.Fatalf("modified project file: %s %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(store.root, "other")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed attachment reserved a name: %v", err)
	}
	again, resumed, err := New(store.root, store.archive).RunWorkspace("trial", "", "", false)
	if err != nil || resumed != y {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	if _, err := again.Handoffs(y.Name); err != nil {
		t.Fatalf("handoff access with a different physical basename: %v", err)
	}
	if _, err := again.Topics(y.Name, y.HiredAt); err != nil {
		t.Fatalf("topic access: %v", err)
	}
	if _, err := store.Hire("trial"); err == nil {
		t.Fatal("desktop reused the reserved name")
	}
	if err := os.WriteFile(filepath.Join(y.MetaDir(), "yesman.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RunWorkspace("trial", "", "", false); err == nil {
		t.Fatal("resumed a replaced generation")
	}
}

func TestRunWorkspaceRefusesLinksAndOverlappingData(t *testing.T) {
	profile := t.TempDir()
	store := New(filepath.Join(profile, "yesmen"), filepath.Join(profile, "archive"))
	if err := os.Mkdir(store.root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{profile, store.root, filepath.Dir(profile)} {
		if _, _, err := store.RunWorkspace("trial", root, "", true); err == nil {
			t.Fatalf("accepted overlapping data directory %s", root)
		}
	}
	project, alias := t.TempDir(), filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(project, alias); err != nil {
		t.Skip(err)
	}
	if _, _, err := store.RunWorkspace("linked", alias, "", true); err == nil {
		t.Fatal("attached a directory link")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(project, ".yesman")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RunWorkspace("linked", project, "", true); err == nil {
		t.Fatal("replaced a metadata link")
	}
	if _, err := os.Readlink(filepath.Join(project, ".yesman")); err != nil {
		t.Fatal(err)
	}
}
