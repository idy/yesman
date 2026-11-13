package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"

	"github.com/idy/yesman/internal/home"
	"github.com/idy/yesman/internal/pool"
)

func TestConfigRoundTrip(t *testing.T) {
	data := formatConfig(Settings{Appearance: mygo.ThemeDark, AutoFetch: 15}, []string{"idy/magpie", "idy/yesman"})
	c, err := parseConfig(data)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if *c.appearance != mygo.ThemeDark || *c.autoFetch != 15 || len(c.ignored) != 0 {
		t.Errorf("read back %+v", c)
	}
	if got := []configRepo{{"idy/magpie", "idy/magpie"}, {"idy/yesman", "idy/yesman"}}; !reflect.DeepEqual(c.repos, got) {
		t.Errorf("repos = %+v", c.repos)
	}

	// No repositories and no appearance chosen still make a full file.
	c, err = parseConfig(formatConfig(Settings{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if *c.appearance != mygo.ThemeSystem || *c.autoFetch != 0 || len(c.repos) != 0 {
		t.Errorf("empty settings read back %+v", c)
	}

	if got := tomlString("a\"b\\c\n"); got != `"a\"b\\c\u000A"` {
		t.Errorf("tomlString = %s", got)
	}
}

func TestParseConfig(t *testing.T) {
	c, err := parseConfig([]byte(`
version = 1
repos = ["idy/yesman", "IDY/Yesman", "git@github.com:idy/magpie.git"]
theme = "dark"
[later]
key = 1
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.appearance != nil || c.autoFetch != nil {
		t.Errorf("keys left out were read as %v, %v", c.appearance, c.autoFetch)
	}
	if want := []configRepo{{"idy/yesman", "idy/yesman"}, {"idy/magpie", "git@github.com:idy/magpie.git"}}; !reflect.DeepEqual(c.repos, want) {
		t.Errorf("repos = %+v", c.repos)
	}
	if want := []string{"theme", "later", "later.key"}; !reflect.DeepEqual(c.ignored, want) {
		t.Errorf("ignored = %q", c.ignored)
	}

	for text, want := range map[string]string{
		`repos = []`:                          "no version",
		"version = 2":                         "newer yesman",
		"version = 0":                         "unknown",
		"version = 1\nappearance = \"blue\"":  "appearance",
		"version = 1\nauto_fetch = 7":         "auto_fetch",
		"version = 1\nrepos = [\"nope\"]":     "repos",
		"version = 1\nrepos = \"idy/yesman\"": "not a yesman configuration",
		"version = ":                          "not a yesman configuration",
	} {
		if _, err := parseConfig([]byte(text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("parseConfig(%q) = %v, want an error about %s", text, err, want)
		}
	}
}

func TestConfigSettings(t *testing.T) {
	dark := mygo.ThemeDark
	c := config{appearance: &dark}
	current := Settings{Appearance: mygo.ThemeLight, AutoFetch: 5}
	if got := c.settings(current, importMerge); got != (Settings{Appearance: mygo.ThemeDark, AutoFetch: 5}) {
		t.Errorf("merging = %+v", got)
	}
	if got := c.settings(current, importReplace); got != (Settings{Appearance: mygo.ThemeDark}) {
		t.Errorf("replacing = %+v", got)
	}
	// Replacing with a file that sets no appearance follows the system.
	if got := (config{}).settings(current, importReplace); got != (Settings{Appearance: mygo.ThemeSystem}) {
		t.Errorf("replacing without an appearance = %+v", got)
	}
}

func TestPlanImport(t *testing.T) {
	c, err := parseConfig([]byte(`version = 1
repos = ["acme/kept", "acme/new", "acme/cloning", "acme/failed"]`))
	if err != nil {
		t.Fatal(err)
	}
	repos := []pool.Repo{
		{Name: "github.com/acme/kept", URL: "git@github.com:Acme/Kept.git"},
		{Name: "github.com/acme/busy", URL: "https://github.com/acme/busy"},
		{Name: "github.com/acme/idle", URL: "https://github.com/acme/idle"},
		{Name: "local/old", URL: "/src/old"},
	}
	clones := []Clone{{Repo: "acme/cloning", State: CloneCloning}, {Repo: "acme/failed", State: CloneFailed},
		{Repo: "acme/stray", State: CloneQueued}, {Repo: "acme/helper", State: CloneCloning, agents: 1}}
	plan := planImport(c, Settings{AutoFetch: 5}, repos, clones, func(r pool.Repo) string {
		if r.Name == "github.com/acme/busy" {
			return "a worktree is checked out of it"
		}
		return ""
	})
	if want := []string{"acme/new", "acme/failed"}; !reflect.DeepEqual(plan.Add, want) {
		t.Errorf("Add = %q", plan.Add)
	}
	if want := []string{"acme/kept", "acme/cloning"}; !reflect.DeepEqual(plan.Present, want) {
		t.Errorf("Present = %q", plan.Present)
	}
	want := []ExtraRepo{
		{Name: "github.com/acme/busy", Label: "acme/busy", InUse: "a worktree is checked out of it"},
		{Name: "github.com/acme/idle", Label: "acme/idle"},
		{Name: "local/old", Label: "local/old"},
		{Name: "github.com/acme/stray", Label: "acme/stray", Clone: CloneQueued},
		{Name: "github.com/acme/helper", Label: "acme/helper", InUse: agentCloning, Clone: CloneCloning},
	}
	if !reflect.DeepEqual(plan.Extra, want) {
		t.Errorf("Extra = %+v", plan.Extra)
	}
	if plan.Merge.AutoFetch != 5 || plan.Replace.AutoFetch != 0 || plan.Replace.Appearance != mygo.ThemeSystem {
		t.Errorf("settings: merge %+v, replace %+v", plan.Merge, plan.Replace)
	}
}

// git runs git in dir for a test.
func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// TestImportConfig exports a pool, then replaces it with another file: the
// file's new repositories clone, settings it leaves out reset, and of the
// repositories it leaves out only those no work depends on go.
func TestImportConfig(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	ctx := context.Background()
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, src, "init", "--quiet", "--initial-branch=main")
	gitT(t, src, "commit", "--quiet", "--allow-empty", "-m", "first")

	h := home.Home{Root: filepath.Join(tmp, "home")}
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	p := pool.New(h.Repos())
	// Pool repositories cloned from a local origin, then pointed at GitHub
	// names the way a clone from GitHub has them.
	add := func(dir, url string) pool.Repo {
		t.Helper()
		bare := filepath.Join(tmp, dir)
		gitT(t, tmp, "clone", "--quiet", "--bare", src, bare)
		r, err := p.Add(ctx, bare, "")
		if err != nil {
			t.Fatal(err)
		}
		gitT(t, r.Path, "remote", "set-url", "origin", url)
		return r
	}
	add("kept", "https://github.com/acme/kept.git")
	busy := add("busy", "https://github.com/acme/busy.git")
	add("idle", "https://github.com/acme/idle.git")
	add("old", filepath.Join(tmp, "elsewhere", "old"))
	if _, err := p.AddWorktree(ctx, busy, pool.WorktreeOptions{Dest: filepath.Join(h.Yesmen(), "alice", "busy"), Branch: "work"}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var cloned []string
	done := make(chan struct{}, 8)
	a := &app{home: h, pool: p, settings: Settings{Appearance: mygo.ThemeLight, AutoFetch: 5}}
	a.fetcher = newFetcher(p, nil)
	a.clones = newClones(func(_ context.Context, repo string) (pool.Repo, error) {
		mu.Lock()
		cloned = append(cloned, repo)
		mu.Unlock()
		done <- struct{}{}
		return pool.Repo{}, nil
	}, nil)

	exported := filepath.Join(tmp, "yesman.toml")
	res, err := a.writeConfig(exported)
	if err != nil {
		t.Fatal(err)
	}
	if res.Repos != 3 || !reflect.DeepEqual(res.Skipped, []string{"local/old"}) {
		t.Errorf("export = %+v", res)
	}
	data, _ := os.ReadFile(exported)
	for _, want := range []string{`appearance = "light"`, "auto_fetch = 5", `"acme/busy",`, `"acme/idle",`, `"acme/kept",`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the export lacks %s:\n%s", want, data)
		}
	}

	// Merging the export back changes nothing.
	if res, err := a.importConfig(ctx, exported, importMerge); err != nil || len(res.Cloning)+len(res.Removed)+len(res.Kept) != 0 {
		t.Errorf("merging the export = %+v, %v", res, err)
	}

	file := filepath.Join(tmp, "other.toml")
	if err := os.WriteFile(file, []byte("version = 1\nappearance = \"dark\"\nrepos = [\"acme/kept\", \"git@github.com:acme/new.git\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := a.previewImport(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Add, []string{"acme/new"}) || len(plan.Extra) != 3 || plan.Extra[0].InUse == "" || plan.Extra[1].InUse != "" {
		t.Errorf("preview = %+v", plan)
	}

	got, err := a.importConfig(ctx, file, importReplace)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Cloning, []string{"acme/new"}) {
		t.Errorf("cloning %q", got.Cloning)
	}
	if !reflect.DeepEqual(got.Removed, []string{"local/idle", "local/old"}) {
		t.Errorf("removed %q", got.Removed)
	}
	if len(got.Kept) != 1 || got.Kept[0].Label != "acme/busy" || !strings.Contains(got.Kept[0].InUse, "worktree") {
		t.Errorf("kept %+v", got.Kept)
	}
	if a.settings != (Settings{Appearance: mygo.ThemeDark}) {
		t.Errorf("settings after replacing = %+v", a.settings)
	}
	if loadSettings(h.Settings()) != a.settings {
		t.Errorf("settings were not saved")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("nothing was cloned")
	}
	mu.Lock()
	if !slices.Equal(cloned, []string{"git@github.com:acme/new.git"}) {
		t.Errorf("cloned %q", cloned)
	}
	mu.Unlock()
	repos, _ := p.List()
	var names []string
	for _, r := range repos {
		names = append(names, r.Name)
	}
	if !slices.Equal(names, []string{"local/busy", "local/kept"}) {
		t.Errorf("the pool keeps %q", names)
	}

	if _, err := a.importConfig(ctx, file, "overwrite"); err == nil {
		t.Error("an unknown mode imported")
	}
}
