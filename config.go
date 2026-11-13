package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/egoist/mygo"

	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/pool"
)

// yesman's configuration file carries its settings and its repo pool, as
// TOML, to another computer or into a backup. Workspaces, their knowledge
// and conversations stay out of it, as does where yesman keeps its data.
//
// Importing it merges, adding what is missing and changing the settings it
// sets, or replaces, which also resets the settings it leaves out and
// removes the pool's repositories it does not list, except those that work
// depends on.

// configVersion is the version of the file's format this yesman writes.
const configVersion = 1

// configLimit bounds the file yesman reads.
const configLimit = 1 << 20

// configFile is the file as TOML has it. Pointers tell a key left out from
// one set to its zero value.
type configFile struct {
	Version    int       `toml:"version"`
	Appearance *string   `toml:"appearance"`
	AutoFetch  *int      `toml:"auto_fetch"`
	Repos      *[]string `toml:"repos"`
}

// config is a configuration file read and checked.
type config struct {
	appearance *mygo.ThemeSource
	autoFetch  *int
	repos      []configRepo // in the file's order, each once
	ignored    []string     // keys this yesman does not know
}

// configRepo is a GitHub repository the file lists.
type configRepo struct {
	name  string // owner/repo
	input string // as written, which may name a protocol to clone over
}

// importMode is how an import treats what the file leaves out.
type importMode string

const (
	importMerge   importMode = "merge"   // keeps it
	importReplace importMode = "replace" // resets settings, removes repositories
)

// formatConfig writes the configuration file.
func formatConfig(s Settings, repos []string) []byte {
	appearance := s.Appearance
	if appearance == "" {
		appearance = mygo.ThemeSystem
	}
	var b strings.Builder
	b.WriteString(`# yesman configuration: its settings and the repositories of its repo pool.
# Import it in Settings > Import and export. Merge adds what is missing; Replace
# also removes the repositories this file does not list, unless work depends on them.

version = ` + strconv.Itoa(configVersion) + `

# system, light or dark
appearance = ` + tomlString(string(appearance)) + `

# Minutes between fetching every repository: 1, 5, 15 or 60, or 0 for never.
auto_fetch = ` + strconv.Itoa(s.AutoFetch) + `

# GitHub repositories, as owner/repo.
repos = [`)
	for _, r := range repos {
		b.WriteString("\n  " + tomlString(r) + ",")
	}
	if len(repos) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("]\n")
	return []byte(b.String())
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// parseConfig reads and checks a configuration file. It takes only files
// with a version, so that another TOML file is not imported as an empty
// configuration.
func parseConfig(data []byte) (config, error) {
	var f configFile
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return config{}, fmt.Errorf("this is not a yesman configuration file: %w", err)
	}
	if !md.IsDefined("version") {
		return config{}, errors.New("this is not a yesman configuration file: it has no version")
	}
	if f.Version < 1 {
		return config{}, fmt.Errorf("this is not a yesman configuration file: version %d is unknown", f.Version)
	}
	if f.Version > configVersion {
		return config{}, fmt.Errorf("this file is for a newer yesman (version %d); update yesman to import it", f.Version)
	}
	var c config
	if f.Appearance != nil {
		t := mygo.ThemeSource(*f.Appearance)
		if !validAppearance(t) {
			return config{}, fmt.Errorf("appearance is system, light or dark, not %q", *f.Appearance)
		}
		c.appearance = &t
	}
	if f.AutoFetch != nil {
		if !validAutoFetch(*f.AutoFetch) {
			return config{}, fmt.Errorf("auto_fetch is 0, 1, 5, 15 or 60 minutes, not %d", *f.AutoFetch)
		}
		c.autoFetch = f.AutoFetch
	}
	if f.Repos != nil {
		seen := map[string]bool{}
		for _, in := range *f.Repos {
			r, err := github.Parse(in)
			if err != nil {
				return config{}, fmt.Errorf("repos: %w", err)
			}
			name := r.FullName()
			if key := repoKey(name); !seen[key] {
				seen[key] = true
				c.repos = append(c.repos, configRepo{name: name, input: strings.TrimSpace(in)})
			}
		}
	}
	for _, k := range md.Undecoded() {
		c.ignored = append(c.ignored, k.String())
	}
	return c, nil
}

