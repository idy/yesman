package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/home"
)

func TestStorageSelectsExistingDataWithoutCopying(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config", "storage.json")
	source := filepath.Join(base, "old")
	destination := filepath.Join(base, "existing")
	current, loc, err := loadStorage(path, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current.Settings(), []byte(`{"appearance":"light"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(destination, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"appearance":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loc.setHome(destination); err != nil {
		t.Fatal(err)
	}
	if current.Root != source {
		t.Fatal("changed the running instance's home")
	}
	next, _, err := loadStorage(path, source)
	want, _ := filepath.EvalSymlinks(destination)
	if err != nil || next.Root != want {
		t.Fatalf("next launch: %+v, %v", next, err)
	}
	if got := loadSettings(settings); got.Appearance != "dark" {
		t.Fatalf("overwrote the destination's settings: %+v", got)
	}
	if got := loadSettings(current.Settings()); got.Appearance != "light" {
		t.Fatalf("modified the old folder: %+v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 1 || fields["home"] != want {
		t.Fatalf("unexpected selector: %s, %v", data, err)
	}
}

func TestStorageCreatesNewFolderAndRejectsInvalidSelection(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config", "storage.json")
	loc := &storageLocation{path: path}
	destination := filepath.Join(base, "new", "data")
	if err := loc.setHome(destination); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	for _, invalid := range []string{"", "relative", path} {
		if err := loc.setHome(invalid); err == nil {
			t.Errorf("accepted %q", invalid)
		}
		data, _ := os.ReadFile(path)
		if string(data) != string(original) {
			t.Fatal("an invalid selection replaced the remembered folder")
		}
	}
	for _, dir := range []string{"repos", "yesmen", "archive", ".runtime"} {
		if info, err := os.Stat(filepath.Join(destination, dir)); err != nil || !info.IsDir() {
			t.Fatalf("missing %s: %v", dir, err)
		}
	}
}

func TestStorageRejectsReadOnlyDataDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows chmod does not restrict directory write access")
	}
	for _, name := range []string{"root", "repos", "yesmen", "archive", ".runtime"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			path := filepath.Join(base, "config", "storage.json")
			loc := &storageLocation{path: path}
			if err := loc.setHome(filepath.Join(base, "current")); err != nil {
				t.Fatal(err)
			}
			before := loc.config
			selector, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			target := home.Home{Root: filepath.Join(base, "selected")}
			if err := target.Ensure(); err != nil {
				t.Fatal(err)
			}
			locked := target.Root
			if name != "root" {
				locked = filepath.Join(target.Root, name)
			}
			if err := os.Chmod(locked, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
			if probe, err := os.CreateTemp(locked, ".permission-check-*"); err == nil {
				probe.Close()
				os.Remove(probe.Name())
				t.Skip("the current user can write through read-only directory permissions")
			}
			if err := loc.setHome(target.Root); err == nil || !strings.Contains(err.Error(), "not writable") {
				t.Fatalf("accepted a read-only %s: %v", name, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(selector) || loc.config != before {
				t.Fatalf("a failed permission check changed the selected folder: %s, %v", after, err)
			}
			for _, dir := range []string{target.Root, target.Repos(), target.Yesmen(), target.Archive(), target.Runtime()} {
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".write-check-") {
						t.Errorf("permission probe left behind in %s", dir)
					}
				}
			}
		})
	}
}

func TestStorageEnvironmentOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YESMAN_HOME", root)
	h, loc, err := openStorage()
	if err != nil || h.Root != root || !loc.override || loc.path != "" {
		t.Fatalf("environment override: home=%+v location=%+v error=%v", h, loc, err)
	}
	if err := loc.setHome(filepath.Join(root, "other")); err == nil {
		t.Fatal("replaced YESMAN_HOME")
	}
}

func TestStorageDoesNotReplaceUnavailableOrBrokenConfiguration(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "storage.json")
	for _, data := range []string{
		`{broken`,
		`{"home":"relative"}`,
		`{"home":"` + filepath.ToSlash(filepath.Join(base, "missing")) + `"}`,
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadStorage(path, filepath.Join(base, "default")); err == nil {
			t.Errorf("silently accepted %s", data)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != data {
			t.Fatalf("rewrote a broken configuration: %s, %v", got, err)
		}
	}
}
