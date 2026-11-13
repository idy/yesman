package pipkg

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseSource(t *testing.T) {
	for _, c := range []struct {
		in            string
		kind, name, v string
		pinned        bool
	}{
		{"npm:pi-web-access", "npm", "pi-web-access", "", false},
		{"  npm:pi-web-access@0.47.0 ", "npm", "pi-web-access", "0.47.0", true},
		{"npm:@lennix1337/pi-agent-desktop@0.1.3", "npm", "@lennix1337/pi-agent-desktop", "0.1.3", true},
		{"npm:@scope/tools", "npm", "@scope/tools", "", false},
		{"npm:tools@^1.2", "npm", "tools", "^1.2", false},
		{"git:github.com/user/repo@v1", "git", "", "", false},
		{"https://github.com/user/repo", "git", "", "", false},
		{"ssh://git@github.com/user/repo", "git", "", "", false},
		{"git@github.com:user/repo", "git", "", "", false},
		{"./local/path", "local", "", "", false},
		{"/abs/path", "local", "", "", false},
		{"~/pi/tools", "local", "", "", false},
		{`C:\pi\tools`, "local", "", "", false},
	} {
		s, err := ParseSource(c.in)
		if err != nil {
			t.Errorf("ParseSource(%q): %v", c.in, err)
			continue
		}
		if s.Kind != c.kind || s.Name != c.name || s.Version != c.v || s.Pinned() != c.pinned || s.Raw != strings.TrimSpace(c.in) {
			t.Errorf("ParseSource(%q) = %+v pinned=%v", c.in, s, s.Pinned())
		}
	}
	for _, in := range []string{"", "--help", "-e", "pi-web-access", "npm:", "npm:Bad Name", "npm:tools@", "git:a b", "npm:x\nnpm:y", "javascript:alert(1)"} {
		if s, err := ParseSource(in); err == nil {
			t.Errorf("ParseSource(%q) = %+v, want an error", in, s)
		}
	}
}

