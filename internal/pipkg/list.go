package pipkg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Installed is a package pi's user settings list.
type Installed struct {
	Source
	// Filtered says the settings load only some of its resources.
	Filtered bool `json:"filtered,omitempty"`
	// Path is where it is installed; empty until pi has installed it.
	Path string `json:"path,omitempty"`
	// From its package.json, when it has one.
	Title       string   `json:"title"`             // its package name, or what the source names
	Current     string   `json:"current,omitempty"` // the installed version
	Description string   `json:"description,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
	Provides    []string `json:"provides,omitempty"`
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// StripANSI drops terminal colour codes from pi's output.
func StripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// ParseList reads the user packages from what `pi list` prints:
//
//	User packages:
//	  npm:pi-web-access
//	    /home/me/.pi/agent/npm/node_modules/pi-web-access
//	  git:github.com/me/tools (filtered)
//
//	Project packages:
//	  …
//
// Project packages, which belong to the folder pi ran in, are left out.
func ParseList(out string) []Installed {
	list := []Installed{}
	user := false
	for _, line := range strings.Split(StripANSI(strings.ReplaceAll(out, "\r\n", "\n")), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case !strings.HasPrefix(line, " "):
			// A heading: only the user section is ours.
			user = trimmed == "User packages:"
		case !user:
		case strings.HasPrefix(line, "    "):
			if len(list) > 0 && list[len(list)-1].Path == "" {
				list[len(list)-1].Path = trimmed
			}
		default:
			raw, filtered := strings.CutSuffix(trimmed, " (filtered)")
			src, err := ParseSource(raw)
			if err != nil {
				// pi knows sources this does not; keep it as pi wrote it.
				src = Source{Raw: raw, Kind: "local"}
				if strings.Contains(raw, ":") {
					src.Kind = "git"
				}
			}
			list = append(list, Installed{Source: src, Filtered: filtered})
		}
	}
	for i := range list {
		list[i].inspect()
	}
	return list
}

// inspect reads the installed package's package.json for its name,
// version and what it provides.
func (p *Installed) inspect() {
	p.Title = p.Name
	if p.Title == "" {
		p.Title = strings.TrimSuffix(filepath.Base(strings.TrimRight(p.Raw, `/\`)), ".git")
		if i := strings.LastIndex(p.Title, "@"); i > 0 {
			p.Title = p.Title[:i] // a git ref
		}
	}
	if p.Path == "" {
		return
	}
	info, err := os.Stat(p.Path)
	if err != nil {
		return
	}
	if !info.IsDir() {
		// A file is one extension.
		p.Provides = []string{"extensions"}
		return
	}
	var m manifest
	if data, err := os.ReadFile(filepath.Join(p.Path, "package.json")); err == nil && json.Unmarshal(data, &m) == nil {
		if m.Name != "" {
			p.Title = m.Name
		}
		p.Current, p.Description, p.Homepage = m.Version, m.Description, webURL(m.Homepage)
		if p.Homepage == "" {
			p.Homepage = repoURL(m.repository())
		}
		if m.Pi.declared() {
			p.Provides = m.Pi.provides()
			return
		}
	}
	// Without a manifest, pi looks in the conventional folders.
	for _, kind := range []string{"extensions", "skills", "prompts", "themes"} {
		if info, err := os.Stat(filepath.Join(p.Path, kind)); err == nil && info.IsDir() {
			p.Provides = append(p.Provides, kind)
		}
	}
}
