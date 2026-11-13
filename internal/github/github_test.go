package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParse(t *testing.T) {
	for in, want := range map[string]Repo{
		"idy/yesman":                                  {Owner: "idy", Name: "yesman"},
		"  idy/yesman.git ":                           {Owner: "idy", Name: "yesman"},
		"github.com/idy/yesman":                       {Owner: "idy", Name: "yesman"},
		"github.com/idy/yesman/pull/3":                {Owner: "idy", Name: "yesman"},
		"https://github.com/idy/yesman":               {Owner: "idy", Name: "yesman", Protocol: "https"},
		"https://github.com/idy/yesman.git":           {Owner: "idy", Name: "yesman", Protocol: "https"},
		"https://www.github.com/idy/yesman/tree/main": {Owner: "idy", Name: "yesman", Protocol: "https"},
		"http://github.com/idy/yesman/":               {Owner: "idy", Name: "yesman", Protocol: "https"},
		"git@github.com:idy/yesman.git":               {Owner: "idy", Name: "yesman", Protocol: "ssh"},
		"ssh://git@github.com/idy/yesman.git":         {Owner: "idy", Name: "yesman", Protocol: "ssh"},
		"https://github.com/my-org/my.repo_2":         {Owner: "my-org", Name: "my.repo_2", Protocol: "https"},
	} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
}

func TestParseRejectsOtherRepositories(t *testing.T) {
	for _, in := range []string{
		"",
		"yesman",
		"idy/yesman/extra",
		"https://gitlab.com/idy/yesman",
		"git@gitlab.com:idy/yesman.git",
		"https://github.com/idy",
		"/tmp/src/shop-api",
		"file:///tmp/src/shop-api",
		"-idy/yesman",
		"idy/..",
		"idy/yes man",
		"git://github.com/idy/yesman",
	} {
		if r, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %+v, want an error", in, r)
		}
	}
}

func TestResolve(t *testing.T) {
	defer func(p func(context.Context) string, l func(context.Context, string) (info, error)) {
		protocol, lookup = p, l
	}(protocol, lookup)
	repos := map[string]string{ // lowercase name -> GitHub's spelling and parent
		"idy/fork":   "IDY/fork badlogic/pi",
		"idy/plain":  "idy/plain",
		"idy/hidden": "",
	}
	lookup = func(_ context.Context, name string) (info, error) {
		var v info
		switch repos[name] {
		case "IDY/fork badlogic/pi":
			v.FullName = "IDY/fork"
			v.Parent = &struct {
				FullName string `json:"full_name"`
			}{"badlogic/pi"}
		case "idy/plain":
			v.FullName = "idy/plain"
		default:
			return v, errors.New("GitHub answered 404 Not Found")
		}
		return v, nil
	}
	protocol = func(context.Context) string { return "ssh" }

	for in, want := range map[string]Resolved{
		// owner/repo takes the CLI's protocol; a fork's parent follows it.
		"idy/fork": {URL: "git@github.com:IDY/fork.git", Upstream: "git@github.com:badlogic/pi.git"},
		// A URL keeps its own protocol.
		"https://github.com/idy/fork": {URL: "https://github.com/IDY/fork.git", Upstream: "https://github.com/badlogic/pi.git"},
		"idy/plain":                   {URL: "git@github.com:idy/plain.git"},
		// When GitHub cannot say, the clone decides.
		"https://github.com/idy/hidden": {URL: "https://github.com/idy/hidden.git"},
	} {
		got, err := Resolve(context.Background(), in)
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	if _, err := Resolve(context.Background(), "https://gitlab.com/idy/plain"); err == nil {
		t.Error("a GitLab repository resolved")
	}
}

// Without the GitHub CLI, the public API answers for public repositories.
func TestLookupWithoutCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/idy/fork" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"full_name":"idy/fork","fork":true,"parent":{"full_name":"badlogic/pi"}}`))
	}))
	defer srv.Close()
	defer func(b string) { apiBase = b }(apiBase)
	apiBase = srv.URL

	v, err := ghLookup(context.Background(), "idy/fork")
	if err != nil || v.FullName != "idy/fork" || v.Parent == nil || v.Parent.FullName != "badlogic/pi" {
		t.Errorf("lookup = %+v, %v", v, err)
	}
	if _, err := ghLookup(context.Background(), "idy/missing"); err == nil {
		t.Error("a missing repository was found")
	}
	if p := ghProtocol(context.Background()); p != "https" {
		t.Errorf("protocol without the CLI = %q", p)
	}
}
