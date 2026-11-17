package tasklog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinBirthIdentityEvidence(t *testing.T) {
	for _, tc := range []struct {
		birth string
		valid bool
	}{
		{"", false},
		{"0:0", false},
		{"0:1", true}, // A supplied subsecond birth time is still evidence.
		{"123:0", true},
	} {
		i := Identity{Platform: "darwin", Volume: "1", ID: "2", Birth: tc.birth}
		if got := i.valid(); got != tc.valid {
			t.Errorf("birth %q valid=%v, want %v", tc.birth, got, tc.valid)
		}
	}
}

func TestMarkerRefusesMissingDarwinBirthBeforeRecovery(t *testing.T) {
	for _, birth := range []string{"0:0", "123:0"} {
		t.Run(birth, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.Mkdir(filepath.Join(workspace, ".yesman"), 0o700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			m := Manifest{Version: 2, SHA256: strings.Repeat("a", 64), MigrationID: strings.Repeat("b", 32), SourceIdentity: Identity{Platform: "darwin", Volume: "1", ID: "2", Birth: birth}}
			marker := manifestBytes(m)
			if err := os.WriteFile(filepath.Join(workspace, Marker), marker, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, TOC), []byte("original source sentinel\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = loadMarker(root)
			if (err == nil) != (birth != "0:0") {
				t.Fatalf("birth %q: marker admission error=%v", birth, err)
			}
			got, readErr := os.ReadFile(filepath.Join(workspace, Marker))
			if readErr != nil || !bytes.Equal(got, marker) {
				t.Fatalf("marker changed on admission: %v", readErr)
			}
			got, readErr = os.ReadFile(filepath.Join(workspace, TOC))
			if readErr != nil || string(got) != "original source sentinel\n" {
				t.Fatalf("source changed on admission: %v", readErr)
			}
			entries, err := os.ReadDir(filepath.Join(workspace, ".yesman"))
			if err != nil || len(entries) != 2 {
				t.Fatalf("admission created recovery artifacts: %v %v", entries, err)
			}
		})
	}
}