// readConfig reads a configuration file from disk.
func readConfig(path string) (config, error) {
	f, err := os.Open(path)
	if err != nil {
		return config{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, configLimit+1))
	if err != nil {
		return config{}, err
	}
	if len(data) > configLimit {
		return config{}, errors.New("this file is too large for a yesman configuration file")
	}
	return parseConfig(data)
}

// settings are the settings an import leaves.
func (c config) settings(current Settings, mode importMode) Settings {
	next := current
	if mode == importReplace {
		next = Settings{Appearance: mygo.ThemeSystem}
	}
	if c.appearance != nil {
		next.Appearance = *c.appearance
	}
	if c.autoFetch != nil {
		next.AutoFetch = *c.autoFetch
	}
	return next
}

// repoKey identifies a GitHub repository, which GitHub names in any case.
func repoKey(fullName string) string { return strings.ToLower(fullName) }

// githubName is a pool repository's owner/repo, or "" when it does not
// come from GitHub, as repositories added before yesman took only GitHub.
func githubName(r pool.Repo) string {
	// Parse also takes owner/repo, which a local path such as /src/repo
	// would pass for: a remote URL must name GitHub.
	if !strings.Contains(strings.ToLower(r.URL), "github.com") {
		return ""
	}
	if g, err := github.Parse(r.URL); err == nil {
		return g.FullName()
	}
	return ""
}

// SettingsValues are the settings a configuration file carries.
type SettingsValues struct {
	Appearance mygo.ThemeSource `json:"appearance"`
	AutoFetch  int              `json:"autoFetch"`
}

func settingsValues(s Settings) SettingsValues {
	v := SettingsValues{Appearance: s.Appearance, AutoFetch: s.AutoFetch}
	if v.Appearance == "" {
		v.Appearance = mygo.ThemeSystem
	}
	return v
}

// ImportPlan is what importing a configuration file would do, for the page
// to show before you choose to merge or replace.
type ImportPlan struct {
	Path    string         `json:"path"`
	Current SettingsValues `json:"current"`
	Merge   SettingsValues `json:"merge"`   // the settings after merging
	Replace SettingsValues `json:"replace"` // the settings after replacing
	// Add lists the file's repositories the pool lacks, which both modes
	// clone; Present those it has or is cloning.
	Add     []string `json:"add"`
	Present []string `json:"present"`
	// Extra lists the pool's repositories the file leaves out. Merging
	// keeps them; replacing removes those not in use.
	Extra   []ExtraRepo `json:"extra"`
	Ignored []string    `json:"ignored"` // keys this yesman does not know
}

// ExtraRepo is a pool repository a configuration file leaves out.
type ExtraRepo struct {
	Name  string `json:"name"`  // its pool name, such as github.com/idy/yesman
	Label string `json:"label"` // owner/repo, or the pool name when it is not from GitHub
	// InUse says why replacing keeps it: work depends on it.
	InUse string `json:"inUse,omitempty"`
	// Clone is the state of a repository still on its way into the pool,
	// whose clone replacing stops, or "" for one in the pool.
	Clone CloneState `json:"clone,omitempty"`
}

// agentCloning is why replacing keeps a repository an agent is adding.
const agentCloning = "an agent is cloning it"

// planImport compares a configuration with the pool and the clones under
// way. inUse, when not nil, says why an extra repository must stay.
func planImport(c config, current Settings, repos []pool.Repo, clones []Clone, inUse func(pool.Repo) string) ImportPlan {
	plan := ImportPlan{
		Current: settingsValues(current),
		Merge:   settingsValues(c.settings(current, importMerge)),
		Replace: settingsValues(c.settings(current, importReplace)),
		Add:     []string{}, Present: []string{}, Extra: []ExtraRepo{}, Ignored: []string{},
	}
	plan.Ignored = append(plan.Ignored, c.ignored...)
	have, pooled := map[string]bool{}, map[string]bool{}
	for _, r := range repos {
		if name := githubName(r); name != "" {
			have[repoKey(name)], pooled[repoKey(name)] = true, true
		}
	}
	for _, cl := range clones {
		if cl.State != CloneFailed {
			have[repoKey(cl.Repo)] = true
		}
	}
	listed := map[string]bool{}
	for _, r := range c.repos {
		listed[repoKey(r.name)] = true
		if have[repoKey(r.name)] {
			plan.Present = append(plan.Present, r.name)
		} else {
			plan.Add = append(plan.Add, r.name)
		}
	}
	for _, r := range repos {
		name := githubName(r)
		if name != "" && listed[repoKey(name)] {
			continue
		}
		e := ExtraRepo{Name: r.Name, Label: name}
		if name == "" {
			e.Label = r.Name
		}
		if inUse != nil {
			e.InUse = inUse(r)
		}
		plan.Extra = append(plan.Extra, e)
	}
	// Clones the file leaves out, queued, under way or failed, would add
	// repositories it does not list.
	for _, cl := range clones {
		if key := repoKey(cl.Repo); listed[key] || pooled[key] {
			continue
		}
		e := ExtraRepo{Name: "github.com/" + cl.Repo, Label: cl.Repo, Clone: cl.State}
		if cl.agents > 0 {
			e.InUse = agentCloning
		}
		plan.Extra = append(plan.Extra, e)
	}
	return plan
}

