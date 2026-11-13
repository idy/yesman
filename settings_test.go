package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
)

func TestSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if s := loadSettings(path); s.Appearance != "" {
		t.Fatalf("a missing file gave %+v", s)
	}
	if err := (Settings{Appearance: mygo.ThemeDark, AutoFetch: 5}).save(path); err != nil {
		t.Fatal(err)
	}
	if s := loadSettings(path); s.Appearance != mygo.ThemeDark || s.AutoFetch != 5 {
		t.Errorf("read back %+v", s)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("save left %d files behind", len(entries))
	}
}

func TestReleaseVersion(t *testing.T) {
	for v, want := range map[string]string{
		"":        "",
		"(devel)": "",
		"v0.0.0-20261112231006-83e70753a38c+dirty": "",
		"v0.2.1-0.20261112231006-83e70753a38c":     "",
		"v0.2.0":                                   "v0.2.0",
		"v0.2.0+dirty":                             "v0.2.0",
		"v1.0.0-rc.1":                              "v1.0.0-rc.1",
	} {
		if got := releaseVersion(v); got != want {
			t.Errorf("releaseVersion(%q) = %q, want %q", v, got, want)
		}
	}
}

// A damaged file or an unknown value starts the app with the defaults.
func TestSettingsIgnoresBadFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	for _, content := range []string{"{not json", `{"appearance":"sepia"}`, `{"autoFetch":7}`, `{"autoFetch":-1}`} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if s := loadSettings(path); s.Appearance != "" || s.AutoFetch != 0 {
			t.Errorf("%s gave %+v", content, s)
		}
	}
}
