package yesmen

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestHireNamedPersistsDisplayNameOnFirstCreation(t *testing.T) {
	base := t.TempDir()
	s := New(filepath.Join(base, "yesmen"), filepath.Join(base, "archive"))
	for _, tc := range []struct{ label, name string }{
		{"  GizMoth  ", "gizmoth"},
		{"checkout-v2", "checkout-v2"},
		{"项目 Alpha 团队", "alpha"},
		{"中文项目", "yesman"},
		{"<b>Giz Moth</b>", "b-giz-moth-b"},
		{"../../Outside", "outside"},
		{"CON", "yesman-con"},
		{"LPT1", "yesman-lpt1"},
		{strings.Repeat("大写LongName", 90), "longname-longname-longname-longn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			y, err := s.HireNamed(tc.label, "synthetic/model:high")
			if err != nil {
				t.Fatal(err)
			}
			if y.Name != tc.name || y.Label() != strings.TrimSpace(tc.label) || y.DisplayName != y.Label() || y.Model != "synthetic/model:high" || y.HiredAt.IsZero() {
				t.Fatalf("created %+v", y)
			}
			if err := ValidName(y.Name); err != nil || y.Path != filepath.Join(s.root, y.Name) {
				t.Fatalf("unsafe identity: %+v %v", y, err)
			}
			fresh := New(s.root, s.archive)
			if got, err := fresh.Get(y.Name); err != nil || got != y {
				t.Fatalf("reload: %+v, %v", got, err)
			}
			var meta map[string]any
			if err := readJSON(filepath.Join(y.MetaDir(), "yesman.json"), &meta); err != nil || meta["displayName"] != y.DisplayName || meta["model"] != y.Model {
				t.Fatalf("initial metadata: %v %v", meta, err)
			}
			for _, path := range []string{filepath.Join(y.KnowledgeDir(), "INDEX.md"), y.TaskLogPath(), y.SessionDir("")} {
				if _, err := os.Stat(path); err != nil {
					t.Fatal(err)
				}
			}
			if entries, _ := os.ReadDir(y.Path); len(entries) != 1 || entries[0].Name() != ".yesman" {
				t.Fatalf("staging leftovers: %v", entries)
			}
		})
	}
}

func TestHireKeepsExactInternalNameContract(t *testing.T) {
	base := t.TempDir()
	s := New(filepath.Join(base, "yesmen"), filepath.Join(base, "archive"))
	y, err := s.HireAs("gizmoth", "old/model")
	if err != nil || y.Name != "gizmoth" || y.DisplayName != "" || y.Label() != "gizmoth" || y.Model != "old/model" {
		t.Fatalf("legacy hire: %+v, %v", y, err)
	}
	for _, name := range []string{"GizMoth", "has spaces", "../escape", "", strings.Repeat("a", 33), "gizmoth"} {
		if _, err := s.Hire(name); err == nil {
			t.Fatalf("legacy Hire accepted %q", name)
		}
	}
	got, err := s.HireNamed("GizMoth", "new/model")
	if err != nil || got.Name != "gizmoth-2" || got.Label() != "GizMoth" {
		t.Fatalf("display-name collision: %+v %v", got, err)
	}
	if original, err := s.Get(y.Name); err != nil || original != y {
		t.Fatalf("old identity changed: %+v %v", original, err)
	}
}

