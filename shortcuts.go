package main

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Settings › Shortcuts lets the user change the keys of the window's
// actions. The page knows the actions and their default keys
// (web/shortcuts.js); settings.json keeps only the keys the user chose
// instead, by action ID, which this file checks and keeps.
//
// A shortcut is written portably, as "Mod+Shift+K": Mod is ⌘ on macOS and
// Ctrl on Windows and Linux; Ctrl is a Mac's Control key, Alt its Option
// key, Meta the Command, Windows or Super key; then one key. Whether a
// shortcut suits a platform (reserved keys, conflicts) is the page's to say:
// a file carried between computers keeps what another platform could use.

// Shortcuts maps an action's ID to the keys the user chose for it; an
// empty list takes its keys away. An action left out has its defaults.
type Shortcuts map[string][]string

const (
	maxShortcutActions  = 512 // actions with keys of their own
	maxShortcutBindings = 4   // keys of one action
	maxShortcutLength   = 64  // bytes of one written shortcut, or of an action ID
)

var shortcutActionRE = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*$`)

// UnmarshalJSON reads the shortcuts of settings.json, skipping an entry it
// cannot read rather than losing the other settings with it.
func (s *Shortcuts) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Print("settings: shortcuts: ", err)
		*s = nil
		return nil
	}
	out := Shortcuts{}
	for id, value := range raw {
		var list []string
		if err := json.Unmarshal(value, &list); err != nil {
			log.Printf("settings: shortcuts: %s: %v", id, err)
			continue
		}
		if list == nil {
			list = []string{}
		}
		out[id] = list
	}
	*s = out
	return nil
}

// clone copies the shortcuts, so a change to the copy leaves them alone.
func (s Shortcuts) clone() Shortcuts {
	if s == nil {
		return nil
	}
	out := make(Shortcuts, len(s))
	for id, list := range s {
		out[id] = slices.Clone(list)
	}
	return out
}

var shortcutModifiers = map[string]string{
	"mod": "Mod", "cmdorctrl": "Mod", "commandorcontrol": "Mod",
	"ctrl": "Ctrl", "control": "Ctrl",
	"alt": "Alt", "option": "Alt", "opt": "Alt",
	"shift": "Shift",
	"meta":  "Meta", "cmd": "Meta", "command": "Meta",
}

var shortcutModifierOrder = []string{"Mod", "Ctrl", "Alt", "Shift", "Meta"}

var shortcutNamedKeys = func() map[string]string {
	m := map[string]string{
		"esc": "Escape", "return": "Enter", "up": "ArrowUp", "down": "ArrowDown", "left": "ArrowLeft", "right": "ArrowRight",
		"del": "Delete", "ins": "Insert", "pgup": "PageUp", "pgdn": "PageDown", "spacebar": "Space", "plus": "+",
	}
	for _, k := range []string{"Enter", "Space", "Tab", "Escape", "Backspace", "Delete", "Insert", "Home", "End",
		"PageUp", "PageDown", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"} {
		m[strings.ToLower(k)] = k
	}
	for i := 1; i <= 24; i++ {
		m[fmt.Sprintf("f%d", i)] = fmt.Sprintf("F%d", i)
	}
	return m
}()

// shortcutKey is the key a shortcut names, as it is kept: a letter upper
// case, a digit, a symbol, or a named key.
func shortcutKey(name string) (string, bool) {
	if utf8.RuneCountInString(name) == 1 {
		r, _ := utf8.DecodeRuneInString(name)
		switch {
		case r == ' ':
			return "Space", true
		case r == utf8.RuneError || !unicode.IsPrint(r):
			return "", false
		case unicode.IsLetter(r):
			return string(unicode.ToUpper(r)), true
		}
		return name, true
	}
	k, ok := shortcutNamedKeys[strings.ToLower(name)]
	return k, ok
}

// isShortcutSymbol says whether a key is a symbol, whose character says
// whether Shift was held, so Shift is not written with it.
func isShortcutSymbol(key string) bool {
	if utf8.RuneCountInString(key) != 1 {
		return false
	}
	r, _ := utf8.DecodeRuneInString(key)
	return !unicode.IsLetter(r) && (r < '0' || r > '9')
}

// parseShortcut reads a shortcut into its modifiers, in the order they are
// written, and its key.
func parseShortcut(text string) (mods []string, key string, err error) {
	if text == "" || len(text) > maxShortcutLength || !utf8.ValidString(text) {
		return nil, "", fmt.Errorf("%q is not a shortcut", text)
	}
	parts := strings.Split(text, "+")
	if text == "+" {
		parts = []string{"+"}
	} else if strings.HasSuffix(text, "++") {
		parts = append(parts[:len(parts)-2], "+")
	}
	key, ok := shortcutKey(parts[len(parts)-1])
	if !ok {
		return nil, "", fmt.Errorf("%q has no key yesman knows", text)
	}
	held := map[string]bool{}
	for _, p := range parts[:len(parts)-1] {
		m, ok := shortcutModifiers[strings.ToLower(p)]
		if !ok || held[m] {
			return nil, "", fmt.Errorf("%q is not a shortcut", text)
		}
		held[m] = true
	}
	if isShortcutSymbol(key) {
		delete(held, "Shift")
	}
	for _, m := range shortcutModifierOrder {
		if held[m] {
			mods = append(mods, m)
		}
	}
	return mods, key, nil
}

// canonicalShortcut writes a shortcut the one way it is kept.
func canonicalShortcut(text string) (string, error) {
	mods, key, err := parseShortcut(text)
	if err != nil {
		return "", err
	}
	return strings.Join(append(mods, key), "+"), nil
}

// checkShortcutBindings checks an action's keys and writes them as they are
// kept.
func checkShortcutBindings(id string, list []string) ([]string, error) {
	if len(id) > maxShortcutLength || !shortcutActionRE.MatchString(id) {
		return nil, fmt.Errorf("%q is not an action", id)
	}
	if len(list) > maxShortcutBindings {
		return nil, fmt.Errorf("%s has %d shortcuts; an action has at most %d", id, len(list), maxShortcutBindings)
	}
	out := make([]string, 0, len(list))
	for _, b := range list {
		c, err := canonicalShortcut(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		if slices.Contains(out, c) {
			return nil, fmt.Errorf("%s has %s twice", id, c)
		}
		out = append(out, c)
	}
	return out, nil
}

// changeShortcuts applies changes to shortcuts: an action's new keys, an
// empty list for none, or nil for its defaults. It changes all or nothing.
func changeShortcuts(current Shortcuts, changes map[string]*[]string) (Shortcuts, error) {
	next := current.clone()
	if next == nil {
		next = Shortcuts{}
	}
	for _, id := range slices.Sorted(maps.Keys(changes)) {
		list := changes[id]
		if list == nil {
			if len(id) > maxShortcutLength || !shortcutActionRE.MatchString(id) {
				return nil, fmt.Errorf("%q is not an action", id)
			}
			delete(next, id)
			continue
		}
		checked, err := checkShortcutBindings(id, *list)
		if err != nil {
			return nil, err
		}
		next[id] = checked
	}
	if len(next) > maxShortcutActions {
		return nil, fmt.Errorf("at most %d actions can have keys of their own", maxShortcutActions)
	}
	if len(next) == 0 {
		return nil, nil
	}
	return next, nil
}

// checkShortcuts checks shortcuts read from a configuration file.
func checkShortcuts(in map[string][]string) (Shortcuts, error) {
	if len(in) > maxShortcutActions {
		return nil, fmt.Errorf("at most %d actions can have keys of their own", maxShortcutActions)
	}
	out := Shortcuts{}
	for _, id := range slices.Sorted(maps.Keys(in)) {
		checked, err := checkShortcutBindings(id, in[id])
		if err != nil {
			return nil, err
		}
		out[id] = checked
	}
	return out, nil
}

// changedShortcuts lists the actions whose keys differ between two sets.
func changedShortcuts(a, b Shortcuts) []string {
	changed := []string{}
	for id := range a {
		if other, ok := b[id]; !ok || !slices.Equal(a[id], other) {
			changed = append(changed, id)
		}
	}
	for id := range b {
		if _, ok := a[id]; !ok {
			changed = append(changed, id)
		}
	}
	slices.Sort(changed)
	return changed
}

// shortcutAccelerator writes a shortcut as a native menu's accelerator, or
// "" for none.
func shortcutAccelerator(binding string) string {
	mods, key, err := parseShortcut(binding)
	if err != nil {
		return ""
	}
	names := map[string]string{"Mod": "CmdOrCtrl", "Ctrl": "Ctrl", "Alt": "Alt", "Shift": "Shift", "Meta": "Super"}
	parts := make([]string, 0, len(mods)+1)
	for _, m := range mods {
		parts = append(parts, names[m])
	}
	switch key {
	case "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight":
		key = strings.TrimPrefix(key, "Arrow")
	case "+":
		key = "Plus"
	}
	return strings.Join(append(parts, key), "+")
}

// Shortcuts returns the keys the user chose instead of the defaults.
func (s *service) Shortcuts() map[string][]string {
	a := s.app
	a.setmu.Lock()
	defer a.setmu.Unlock()
	return shortcutsView(a.settings.Shortcuts)
}

func shortcutsView(s Shortcuts) map[string][]string {
	out := map[string][]string(s.clone())
	if out == nil {
		out = map[string][]string{}
	}
	return out
}

// SetShortcuts changes actions' keys together: each action to its new
// keys, to none with an empty list, or back to its defaults with null.
func (s *service) SetShortcuts(changes map[string]*[]string) (map[string][]string, error) {
	a := s.app
	a.setmu.Lock()
	defer a.setmu.Unlock()
	next, err := changeShortcuts(a.settings.Shortcuts, changes)
	if err != nil {
		return nil, err
	}
	settings := a.settings
	settings.Shortcuts = next
	if err := a.putSettingsLocked(settings); err != nil {
		return nil, err
	}
	return shortcutsView(next), nil
}

// ResetShortcuts gives every action its default keys again.
func (s *service) ResetShortcuts() (map[string][]string, error) {
	a := s.app
	a.setmu.Lock()
	defer a.setmu.Unlock()
	settings := a.settings
	settings.Shortcuts = nil
	if err := a.putSettingsLocked(settings); err != nil {
		return nil, err
	}
	return map[string][]string{}, nil
}
