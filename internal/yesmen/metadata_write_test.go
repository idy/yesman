package yesmen

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMetadataWriterBudgetHireRollback(t *testing.T) {
	for _, entry := range []string{"HireNamed", "HireAs"} {
		t.Run(entry, func(t *testing.T) {
			base := t.TempDir()
			s := New(filepath.Join(base, "yesmen"), filepath.Join(base, "archive"))
			var err error
			var retryName string
			if entry == "HireNamed" {
				// The raw label fits the read cap, but JSON escaping does not.
				_, err = s.HireNamed(strings.Repeat("<", maxYesmanMetadataBytes/6+1), "synthetic/model")
				retryName = "yesman"
			} else {
				_, err = s.HireAs("alice", strings.Repeat("m", maxYesmanMetadataBytes))
				retryName = "alice"
			}
			if !errors.Is(err, errYesmanMetadataTooLarge) {
				t.Fatalf("oversized writer returned %v", err)
			}
			entries, err := os.ReadDir(s.root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed hire left a reservation or staging files: %v %v", entries, err)
			}
			fresh, err := s.HireAs(retryName, "synthetic/small")
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Get(retryName)
			if err != nil || got != fresh || got.HiredAt.IsZero() {
				t.Fatalf("retry did not round-trip: %v", err)
			}
		})
	}
}

func TestMetadataWriterBudgetRenameExactAndOverflow(t *testing.T) {
	s, y := metadataStore(t)
	path := filepath.Join(y.MetaDir(), "yesman.json")
	var meta map[string]json.RawMessage
	if err := readJSON(path, &meta); err != nil {
		t.Fatal(err)
	}
	meta["future"] = json.RawMessage(`{"large":9007199254740993,"exponent":1e400}`)
	meta["displayName"] = json.RawMessage(`""`)
	emptyLabel, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(path, meta); err != nil {
		t.Fatal(err)
	}
	label := strings.Repeat("a", maxYesmanMetadataBytes-len(emptyLabel)-1)
	want, err := s.Rename("alice", label)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil || len(before) != maxYesmanMetadataBytes {
		t.Fatalf("exact encoded budget not exercised: size=%d err=%v", len(before), err)
	}
	got, err := s.Get("alice")
	if err != nil || got != want || !got.HiredAt.Equal(y.HiredAt) {
		t.Fatalf("successful exact-limit Rename failed roundtrip: %v", err)
	}
	if _, err := s.Rename("alice", label+"a"); !errors.Is(err, errYesmanMetadataTooLarge) {
		t.Fatalf("encoded limit+1 Rename returned %v", err)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed Rename changed source bytes: %v", err)
	}
	if _, err := os.Lstat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected output touched the temp path: %v", err)
	}
	if got, err := New(s.root, s.archive).Get("alice"); err != nil || got != want {
		t.Fatalf("failed Rename changed cold identity: %v", err)
	}
	if err := readJSON(path, &meta); err != nil {
		t.Fatal(err)
	}
	var future bytes.Buffer
	if err := json.Compact(&future, meta["future"]); err != nil || future.String() != `{"large":9007199254740993,"exponent":1e400}` {
		t.Fatalf("unknown raw fields changed: %s %v", future.String(), err)
	}
}

func TestMetadataWriterBudgetRenamePreservesCompactUnknown(t *testing.T) {
	s, y := metadataStore(t)
	path := filepath.Join(y.MetaDir(), "yesman.json")
	hired, _ := json.Marshal(y.HiredAt)
	prefix := []byte(`{"hiredAt":` + string(hired) + `,"model":"` + y.Model + `","future":{"large":9007199254740993,"exponent":1e400,"padding":"`)
	suffix := []byte(`"}}`)
	before := append(prefix, bytes.Repeat([]byte("p"), maxYesmanMetadataBytes-8-len(prefix)-len(suffix))...)
	before = append(before, suffix...)
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rename("alice", "Ordinary name"); !errors.Is(err, errYesmanMetadataTooLarge) {
		t.Fatalf("indentation/label expanded beyond budget without error: %v", err)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected Rename changed unknown fields/source bytes: %v", err)
	}
	if got, err := s.Get("alice"); err != nil || got != y {
		t.Fatalf("rejected Rename changed identity: %v", err)
	}
}

func TestMetadataHistoricalOversizeIsExplicitAndRepairable(t *testing.T) {
	s, y := metadataStore(t)
	path := filepath.Join(y.MetaDir(), "yesman.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oversize := append(append([]byte{}, original...), bytes.Repeat([]byte(" "), maxYesmanMetadataBytes+1-len(original))...)
	if err := os.WriteFile(path, oversize, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("alice")
	if !errors.Is(err, errYesmanMetadataTooLarge) || got != (Yesman{}) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "reduce its encoded size") {
		t.Fatalf("oversized historical identity silently fell back: err=%v", err)
	}
	if list, err := s.List(); !errors.Is(err, errYesmanMetadataTooLarge) || list != nil {
		t.Fatalf("List silently hid or changed oversized identity: len=%d err=%v", len(list), err)
	}
	called := false
	if err := s.Use("alice", func(Yesman) error { called = true; return nil }); !errors.Is(err, errYesmanMetadataTooLarge) || called {
		t.Fatalf("Use entered with unsupported identity: called=%v err=%v", called, err)
	}
	if _, err := s.Rename("alice", "New label"); !errors.Is(err, errYesmanMetadataTooLarge) {
		t.Fatalf("Rename ignored historical budget error: %v", err)
	}
	if _, err := s.Archive("alice", func(Yesman) error { called = true; return nil }); !errors.Is(err, errYesmanMetadataTooLarge) || called {
		t.Fatalf("Archive used unsupported identity: called=%v err=%v", called, err)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, oversize) {
		t.Fatalf("operations rewrote historical metadata: %v", err)
	}
	// Only this synthetic fixture performs the documented offline repair.
	// Removing excess JSON whitespace preserves the original HiredAt exactly.
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get("alice"); err != nil || got != y {
		t.Fatalf("repair did not recover original generation: %v", err)
	}
}