// exportedRepos are the repositories a configuration file lists: the
// pool's GitHub repositories and those on their way into it, sorted. It
// also returns the pool's repositories that are not from GitHub, which
// yesman cannot add again and so leaves out.
func (a *app) exportedRepos() (repos, skipped []string, err error) {
	list, err := a.pool.List()
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	add := func(name string) {
		if key := repoKey(name); !seen[key] {
			seen[key] = true
			repos = append(repos, name)
		}
	}
	for _, r := range list {
		if name := githubName(r); name != "" {
			add(name)
		} else {
			skipped = append(skipped, r.Name)
		}
	}
	for _, c := range a.clones.List() {
		if c.State != CloneFailed {
			add(c.Repo)
		}
	}
	slices.SortFunc(repos, func(x, y string) int { return strings.Compare(repoKey(x), repoKey(y)) })
	return repos, skipped, nil
}

// ExportResult says what an export wrote.
type ExportResult struct {
	Path    string   `json:"path"` // empty when the dialog was cancelled
	Repos   int      `json:"repos"`
	Skipped []string `json:"skipped"` // pool repositories not from GitHub, left out
}

// writeConfig writes the configuration file to path.
func (a *app) writeConfig(path string) (ExportResult, error) {
	repos, skipped, err := a.exportedRepos()
	if err != nil {
		return ExportResult{}, err
	}
	a.setmu.Lock()
	s := a.settings
	a.setmu.Unlock()
	if err := os.WriteFile(path, formatConfig(s, repos), 0o644); err != nil {
		return ExportResult{}, err
	}
	if skipped == nil {
		skipped = []string{}
	}
	return ExportResult{Path: path, Repos: len(repos), Skipped: skipped}, nil
}

// previewImport reads a configuration file and says what importing it
// would do.
func (a *app) previewImport(ctx context.Context, path string) (ImportPlan, error) {
	c, err := readConfig(path)
	if err != nil {
		return ImportPlan{}, err
	}
	repos, err := a.pool.List()
	if err != nil {
		return ImportPlan{}, err
	}
	a.setmu.Lock()
	current := a.settings
	a.setmu.Unlock()
	plan := planImport(c, current, repos, a.clones.List(), func(r pool.Repo) string {
		reason, err := a.pool.InUse(ctx, r)
		if err != nil {
			return "yesman could not check whether work depends on it: " + err.Error()
		}
		return reason
	})
	plan.Path = path
	return plan, nil
}

// ImportResult says what an import did.
type ImportResult struct {
	Mode    importMode  `json:"mode"`
	Cloning []string    `json:"cloning"` // repositories it started cloning
	Removed []string    `json:"removed"` // pool names of the repositories it removed
	Stopped []string    `json:"stopped"` // clones it stopped, as owner/repo
	Kept    []ExtraRepo `json:"kept"`    // repositories replacing left, and why
}