func TestParseList(t *testing.T) {
	dir := t.TempDir()
	web := filepath.Join(dir, "node_modules", "pi-web-access")
	write(t, filepath.Join(web, "package.json"), `{"name":"pi-web-access","version":"0.47.0","description":"Web search","repository":{"url":"git+https://github.com/x/pi-web-access.git"},"pi":{"extensions":["./dist"],"skills":[]}}`)
	plain := filepath.Join(dir, "git", "tools")
	write(t, filepath.Join(plain, "package.json"), `{"name":"tools","version":"1.0.0"}`)
	write(t, filepath.Join(plain, "skills", "a", "SKILL.md"), "")
	write(t, filepath.Join(plain, "prompts", "p.md"), "")
	file := filepath.Join(dir, "one.ts")
	write(t, file, "")

	out := "\x1b[1mUser packages:\x1b[22m\n" +
		"  npm:pi-web-access\n" +
		"    \x1b[2m" + web + "\x1b[22m\n" +
		"  git:github.com/me/tools@v2 (filtered)\n" +
		"    " + plain + "\n" +
		"  " + file + "\n" +
		"    " + file + "\n" +
		"  npm:not-yet\n" +
		"\n" +
		"Project packages:\n" +
		"  npm:project-only\n" +
		"    /somewhere\n"
	got := ParseList(out)
	if len(got) != 4 {
		t.Fatalf("ParseList = %+v, want 4 user packages", got)
	}
	if p := got[0]; p.Kind != "npm" || p.Title != "pi-web-access" || p.Current != "0.47.0" || p.Description != "Web search" ||
		p.Homepage != "https://github.com/x/pi-web-access" || !slices.Equal(p.Provides, []string{"extensions"}) || p.Path != web {
		t.Errorf("npm package = %+v", p)
	}
	if p := got[1]; p.Kind != "git" || !p.Filtered || p.Title != "tools" || p.Current != "1.0.0" || !slices.Equal(p.Provides, []string{"skills", "prompts"}) {
		t.Errorf("git package = %+v", p)
	}
	if p := got[2]; p.Kind != "local" || p.Title != "one.ts" || !slices.Equal(p.Provides, []string{"extensions"}) {
		t.Errorf("file = %+v", p)
	}
	if p := got[3]; p.Name != "not-yet" || p.Path != "" || p.Current != "" || p.Title != "not-yet" {
		t.Errorf("uninstalled = %+v", p)
	}
	if got := ParseList("No packages installed.\n"); len(got) != 0 {
		t.Errorf("empty list = %+v", got)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func registry(t *testing.T, routes map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.EscapedPath()
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		body, ok := routes[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := Registry
	Registry = srv.URL
	t.Cleanup(func() { Registry = old })
}

func TestSearch(t *testing.T) {
	registry(t, map[string]string{
		"/-/v1/search?from=30&size=30&text=keywords%3Api-package+web+search": `{"total":31,"objects":[{"downloads":{"weekly":12,"monthly":40},
			"package":{"name":"pi-web-access","version":"0.47.0","description":"Web","license":"MIT","date":"2026-11-01T10:00:00Z",
			"publisher":{"username":"nico"},"links":{"npm":"https://www.npmjs.com/package/pi-web-access","homepage":"javascript:alert(1)","repository":"git+https://github.com/nico/pi-web-access.git"}}}]}`,
	})
	r, err := Search(context.Background(), "  web   search ", 30)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 31 || r.From != 30 || len(r.Packages) != 1 {
		t.Fatalf("Search = %+v", r)
	}
	p := r.Packages[0]
	if p.Name != "pi-web-access" || p.Weekly != 12 || p.Publisher != "nico" || p.Repository != "https://github.com/nico/pi-web-access" || p.Homepage != "" || p.Date.IsZero() {
		t.Errorf("listing = %+v", p)
	}
}

func TestPackage(t *testing.T) {
	registry(t, map[string]string{
		"/@scope%2Ftools": `{"name":"@scope/tools","dist-tags":{"latest":"2.0.0"},
			"time":{"2.0.0":"2026-10-01T00:00:00Z","unpublished":{"time":"x"}},
			"readme":"# Tools",
			"versions":{"1.0.0":{},"2.0.0":{"version":"2.0.0","description":"Tools","license":{"type":"MIT"},
				"repository":"github:me/tools","author":"Me <me@example.com>","keywords":["pi-package"],
				"pi":{"image":"https://example.com/a.png","video":"file:///etc/passwd","extensions":["./x.ts"],"themes":["./t"]}}}}`,
		"/@scope%2Ftools/latest": `{"version":"2.0.0"}`,
	})
	d, err := Package(context.Background(), "@scope/tools")
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != "2.0.0" || d.License != "MIT" || d.Author != "Me" || d.Repository != "https://github.com/me/tools" || !d.IsPi ||
		d.Image != "https://example.com/a.png" || d.Video != "" || d.Readme != "# Tools" || d.Versions != 2 ||
		!slices.Equal(d.Provides, []string{"extensions", "themes"}) || d.Date.IsZero() {
		t.Errorf("Package = %+v", d)
	}
	if v, err := Latest(context.Background(), "@scope/tools"); err != nil || v != "2.0.0" {
		t.Errorf("Latest = %q, %v", v, err)
	}
	if _, err := Package(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing package: %v", err)
	}
	if _, err := Package(context.Background(), "../x"); err == nil {
		t.Error("Package took a path")
	}
}

func TestRepoURL(t *testing.T) {
	for in, want := range map[string]string{
		"git+https://github.com/a/b.git": "https://github.com/a/b",
		"git://github.com/a/b.git":       "https://github.com/a/b",
		"git+ssh://git@github.com/a/b":   "https://github.com/a/b",
		"git@gitlab.com:a/b.git":         "https://gitlab.com/a/b",
		"github:a/b":                     "https://github.com/a/b",
		"a/b":                            "https://github.com/a/b",
		"https://example.com/x":          "https://example.com/x",
		"":                               "",
		"javascript:alert(1)":            "",
	} {
		if got := repoURL(in); got != want {
			t.Errorf("repoURL(%q) = %q, want %q", in, got, want)
		}
	}
}