func TestHireNamedConcurrentStoresNeverOverwrite(t *testing.T) {
	base := t.TempDir()
	root, archive := filepath.Join(base, "yesmen"), filepath.Join(base, "archive")
	s := New(root, archive)
	old, err := s.Hire("gizmoth")
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(old.Path, "existing-session")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(old.MetaDir(), "yesman.json"))
	const count = 24
	results := make(chan Yesman, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			// Each independent store has a different mutex, like two app instances.
			store := New(root, archive)
			label := []string{"GizMoth", "gizmoth", "GIZMOTH"}[i%3]
			y, err := store.HireNamed(label, "synthetic/model")
			if err != nil {
				t.Error(err)
				return
			}
			if y.Label() != label {
				t.Errorf("label changed: %+v", y)
			}
			results <- y
		})
	}
	// A fresh reader must only observe complete metadata, never a half hire.
	stop := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		reader := New(root, archive)
		for {
			select {
			case <-stop:
				return
			default:
			}
			listed, err := reader.List()
			if err != nil {
				t.Error(err)
				return
			}
			for _, y := range listed {
				if y.Name != old.Name && (y.DisplayName == "" || y.Model != "synthetic/model" || y.HiredAt.IsZero()) {
					t.Errorf("published incomplete hire: %+v", y)
				}
			}
		}
	}()
	wg.Wait()
	close(stop)
	<-readDone
	close(results)
	names := map[string]bool{old.Name: true}
	for y := range results {
		if names[y.Name] {
			t.Fatalf("reused identity: %s", y.Name)
		}
		names[y.Name] = true
		if got, err := s.Get(y.Name); err != nil || got != y {
			t.Fatalf("reload: %+v %v", got, err)
		}
	}
	if len(names) != count+1 {
		t.Fatalf("created %d workspaces", len(names)-1)
	}
	after, _ := os.ReadFile(filepath.Join(old.MetaDir(), "yesman.json"))
	if string(before) != string(after) {
		t.Fatal("existing metadata overwritten")
	}
	if data, _ := os.ReadFile(sentinel); string(data) != "untouched" {
		t.Fatal("existing workspace overwritten")
	}
}

func TestHireNamedValidationAndFailureLeaveNoPartialWorkspace(t *testing.T) {
	base := t.TempDir()
	s := New(filepath.Join(base, "yesmen"), filepath.Join(base, "archive"))
	for _, label := range []string{"", " \t\n", "a\nb", "a\x00b", "a\u0085b", string([]byte{0xff})} {
		if _, err := s.HireNamed(label, "model"); err == nil {
			t.Fatalf("accepted %q", label)
		}
	}
	if _, err := os.Stat(s.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid input touched disk: %v", err)
	}
	// Model a failed final publication after the exclusive reservation. A file
	// at .yesman blocks rename on every OS, even when tests run as root.
	y := Yesman{Name: "gizmoth", DisplayName: "GizMoth", Path: filepath.Join(s.root, "gizmoth")}
	if err := os.MkdirAll(y.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(y.MetaDir(), []byte("block publication"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := initializeHire(y); err == nil {
		t.Fatal("publication failure ignored")
	}
	if _, err := os.Stat(y.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed initialization left a reservation: %v", err)
	}
	if listed, err := s.List(); err != nil || len(listed) != 0 {
		t.Fatalf("failed hire was listed: %v %v", listed, err)
	}
	if got, err := s.HireNamed("GizMoth", "model"); err != nil || got.Name != y.Name {
		t.Fatalf("retry: %+v %v", got, err)
	}
}

func TestHireNamedPreservesOccupiedPaths(t *testing.T) {
	for _, kind := range []string{"directory", "file", "dangling-link"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			s := New(base, filepath.Join(t.TempDir(), "archive"))
			path := filepath.Join(base, "gizmoth")
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(path, []byte("unrelated"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "dangling-link":
				if err := os.Symlink(filepath.Join(base, "missing"), path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Hire("gizmoth"); err == nil {
				t.Fatal("legacy hire overwrote occupied path")
			}
			got, err := s.HireNamed("GizMoth", "model")
			if err != nil || got.Name != "gizmoth-2" {
				t.Fatalf("allocate: %+v %v", got, err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("occupied path changed: %v", err)
			}
		})
	}
}

func TestHireNamedLongCollisionIDsStayValid(t *testing.T) {
	base := t.TempDir()
	s := New(base, filepath.Join(t.TempDir(), "archive"))
	label := strings.Repeat("A", 80)
	for n := 1; n <= 12; n++ {
		y, err := s.HireNamed(label, "")
		if err != nil || ValidName(y.Name) != nil || y.Label() != label {
			t.Fatalf("collision %d: %+v %v", n, y, err)
		}
		if n > 1 && !strings.HasSuffix(y.Name, fmt.Sprintf("-%d", n)) {
			t.Fatalf("unexpected suffix: %s", y.Name)
		}
		data, err := os.ReadFile(filepath.Join(y.MetaDir(), "yesman.json"))
		if err != nil || !json.Valid(data) {
			t.Fatalf("metadata: %v", err)
		}
	}
}