// importConfig imports a configuration file. It reads the file again,
// since it may have changed since its preview, and checks each repository
// it would remove again too.
func (a *app) importConfig(ctx context.Context, path string, mode importMode) (ImportResult, error) {
	if mode != importMerge && mode != importReplace {
		return ImportResult{}, fmt.Errorf("unknown import mode %q", mode)
	}
	a.cfgmu.Lock()
	defer a.cfgmu.Unlock()
	c, err := readConfig(path)
	if err != nil {
		return ImportResult{}, err
	}
	res := ImportResult{Mode: mode, Cloning: []string{}, Removed: []string{}, Stopped: []string{}, Kept: []ExtraRepo{}}

	a.setmu.Lock()
	err = a.putSettingsLocked(c.settings(a.settings, mode))
	a.setmu.Unlock()
	if err != nil {
		return res, err
	}

	if mode == importReplace {
		// Clones the file leaves out stop first, so that none adds a
		// repository after the pool is reconciled. One that landed before
		// it stopped is removed with the pool's other extras below.
		listed := map[string]bool{}
		for _, r := range c.repos {
			listed[repoKey(r.name)] = true
		}
		stopped, held, err := a.clones.Withdraw(ctx, func(repo string) bool { return listed[repoKey(repo)] })
		if stopped != nil {
			res.Stopped = stopped
		}
		if err != nil {
			return res, fmt.Errorf("stop the clones the file leaves out: %w", err)
		}
		for _, r := range held {
			res.Kept = append(res.Kept, ExtraRepo{Name: "github.com/" + r, Label: r, InUse: agentCloning, Clone: CloneCloning})
		}
	}

	repos, err := a.pool.List()
	if err != nil {
		return res, err
	}
	plan := planImport(c, Settings{}, repos, a.clones.List(), nil)
	if len(plan.Add) > 0 {
		inputs := make([]string, 0, len(plan.Add))
		for _, r := range c.repos {
			if slices.Contains(plan.Add, r.name) {
				inputs = append(inputs, r.input)
			}
		}
		if _, err := a.clones.Queue(inputs); err != nil {
			return res, err
		}
		res.Cloning = plan.Add
	}
	if mode == importReplace {
		for _, e := range plan.Extra {
			if e.Clone != "" {
				continue // an agent's clone, kept above
			}
			var used *pool.InUseError
			switch err := a.pool.Remove(ctx, e.Name); {
			case err == nil:
				res.Removed = append(res.Removed, e.Name)
			case errors.As(err, &used):
				e.InUse = used.Reason
				res.Kept = append(res.Kept, e)
			default:
				e.InUse = "removing it failed: " + err.Error()
				res.Kept = append(res.Kept, e)
			}
		}
		if len(res.Removed) > 0 {
			a.fetcher.Forget(res.Removed...)
			a.notifyChanged()
		}
	}
	return res, nil
}

// configFilters are what the file dialogs offer.
var configFilters = []mygo.FileFilter{{Name: "yesman configuration", Extensions: []string{"toml"}}}

// The configuration file's dialogs. End-to-end builds may answer them
// themselves, since automation cannot reach native dialogs.
var (
	configSaveDialog = mygo.Dialog.Save
	configOpenDialog = mygo.Dialog.Open
)

func (a *app) window() *mygo.Window {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	return a.win
}

// ExportConfig asks where to save the configuration file and writes it.
// The result has no path when the dialog was cancelled.
func (s *service) ExportConfig() (ExportResult, error) {
	dir, _ := os.UserHomeDir()
	path, err := configSaveDialog(mygo.SaveDialogOptions{
		Parent: s.app.window(), Title: "Export yesman configuration", ButtonLabel: "Export",
		DefaultPath: filepath.Join(dir, "yesman.toml"), Filters: configFilters, CreateDirectories: true,
	})
	if err != nil || path == "" {
		return ExportResult{Skipped: []string{}}, err
	}
	return s.app.writeConfig(path)
}

// ChooseConfigFile asks for a configuration file to import; "" when the
// dialog was cancelled.
func (s *service) ChooseConfigFile() (string, error) {
	dir, _ := os.UserHomeDir()
	paths, err := configOpenDialog(mygo.OpenDialogOptions{
		Parent: s.app.window(), Title: "Import yesman configuration", ButtonLabel: "Choose",
		DefaultPath: dir, Filters: configFilters,
	})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

// PreviewImport says what importing a configuration file would do.
func (s *service) PreviewImport(ctx context.Context, path string) (ImportPlan, error) {
	return s.app.previewImport(ctx, path)
}

// ImportConfig imports a configuration file, merging it or replacing with
// it. Its repositories clone in the background, as the clones event shows.
func (s *service) ImportConfig(path, mode string) (ImportResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return s.app.importConfig(ctx, path, importMode(mode))
}
