package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseCreatePreservesBodyAndRejectsAmbiguity(t *testing.T) {
	file := filepath.Join(t.TempDir(), "body with spaces.md")
	body := "literal $() `text` & 中文\nsecond line\n"
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	in, err := ParseCreate([]string{"-R", "IdY/YesMan", "pr", "create", "-H", "test/identity", "-B", "main", "-t", "test: spaces & punctuation", "-F", file, "-d"}, strings.NewReader(""))
	if err != nil || in.Body != body || !in.Draft || in.Repository != "idy/yesman" {
		t.Fatalf("parse: %+v %v", in, err)
	}
	for _, args := range [][]string{
		{"pr", "create", "--fill"},
		{"pr", "create", "--repo", "idy/yesman", "--repo", "other/project"},
		{"pr", "create", "--body", "one", "--body-file", "-"},
		{"pr", "create", "--title"},
	} {
		if _, err := ParseCreate(args, strings.NewReader("body")); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestAttachedShortValuesPreserveEquals(t *testing.T) {
	body := "Checks=passed\nparameter=a=b\n"
	path := filepath.Join(t.TempDir(), "body=with spaces.md")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, bodyArgs := range [][]string{{"-b" + body}, {"-b=" + body}, {"--body=" + body}, {"-F" + path}, {"-F=" + path}} {
		args := append([]string{"pr", "-Ridy/yesman", "create", "-Hfix/a=b", "-Bbase=x", "-tfix: Handle a=b"}, bodyArgs...)
		in, err := ParseCreate(args, strings.NewReader(""))
		if err != nil || in.Body != body || in.Title != "fix: Handle a=b" || in.Head != "fix/a=b" || in.Base != "base=x" {
			t.Fatalf("attached value changed for %v: %v", bodyArgs, err)
		}
	}
	in, err := ParseCreate([]string{"pr", "create", "-Ridy/yesman", "-Htest", "-Bmain", "-t=title=x", "-b==literal"}, strings.NewReader(""))
	if err != nil || in.Title != "title=x" || in.Body != "=literal" {
		t.Fatal("leading delimiter consumed content")
	}
}

func fixtureGHExecutable(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(`package main
import("encoding/json";"os")
func main(){json.NewEncoder(os.Stdout).Encode(map[string]any{"args":os.Args[1:],"token":os.Getenv("GH_TOKEN"),"other":os.Getenv("FIXTURE_OTHER")})}
`), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "fixture-gh")
	if runtime.GOOS == "windows" {
		dest += ".exe"
	}
	command := exec.Command("go", "build", "-o", dest, src)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build argv capture fixture: %v: %s", err, out)
	}
	return dest
}

func TestShimModesAndNoPersonalFallback(t *testing.T) {
	executable := fixtureGHExecutable(t)
	t.Setenv("YESMAN_GH_REAL", executable)
	t.Setenv("YESMAN_TOKEN", "agent-fixture-credential")
	t.Setenv("GH_TOKEN", "personal-fixture-token")
	t.Setenv("GH_HOST", "")
	t.Setenv("FIXTURE_OTHER", "preserved")
	mode := "personal"
	status := 200
	creates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-fixture-credential" {
			t.Error("wrong local caller credential")
		}
		if status != 200 {
			http.Error(w, "unavailable", status)
			return
		}
		switch r.URL.Path {
		case "/v1/github-app/mode":
			writeJSON(w, map[string]string{"mode": mode})
		case "/v1/github-app/pulls":
			creates++
			data, _ := io.ReadAll(r.Body)
			if strings.Contains(string(data), "personal-fixture") || strings.Contains(string(data), "PRIVATE KEY") {
				t.Error("secret forwarded to broker")
			}
			var in PullInput
			if err := json.Unmarshal(data, &in); err != nil {
				t.Error(err)
			}
			if in.Title != "--help" || in.Body != "literal body" {
				t.Error("arguments were not preserved")
			}
			writeJSON(w, map[string]any{"number": 77, "html_url": "https://github.com/idy/yesman/pull/77", "user": map[string]string{"type": "Bot", "login": "yesman-test[bot]"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("YESMAN_API", server.URL)
	run := func(args []string) (int, string, string) {
		var out, errout bytes.Buffer
		code := RunShim(context.Background(), args, strings.NewReader("literal body"), &out, &errout)
		return code, out.String(), errout.String()
	}
	for _, args := range [][]string{{"pr", "merge", "58", "--squash"}, {"pr", "create", "--fill"}, {"api", "repos/idy/yesman"}} {
		code, out, errout := run(args)
		if code != 0 || !strings.Contains(out, "personal-fixture-token") || !strings.Contains(out, "preserved") {
			t.Fatalf("personal passthrough %d %s %s", code, out, errout)
		}
	}
	mode = "app"
	args := []string{"pr", "create", "-R", "idy/yesman", "-H", "test/app", "-B", "main", "-t", "--help", "-F", "-", "--draft"}
	code, out, errout := run(args)
	if code != 0 || out != "https://github.com/idy/yesman/pull/77\n" || !strings.Contains(errout, "yesman-test[bot]") || creates != 1 {
		t.Fatalf("App routing %d %s %s", code, out, errout)
	}
	variants := [][]string{
		{"pr", "--repo", "idy/yesman", "create", "-H", "test/app", "-B", "main", "-t", "--help", "-F", "-", "--draft"},
		{"--repo=idy/yesman", "pr", "create", "--head=test/app", "--base=main", "--title=--help", "--body-file=-"},
		{"pr", "-Ridy/yesman", "create", "-Htest/app", "-Bmain", "-t--help", "-F-", "-d"},
	}
	for _, variant := range variants {
		code, out, _ := run(variant)
		if code != 0 || out != "https://github.com/idy/yesman/pull/77\n" {
			t.Fatalf("flag placement changed the PR identity: %v", variant)
		}
	}
	created := 1 + len(variants)
	for _, nonCreate := range [][]string{{"pr", "--repo", "idy/yesman", "view", "--json", "create"}, {"issue", "create", "--title", "pr", "--body", "create"}, {"pr", "--repo", "idy/yesman", "create", "--help"}} {
		code, out, _ := run(nonCreate)
		if code != 0 || !strings.Contains(out, "personal-fixture-token") || creates != created {
			t.Fatal("non-creation/help did not retain existing credentials")
		}
	}
	for _, unsupported := range [][]string{{"pr", "create", "--fill"}, {"pr", "--future-option", "value", "create"}, {"pr", "create", "--future-option", "--help"}, {"--", "pr", "create"}} {
		code, out, _ = run(unsupported)
		if code == 0 || out != "" || creates != created {
			t.Fatal("unsupported/ambiguous option fell through to personal")
		}
	}
	t.Setenv("GH_HOST", "enterprise.example")
	code, out, _ = run(args)
	if code == 0 || out != "" || creates != created {
		t.Fatal("conflicting host created a PR on github.com")
	}
	t.Setenv("GH_HOST", "")
	status = 503
	code, out, _ = run(args)
	if code == 0 || out != "" || creates != created {
		t.Fatal("service failure fell through to personal")
	}
	t.Setenv("YESMAN_API", "https://example.invalid")
	code, out, _ = run(args)
	if code == 0 || out != "" {
		t.Fatal("non-loopback broker accepted")
	}
}
