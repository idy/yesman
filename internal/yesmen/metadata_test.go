package yesmen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func metadataStore(t *testing.T) (*Store, Yesman) {
	t.Helper()
	base := t.TempDir()
	s := New(filepath.Join(base, "yesmen"), filepath.Join(base, "archive"))
	y, err := s.HireAs("alice", "synthetic/model:high")
	if err != nil {
		t.Fatal(err)
	}
	return s, y
}

func TestMetadataReadCompatibility(t *testing.T) {
	hired := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	for _, tc := range []struct {
		name, data, label, model string
		hired                    time.Time
	}{
		{"normal", `{"hiredAt":"2026-01-02T03:04:05.000000006Z","displayName":"项目 Alice","model":"synthetic/model","future":9007199254740993}`, "项目 Alice", "synthetic/model", hired},
		{"legacy", `{}`, "alice", "", time.Time{}},
		{"missing", "", "alice", "", time.Time{}},
		{"syntax error", `{"displayName":"must not appear",`, "alice", "", time.Time{}},
		{"null", `null`, "alice", "", time.Time{}},
		// json.Unmarshal keeps valid fields even when a different field has
		// a type error. Get has historically ignored that error.
		{"type error", `{"hiredAt":"2026-01-02T03:04:05.000000006Z","model":42,"displayName":"Alice"}`, "Alice", "", hired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, original := metadataStore(t)
			path := filepath.Join(original.MetaDir(), "yesman.json")
			if tc.name == "missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			check := func(y Yesman) {
				t.Helper()
				if y.Name != original.Name || y.Path != original.Path || y.Label() != tc.label || y.Model != tc.model || !y.HiredAt.Equal(tc.hired) {
					t.Fatalf("metadata = %+v, want label %q, model %q, hired %s", y, tc.label, tc.model, tc.hired)
				}
			}
			y, err := s.Get("alice")
			if err != nil {
				t.Fatal(err)
			}
			check(y)
			list, err := s.List()
			if err != nil || len(list) != 1 {
				t.Fatalf("List = %v, %v", list, err)
			}
			check(list[0])
			if err := s.Use("alice", func(y Yesman) error { check(y); return nil }); err != nil {
				t.Fatal(err)
			}
			if tc.name == "missing" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("read recreated missing metadata: %v", err)
				}
			} else if after, err := os.ReadFile(path); err != nil || string(after) != tc.data {
				t.Fatalf("read changed metadata: %v", err)
			}
		})
	}
}

func TestMetadataReadSizeAndType(t *testing.T) {
	for _, size := range []int{maxYesmanMetadataBytes, maxYesmanMetadataBytes + 1} {
		t.Run(stringSize(size), func(t *testing.T) {
			s, y := metadataStore(t)
			path := filepath.Join(y.MetaDir(), "yesman.json")
			prefix := []byte(`{"displayName":"Alice","hiredAt":"2026-01-02T03:04:05Z","future":9007199254740993}`)
			data := append(prefix, bytes.Repeat([]byte(" "), size-len(prefix))...)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			var decoded map[string]json.RawMessage
			err := readYesmanMetadata(path, &decoded)
			got, getErr := s.Get("alice")
			if getErr != nil {
				t.Fatal(getErr)
			}
			if size == maxYesmanMetadataBytes {
				if err != nil || got.Label() != "Alice" || got.HiredAt.IsZero() || string(decoded["future"]) != "9007199254740993" {
					t.Fatalf("exact limit: %+v %v, %s", got, err, decoded["future"])
				}
			} else {
				if err == nil || decoded != nil || !got.HiredAt.IsZero() || got.DisplayName != "" {
					t.Fatalf("oversized metadata decoded: %+v, %v", got, err)
				}
				if _, err := s.Rename("alice", "New label"); err == nil {
					t.Fatal("Rename accepted oversized source metadata")
				}
			}
			if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, data) {
				t.Fatalf("metadata bytes changed: %v", err)
			}
		})
	}
	s, y := metadataStore(t)
	path := filepath.Join(y.MetaDir(), "yesman.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := readYesmanMetadata(path, &decoded); err == nil || decoded != nil {
		t.Fatalf("directory accepted: %v", err)
	}
	if got, err := s.Get("alice"); err != nil || got.DisplayName != "" || !got.HiredAt.IsZero() {
		t.Fatalf("directory fallback = %+v %v", got, err)
	}
}

func stringSize(size int) string {
	if size == maxYesmanMetadataBytes {
		return "exactly 1 MiB"
	}
	return "1 MiB plus one"
}

func TestMetadataReadCoordinatesRenameAndLifecycle(t *testing.T) {
	s, initial := metadataStore(t)
	path := filepath.Join(initial.MetaDir(), "yesman.json")
	var meta map[string]json.RawMessage
	if err := readJSON(path, &meta); err != nil {
		t.Fatal(err)
	}
	meta["future"] = json.RawMessage(`{"large":9007199254740993,"exponent":1e400}`)
	if err := writeJSON(path, meta); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rename("alice", "First label"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 20 {
			if _, err := s.Rename("alice", "Next label"); err != nil {
				t.Error(err)
				return
			}
		}
	})
	for range 2 {
		wg.Go(func() {
			for range 20 {
				got, err := s.Get("alice")
				if err != nil || !got.HiredAt.Equal(initial.HiredAt) || got.Model != initial.Model || got.DisplayName == "" {
					t.Errorf("incomplete metadata during replacement: %+v %v", got, err)
					return
				}
			}
		})
	}
	wg.Wait()
	if err := readJSON(path, &meta); err != nil || string(meta["future"]) != "{\n    \"large\": 9007199254740993,\n    \"exponent\": 1e400\n  }" {
		t.Fatalf("unknown metadata lost: %s %v", meta["future"], err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := s.Archive("alice", func(y Yesman) error {
		if !y.HiredAt.Equal(initial.HiredAt) || y.DisplayName != "Next label" {
			t.Errorf("Archive got wrong identity: %+v", y)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(filepath.Join(archive, ".yesman", "yesman.json")); err != nil || !bytes.Equal(after, before) {
		t.Fatalf("Archive changed metadata: %v", err)
	}
	if _, err := s.Get("alice"); err == nil {
		t.Fatal("archived workspace still visible")
	}
	fresh, err := s.Hire("alice")
	if err != nil || fresh.HiredAt.Equal(initial.HiredAt) {
		t.Fatalf("same-name rehire reused identity: %+v %v", fresh, err)
	}
	if err := s.Use("alice", func(y Yesman) error {
		if y != fresh {
			t.Errorf("Use got stale metadata: %+v, want %+v", y, fresh)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
