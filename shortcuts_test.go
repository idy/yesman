package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/egoist/mygo"

	"github.com/idy/yesman/internal/crew"
	"github.com/idy/yesman/internal/home"
)

func TestCanonicalShortcut(t *testing.T) {
	for in, want := range map[string]string{
		"Mod+K":               "Mod+K",
		"mod+k":               "Mod+K",
		"Shift+Mod+k":         "Mod+Shift+K",
		"CmdOrCtrl+Shift+P":   "Mod+Shift+P",
		"Cmd+Option+Down":     "Alt+Meta+ArrowDown",
		"Control+Alt+pgdn":    "Ctrl+Alt+PageDown",
		"Mod+,":               "Mod+,",
		"Mod+Shift+/":         "Mod+/", // a symbol says whether Shift was held
		"Mod++":               "Mod++",
		"Mod+Plus":            "Mod++",
		"Mod+Shift+1":         "Mod+Shift+1",
		"f5":                  "F5",
		"Alt+F24":             "Alt+F24",
		"Mod+ö":               "Mod+Ö",
		"Mod+ß":               "Mod+ß",
		"Mod+Alt+ArrowUp":     "Mod+Alt+ArrowUp",
		"Ctrl+Meta+Mod+Q":     "Mod+Ctrl+Meta+Q",
		"Mod+Space":           "Mod+Space",
		"Mod+ ":               "Mod+Space",
		"Escape":              "Escape", // well formed; the page says which keys a platform takes
		"Mod+Shift+Insert":    "Mod+Shift+Insert",
		"option+shift+PAGEUP": "Alt+Shift+PageUp",
	} {
		got, err := canonicalShortcut(in)
		if err != nil || got != want {
			t.Errorf("canonicalShortcut(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "Mod", "Mod+", "Mod+Mod+K", "Hyper+K", "Mod+F25", "Mod+Fn", "Mod+KK", "Mod+\x01", "Mod+\u200b",
		strings.Repeat("Mod+", 20) + "K", "Mod+\xff"} {
		if got, err := canonicalShortcut(bad); err == nil {
			t.Errorf("canonicalShortcut(%q) = %q, want an error", bad, got)
		}
	}
}

func TestShortcutAccelerator(t *testing.T) {
	for in, want := range map[string]string{
		"Mod+P":             "CmdOrCtrl+P",
		"Mod+Shift+Y":       "CmdOrCtrl+Shift+Y",
		"Mod+Alt+ArrowDown": "CmdOrCtrl+Alt+Down",
		"Ctrl+PageDown":     "Ctrl+PageDown",
		"Mod++":             "CmdOrCtrl+Plus",
		"Meta+K":            "Super+K",
		"F5":                "F5",
		"":                  "",
		"Mod+Nope":          "",
	} {
		if got := shortcutAccelerator(in); got != want {
			t.Errorf("shortcutAccelerator(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChangeShortcuts(t *testing.T) {
	keys := func(k ...string) *[]string { return &k }
	current := Shortcuts{"palette.open": {"Mod+J"}, "agent.stop": {}}
	next, err := changeShortcuts(current, map[string]*[]string{
		"pane.files":   keys("mod+shift+e", "Ctrl+Alt+E"),
		"agent.stop":   nil,    // back to its defaults
		"future.thing": keys(), // an action this yesman may not know is kept
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Shortcuts{"palette.open": {"Mod+J"}, "pane.files": {"Mod+Shift+E", "Ctrl+Alt+E"}, "future.thing": {}}
	if !reflect.DeepEqual(next, want) {
		t.Errorf("changed to %v, want %v", next, want)
	}
	if !reflect.DeepEqual(current, Shortcuts{"palette.open": {"Mod+J"}, "agent.stop": {}}) {
		t.Errorf("the change touched the shortcuts it was given: %v", current)
	}

	// A bad change changes nothing.
	for name, changes := range map[string]map[string]*[]string{
		"bad key":     {"pane.files": keys("Mod+K"), "pane.board": keys("Mod+Nope")},
		"twice":       {"pane.files": keys("Mod+K", "mod+k")},
		"too many":    {"pane.files": keys("Mod+1", "Mod+2", "Mod+3", "Mod+4", "Mod+5")},
		"bad action":  {"Pane Files": keys("Mod+K")},
		"bad reset":   {"../x": nil},
		"long action": {strings.Repeat("a", 65): keys()},
	} {
		if got, err := changeShortcuts(current, changes); err == nil {
			t.Errorf("%s: changed to %v", name, got)
		}
	}
	// Resetting every action leaves none kept.
	if got, err := changeShortcuts(current, map[string]*[]string{"palette.open": nil, "agent.stop": nil}); err != nil || got != nil {
		t.Errorf("resetting all = %v, %v", got, err)
	}
	many := map[string]*[]string{}
	for i := range maxShortcutActions + 1 {
		many[strings.Repeat("a", 1+i%60)+"."+strings.Repeat("b", 1+i/60)] = keys()
	}
	if _, err := changeShortcuts(nil, many); err == nil {
		t.Error("kept more actions than allowed")
	}
}

func shortcutsApp(t *testing.T, settings Settings) (*service, *app) {
	t.Helper()
	h := home.Home{Root: t.TempDir()}
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	a := &app{home: h, settings: settings}
	return &service{app: a}, a
}

func TestSetShortcutsKeepsTheOtherSettings(t *testing.T) {
	keys := func(k ...string) *[]string { return &k }
	crewConfig := crew.Config{Yesman: "magpie/auto/claude-opus-5-5:xhigh"}
	s, a := shortcutsApp(t, Settings{Appearance: mygo.ThemeDark, AutoFetch: 15, Crew: crewConfig})
	if got := s.Shortcuts(); got == nil || len(got) != 0 {
		t.Errorf("no shortcuts = %#v, want an empty map", got)
	}
	got, err := s.SetShortcuts(map[string]*[]string{"palette.open": keys("Mod+J", "Mod+Shift+P"), "agent.stop": keys()})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"palette.open": {"Mod+J", "Mod+Shift+P"}, "agent.stop": {}}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(s.Shortcuts(), want) {
		t.Errorf("set %v, now %v", got, s.Shortcuts())
	}
	saved := loadSettings(a.home.Settings())
	if !reflect.DeepEqual(map[string][]string(saved.Shortcuts), want) || saved.Appearance != mygo.ThemeDark || saved.AutoFetch != 15 || saved.Crew.Yesman != crewConfig.Yesman {
		t.Errorf("saved %+v", saved)
	}

	// Other settings keep the shortcuts when they save.
	if err := s.SetAppearance("light"); err != nil {
		t.Fatal(err)
	}
	if err := a.changeCrew(func(c *crew.Config) error { c.Yesman = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	if saved := loadSettings(a.home.Settings()); !reflect.DeepEqual(map[string][]string(saved.Shortcuts), want) || saved.Appearance != mygo.ThemeLight {
		t.Errorf("after other settings saved: %+v", saved)
	}

	// A failed change leaves what was saved and what the app has alone.
	if _, err := s.SetShortcuts(map[string]*[]string{"palette.open": keys("Mod+Nope")}); err == nil {
		t.Error("saved a bad key")
	}
	if !reflect.DeepEqual(s.Shortcuts(), want) {
		t.Errorf("a failed change left %v", s.Shortcuts())
	}
	// So does one that cannot be written, on every platform: a folder in
	// the settings file's place cannot be replaced by a file.
	settingsPath := a.home.Settings()
	if err := os.Rename(settingsPath, settingsPath+".aside"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(settingsPath, "in the way"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetShortcuts(map[string]*[]string{"palette.open": nil}); err == nil {
		t.Error("saved over a folder")
	} else if !reflect.DeepEqual(s.Shortcuts(), want) {
		t.Errorf("a failed save left %v", s.Shortcuts())
	}
	if _, err := s.ResetShortcuts(); err == nil {
		t.Error("reset over a folder")
	} else if !reflect.DeepEqual(s.Shortcuts(), want) {
		t.Errorf("a failed reset left %v", s.Shortcuts())
	}
	if err := os.RemoveAll(settingsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(settingsPath+".aside", settingsPath); err != nil {
		t.Fatal(err)
	}
	if saved := loadSettings(settingsPath); !reflect.DeepEqual(map[string][]string(saved.Shortcuts), want) {
		t.Errorf("after failed saves the file has %v", saved.Shortcuts)
	}
	// And, where folder permissions say so, a folder it may not write in.
	// Windows does not refuse new files in a folder by its mode bits.
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		if err := os.Chmod(a.home.Root, 0o500); err != nil {
			t.Fatal(err)
		}
		_, err := s.SetShortcuts(map[string]*[]string{"palette.open": nil})
		if err := os.Chmod(a.home.Root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err == nil {
			t.Error("saved into a folder it cannot write")
		} else if !reflect.DeepEqual(s.Shortcuts(), want) {
			t.Errorf("a failed save left %v", s.Shortcuts())
		}
	}

	if got, err := s.ResetShortcuts(); err != nil || len(got) != 0 {
		t.Errorf("reset = %v, %v", got, err)
	}
	if saved := loadSettings(a.home.Settings()); saved.Shortcuts != nil || saved.Appearance != mygo.ThemeLight {
		t.Errorf("after reset: %+v", saved)
	}
	data, _ := os.ReadFile(a.home.Settings())
	if strings.Contains(string(data), "shortcuts") {
		t.Errorf("defaults are written down:\n%s", data)
	}
}

// settings.json keeps what a newer yesman wrote, and an entry it cannot
// read loses only itself.
func TestSettingsKeepUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	content := `{"appearance":"dark","grant":{"camera":"asked"},"shortcuts":{"palette.open":["Mod+J"],"broken":"Mod+K","nothing":null},"zeta":1}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	s := loadSettings(path)
	if s.Appearance != mygo.ThemeDark || !reflect.DeepEqual(s.Shortcuts, Shortcuts{"palette.open": {"Mod+J"}, "nothing": {}}) {
		t.Fatalf("read %+v", s)
	}
	s.AutoFetch = 5
	if err := s.save(path); err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["zeta"] != float64(1) || !reflect.DeepEqual(saved["grant"], map[string]any{"camera": "asked"}) || saved["autoFetch"] != float64(5) {
		t.Errorf("saved %s", data)
	}
	// A shortcuts value of the wrong kind keeps the other settings.
	if err := os.WriteFile(path, []byte(`{"appearance":"dark","shortcuts":["Mod+K"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := loadSettings(path); s.Appearance != mygo.ThemeDark || s.Shortcuts != nil {
		t.Errorf("read %+v", s)
	}
	// Known keys are never doubled by what was kept.
	s = Settings{Appearance: mygo.ThemeLight, extra: map[string]json.RawMessage{"appearance": json.RawMessage(`"dark"`), "later": json.RawMessage(`true`)}}
	data, err := json.Marshal(s)
	if err != nil || string(data) != `{"appearance":"light","later":true}` {
		t.Errorf("marshal = %s, %v", data, err)
	}
}

func TestConfigShortcuts(t *testing.T) {
	current := Settings{Appearance: mygo.ThemeLight, Shortcuts: Shortcuts{"palette.open": {"Mod+J"}, "agent.stop": {}}, extra: map[string]json.RawMessage{"later": json.RawMessage(`1`)}}
	data := formatConfig(current, nil, nil, nil)
	c, err := parseConfig(data)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if c.shortcuts == nil || !reflect.DeepEqual(*c.shortcuts, current.Shortcuts) || len(c.ignored) != 0 {
		t.Fatalf("read back %+v from\n%s", c.shortcuts, data)
	}

	// Default shortcuts export as an empty table, which replacing restores.
	c, err = parseConfig(formatConfig(Settings{}, nil, nil, nil))
	if err != nil || c.shortcuts == nil || len(*c.shortcuts) != 0 {
		t.Fatalf("defaults read back %v, %v", c.shortcuts, err)
	}
	if got := c.settings(current, importMerge).Shortcuts; !reflect.DeepEqual(got, current.Shortcuts) {
		t.Errorf("merging defaults = %v", got)
	}
	if got := c.settings(current, importReplace); got.Shortcuts != nil || got.extra["later"] == nil {
		t.Errorf("replacing with defaults = %+v", got)
	}

	c, err = parseConfig([]byte("version = 2\n[shortcuts]\n\"pane.files\" = [\"mod+shift+e\"]\n\"agent.stop\" = [\"Mod+.\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	merged := c.settings(current, importMerge)
	if want := (Shortcuts{"palette.open": {"Mod+J"}, "agent.stop": {"Mod+."}, "pane.files": {"Mod+Shift+E"}}); !reflect.DeepEqual(merged.Shortcuts, want) {
		t.Errorf("merged %v, want %v", merged.Shortcuts, want)
	}
	replaced := c.settings(current, importReplace)
	if want := (Shortcuts{"agent.stop": {"Mod+."}, "pane.files": {"Mod+Shift+E"}}); !reflect.DeepEqual(replaced.Shortcuts, want) || replaced.Appearance != mygo.ThemeSystem {
		t.Errorf("replaced %+v", replaced)
	}
	if !reflect.DeepEqual(current.Shortcuts, Shortcuts{"palette.open": {"Mod+J"}, "agent.stop": {}}) {
		t.Errorf("importing changed the current shortcuts: %v", current.Shortcuts)
	}
	plan := planImport(c, current, nil, nil, nil)
	if !reflect.DeepEqual(plan.Shortcuts.Merge, []string{"agent.stop", "pane.files"}) || !reflect.DeepEqual(plan.Shortcuts.Replace, []string{"agent.stop", "palette.open", "pane.files"}) {
		t.Errorf("plan = %+v", plan.Shortcuts)
	}

	// A file without shortcuts, as older ones are, leaves them alone.
	c, err = parseConfig([]byte("version = 1\nappearance = \"dark\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []importMode{importMerge, importReplace} {
		if got := c.settings(current, mode).Shortcuts; !reflect.DeepEqual(got, current.Shortcuts) {
			t.Errorf("%s without shortcuts = %v", mode, got)
		}
	}
	if plan := planImport(c, current, nil, nil, nil); len(plan.Shortcuts.Merge)+len(plan.Shortcuts.Replace) != 0 {
		t.Errorf("plan without shortcuts = %+v", plan.Shortcuts)
	}

	for _, bad := range []string{
		"version = 2\n[shortcuts]\n\"pane.files\" = [\"Mod+Nope\"]\n",
		"version = 2\n[shortcuts]\n\"Pane Files\" = [\"Mod+K\"]\n",
		"version = 2\n[shortcuts]\n\"pane.files\" = \"Mod+K\"\n",
	} {
		if _, err := parseConfig([]byte(bad)); err == nil {
			t.Errorf("imported %q", bad)
		}
	}
}

// Merging a file's shortcuts saves them over the current ones, with the
// other settings.
func TestImportShortcuts(t *testing.T) {
	_, a := shortcutsApp(t, Settings{Shortcuts: Shortcuts{"palette.open": {"Mod+J"}}})
	file := filepath.Join(t.TempDir(), "in.toml")
	if err := os.WriteFile(file, []byte("version = 2\nappearance = \"dark\"\n[shortcuts]\n\"agent.stop\" = []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := readConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	a.setmu.Lock()
	err = a.putSettingsLocked(c.settings(a.settings, importMerge))
	a.setmu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	saved := loadSettings(a.home.Settings())
	if !reflect.DeepEqual(saved.Shortcuts, Shortcuts{"palette.open": {"Mod+J"}, "agent.stop": {}}) || saved.Appearance != mygo.ThemeDark {
		t.Errorf("merged and saved %+v", saved)
	}
}
