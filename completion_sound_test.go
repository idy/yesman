package main

import (
	"os"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/chime"
	"github.com/idy/yesman/internal/home"
)

func TestCompletionPreference(t *testing.T) {
	h := home.Home{Root: t.TempDir()}
	a := &app{home: h, settings: loadSettings(h.Settings())}
	s := &service{app: a}
	if got := s.Settings().CompletionSound; got.Enabled || got.Sound != chime.Bell {
		t.Fatalf("unexpected default: %+v", got)
	}
	if _, err := s.SetCompletionSound(CompletionSoundPatch{Sound: new(chime.Double)}); err != nil {
		t.Fatal(err)
	}
	wanted := CompletionSoundPreference{Enabled: true, Sound: chime.Double}
	if got, err := s.SetCompletionSound(CompletionSoundPatch{Enabled: new(true)}); err != nil || got.CompletionSoundPreference != wanted || got.Revision != 2 {
		t.Fatalf("enable patch: %+v %v", got, err)
	}
	if got := loadSettings(h.Settings()).CompletionSound; got != wanted {
		t.Fatalf("did not persist: %+v", got)
	}
	if _, err := s.SetCompletionSound(CompletionSoundPatch{Sound: new(chime.Sound("missing"))}); err == nil {
		t.Fatal("saved a stale sound ID")
	}
	if s.Settings().CompletionSound.CompletionSoundPreference != wanted {
		t.Fatal("invalid setting changed the active preference")
	}

	if err := os.WriteFile(h.Settings(), []byte(`{"completionSound":{"enabled":true,"sound":"future-tone"},"future":123}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.settings = loadSettings(h.Settings())
	if got := s.Settings().CompletionSound; !got.Enabled || got.Sound != chime.Bell {
		t.Fatalf("bad fallback: %+v", got)
	}
	if err := s.SetAppearance("dark"); err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(h.Settings())
	if err != nil || !strings.Contains(string(file), `"future-tone"`) || !strings.Contains(string(file), `"future": 123`) {
		t.Fatalf("unrelated save lost a setting: %v %s", err, file)
	}
	if _, err := s.SetCompletionSound(CompletionSoundPatch{Enabled: new(false)}); err != nil {
		t.Fatal(err)
	}
	if file, err := os.ReadFile(h.Settings()); err != nil || !strings.Contains(string(file), `"future-tone"`) {
		t.Fatalf("switch patch rewrote an unrelated stored choice: %v %s", err, file)
	}
	if _, err := s.SetCompletionSound(CompletionSoundPatch{Sound: new(chime.Pluck)}); err != nil {
		t.Fatal(err)
	}
	wanted = CompletionSoundPreference{Sound: chime.Pluck}
	cold := &service{app: &app{home: h, settings: loadSettings(h.Settings())}}
	if cold.Settings().CompletionSound.CompletionSoundPreference != wanted {
		t.Fatal("turning off lost the user's selection")
	}
}

func TestCompletionPreferenceSaveFailure(t *testing.T) {
	a := &app{home: home.Home{Root: t.TempDir() + "/missing"}}
	s := &service{app: a}
	if _, err := s.SetCompletionSound(CompletionSoundPatch{Enabled: new(true)}); err == nil {
		t.Fatal("saved a setting in an absent directory")
	}
	if got := s.Settings().CompletionSound; got.Enabled || got.Sound != chime.Bell {
		t.Fatalf("failed save changed active preference: %+v", got)
	}
}
