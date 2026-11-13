package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/idy/yesman/internal/home"
)

func TestStorageMoveAndRemember(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config", "storage.json")
	source := filepath.Join(base, "old")
	destination := filepath.Join(base, "new")
	h, loc, err := loadStorage(context.Background(), path, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Settings(), []byte(`{"appearance":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	destination, err = h.CheckDestination(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := loc.schedule(h, destination); err != nil {
		t.Fatal(err)
	}
	if h.Root != source {
		t.Fatal("scheduling switched the live data")
	}
	h, loc, err = loadStorage(context.Background(), path, source)
	if err != nil {
		t.Fatal(err)
	}
	if h.Root != destination || loc.config.Previous != source || loc.config.Pending != "" || loc.config.Error != "" {
		t.Fatalf("after restart: home=%s config=%+v", h.Root, loc.config)
	}
	for _, root := range []string{source, destination} {
		data, err := os.ReadFile(filepath.Join(root, "settings.json"))
		if err != nil || string(data) != `{"appearance":"dark"}` {
			t.Fatalf("settings in %s: %q, %v", root, data, err)
		}
	}
	h, _, err = loadStorage(context.Background(), path, source)
	if err != nil || h.Root != destination {
		t.Fatalf("next launch forgot the new home: %+v, %v", h, err)
	}
}

func TestStorageFailedMoveKeepsSource(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config", "storage.json")
	h := home.Home{Root: filepath.Join(base, "old")}
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(base, "occupied")
	cfg := storageConfig{Home: h.Root, Pending: dest}
	if err := cfg.save(path); err != nil {
		t.Fatal(err)
	}
	// The destination can become occupied between confirmation and restart.
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "keep"), []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, loc, err := loadStorage(context.Background(), path, h.Root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != h.Root || loc.config.Pending != "" || loc.config.Error == "" {
		t.Fatalf("failed move: home=%s config=%+v", got.Root, loc.config)
	}
	data, err := os.ReadFile(filepath.Join(dest, "keep"))
	if err != nil || string(data) != "untouched" {
		t.Fatalf("destination was modified: %q, %v", data, err)
	}
	_, again, err := loadStorage(context.Background(), path, h.Root)
	if err != nil || again.config.Error == "" {
		t.Fatalf("failure not remembered: %+v, %v", again, err)
	}
}

func TestStorageRejectsUnsafeSchedule(t *testing.T) {
	base := t.TempDir()
	h := home.Home{Root: filepath.Join(base, "old")}
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "config", "storage.json")
	for _, destination := range []string{h.Root, filepath.Join(h.Root, "nested"), base, "relative"} {
		loc := &storageLocation{path: path, config: storageConfig{Home: h.Root}}
		if err := loc.schedule(h, destination); err == nil {
			t.Errorf("accepted %s", destination)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("invalid schedule wrote configuration: %v", err)
		}
	}
	loc := &storageLocation{path: path, override: true, config: storageConfig{Home: h.Root}}
	if err := loc.schedule(h, filepath.Join(base, "new")); err == nil {
		t.Fatal("accepted a change under YESMAN_HOME")
	}
	loc.override = false
	if err := loc.schedule(h, filepath.Join(base, "new")); err != nil {
		t.Fatal(err)
	}
	if err := loc.schedule(h, filepath.Join(base, "second")); err == nil {
		t.Fatal("replaced an already scheduled move")
	}
}

func TestStorageEnvironmentOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YESMAN_HOME", root)
	h, loc, err := openStorage(context.Background())
	if err != nil || h.Root != root || !loc.override || loc.path != "" {
		t.Fatalf("environment override: home=%+v location=%+v error=%v", h, loc, err)
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
		if _, _, err := loadStorage(context.Background(), path, filepath.Join(base, "default")); err == nil {
			t.Errorf("silently accepted %s", data)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != data {
			t.Fatalf("rewrote a broken configuration: %s, %v", got, err)
		}
	}
}
