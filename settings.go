package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"github.com/idy/yesman/internal/crew"
)

// Settings are the user's choices in the Settings pages, kept in
// settings.json under yesman's home.
type Settings struct {
	// Appearance is light or dark, or system to follow the desktop.
	Appearance mygo.ThemeSource `json:"appearance,omitempty"`
	// AutoFetch is how many minutes apart the pool's repositories are all
	// fetched; 0 fetches them only when asked or checked out.
	AutoFetch int `json:"autoFetch,omitempty"`
	// Crew matches pi's models to Artificial Analysis's, and chooses the
	// crew and what new agents start as.
	Crew crew.Config `json:"crew,omitzero"`
}

// autoFetchChoices are the intervals the Repositories page offers, in
// minutes.
var autoFetchChoices = []int{0, 1, 5, 15, 60}

func validAutoFetch(minutes int) bool {
	for _, m := range autoFetchChoices {
		if m == minutes {
			return true
		}
	}
	return false
}

func validAppearance(t mygo.ThemeSource) bool {
	switch t {
	case mygo.ThemeSystem, mygo.ThemeLight, mygo.ThemeDark:
		return true
	}
	return false
}

// loadSettings reads the settings. A missing or unreadable file gives the
// defaults, so a damaged file never keeps the app from starting.
func loadSettings(path string) Settings {
	var s Settings
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Print("settings: ", err)
		}
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil {
		log.Print("settings: ", err)
		return Settings{}
	}
	if s.Appearance != "" && !validAppearance(s.Appearance) {
		s.Appearance = ""
	}
	if !validAutoFetch(s.AutoFetch) {
		s.AutoFetch = 0
	}
	return s
}

// save replaces the settings file whole, so a crash leaves the old one.
func (s Settings) save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // after a successful rename, nothing is left to remove
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// SettingsView is what the General, Repositories and Storage settings
// pages show; the pi page has its own, PiView.
type SettingsView struct {
	Appearance    mygo.ThemeSource `json:"appearance"`
	Language      string           `json:"language"`  // a language of languages, or "system"
	AutoFetch     int              `json:"autoFetch"` // minutes; 0 is off
	Home          string           `json:"home"`
	Pool          string           `json:"pool"`
	Yesmen        string           `json:"yesmen"`
	Archive       string           `json:"archive"`
	Runtime       string           `json:"runtime"`
	SettingsFile  string           `json:"settingsFile"`
	Secrets       string           `json:"secrets,omitempty"`   // the folder of the secrets and their key
	Variables     string           `json:"variables,omitempty"` // the variables' file
	StorageConfig string           `json:"storageConfig,omitempty"`
	HomeOverride  bool             `json:"homeOverride,omitempty"`
}

// Settings returns the user's choices and where yesman keeps its data.
func (s *service) Settings() SettingsView {
	a := s.app
	a.setmu.Lock()
	appearance, autoFetch := a.settings.Appearance, a.settings.AutoFetch
	a.setmu.Unlock()
	if appearance == "" {
		appearance = mygo.ThemeSystem
	}
	language := languageSystem
	if a.chosenLocale != nil {
		language = chosenLanguage(a.chosenLocale())
	}
	v := SettingsView{
		Appearance:   appearance,
		Language:     language,
		AutoFetch:    autoFetch,
		Home:         a.home.Root,
		Pool:         a.home.Repos(),
		Yesmen:       a.home.Yesmen(),
		Archive:      a.home.Archive(),
		Runtime:      a.home.Runtime(),
		SettingsFile: a.home.Settings(),
	}
	if a.vault != nil {
		v.Secrets = a.vault.Dir()
	}
	if a.variables != nil {
		v.Variables = a.variables.File()
	}
	if a.storage != nil {
		v.HomeOverride = a.storage.override
		v.StorageConfig = a.storage.path
	}
	return v
}

// SetAppearance keeps yesman light or dark, or has it follow the desktop,
// and remembers the choice for the next start.
func (s *service) SetAppearance(source string) error {
	t := mygo.ThemeSource(source)
	if !validAppearance(t) {
		return fmt.Errorf("unknown appearance %q", source)
	}
	a := s.app
	a.setmu.Lock()
	defer a.setmu.Unlock()
	next := a.settings
	next.Appearance = t
	return a.putSettingsLocked(next)
}

// SetLanguage shows yesman in a language it is written in, or "system" to
// follow the system's languages. MyGo keeps the choice for the next starts
// (App.SetLocale), and tells the page, which draws itself again.
func (s *service) SetLanguage(choice string) error {
	tag, ok := languageTag(choice)
	if !ok {
		return fmt.Errorf("unknown language %q", choice)
	}
	if s.app.setLocale != nil {
		s.app.setLocale(tag)
	}
	return nil
}

// SetAutoFetch sets how many minutes apart the pool's repositories are
// all fetched, 0 for never, and remembers it for the next start.
func (s *service) SetAutoFetch(minutes int) error {
	if !validAutoFetch(minutes) {
		return fmt.Errorf("fetching every %d minutes is not offered", minutes)
	}
	a := s.app
	a.setmu.Lock()
	defer a.setmu.Unlock()
	next := a.settings
	next.AutoFetch = minutes
	return a.putSettingsLocked(next)
}

// themeOf is the appearance settings put into effect: none chosen follows
// the system.
func themeOf(s Settings) mygo.ThemeSource {
	if s.Appearance == "" {
		return mygo.ThemeSystem
	}
	return s.Appearance
}

// putSettingsLocked saves the settings and puts them into effect. a.setmu
// is held.
func (a *app) putSettingsLocked(next Settings) error {
	if err := next.save(a.home.Settings()); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	prev := a.settings
	a.settings = next
	if themeOf(next) != themeOf(prev) && a.setTheme != nil {
		a.setTheme(next.Appearance)
	}
	if next.AutoFetch != prev.AutoFetch {
		a.fetcher.SetInterval(time.Duration(next.AutoFetch) * time.Minute)
	}
	return nil
}

// AboutView names the versions yesman runs with.
type AboutView struct {
	// Version is the module version of a released build, empty for a
	// development build, which the revision identifies instead.
	Version  string `json:"version,omitempty"`
	Revision string `json:"revision,omitempty"`
	Time     string `json:"time,omitempty"` // the revision's commit time, RFC 3339
	Modified bool   `json:"modified,omitempty"`
	Go       string `json:"go"`
	MyGo     string `json:"mygo,omitempty"`
	Magpie   string `json:"magpie,omitempty"`
	Pi       string `json:"pi,omitempty"`
	PiError  string `json:"piError,omitempty"`
}

// releaseVersion is a tagged module version, or "" for a development build:
// "(devel)", or the pseudo-version go build stamps from an untagged commit,
// such as v0.0.0-20261112231006-83e70753a38c+dirty.
func releaseVersion(v string) string {
	v = strings.TrimSuffix(v, "+dirty")
	if v == "" || v == "(devel)" || strings.HasPrefix(v, "v0.0.0-") || strings.Count(v, "-") >= 2 {
		return ""
	}
	return v
}

// About reports yesman's build and the versions of what it runs on.
func (s *service) About(ctx context.Context) AboutView {
	v := AboutView{Go: runtime.Version()}
	if bi, ok := debug.ReadBuildInfo(); ok {
		v.Version = releaseVersion(bi.Main.Version)
		for _, st := range bi.Settings {
			switch st.Key {
			case "vcs.revision":
				v.Revision = st.Value
			case "vcs.time":
				v.Time = st.Value
			case "vcs.modified":
				v.Modified = st.Value == "true"
			}
		}
		for _, d := range bi.Deps {
			if d.Path == "github.com/egoist/mygo" {
				v.MyGo = d.Version
				if d.Replace != nil && d.Replace.Version != "" {
					v.MyGo = d.Replace.Version
				}
			}
		}
	}
	if !mygo.IsDev() {
		v.Version = mygo.App.Version() // mygo build stamps mygo.json's version
	}
	if data, err := fs.ReadFile(webFS, "web/mds/VERSION"); err == nil {
		if f := strings.Fields(string(data)); len(f) > 0 {
			v.Magpie = f[0]
		}
	}
	a := s.app
	if a.piErr != nil {
		v.PiError = a.piErr.Error()
		return v
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, a.pi, "--version").Output()
	if err != nil {
		v.PiError = "pi --version failed: " + err.Error()
	} else {
		v.Pi = strings.TrimSpace(string(out))
	}
	return v
}
