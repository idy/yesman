package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// gh itself parses the boolean/alias matrix. Its only transport is a local
// socket with a synthetic personal credential; no real profile or API is used.
func TestRealGHHelpCannotBypassIdentity(t *testing.T) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal(err)
		}
		t.Skip("GitHub CLI is not installed")
	}
	dir, err := os.MkdirTemp("", "gh-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	listener, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int32
	repo := map[string]any{"id": "R_synthetic", "name": "yesman", "nameWithOwner": "idy/yesman", "owner": map[string]any{"login": "idy", "__typename": "User"}, "isFork": false, "isPrivate": false, "isArchived": false, "isEmpty": false, "viewerPermission": "READ", "defaultBranchRef": map[string]any{"name": "main"}, "pullRequests": map[string]any{"nodes": []any{}}}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.github.com" {
			t.Error("unexpected socket host")
			http.Error(w, "refused", 400)
			return
		}
		var q struct {
			Query string `json:"query"`
		}
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			http.Error(w, "bad", 400)
			return
		}
		if strings.Contains(q.Query, "createPullRequest") {
			writes.Add(1)
			if a := r.Header.Get("Authorization"); a != "token synthetic-personal-fixture" && a != "Bearer synthetic-personal-fixture" {
				t.Error("wrong fixture credential")
			}
			writeJSON(w, map[string]any{"data": map[string]any{"createPullRequest": map[string]any{"pullRequest": map[string]any{"id": "PR_synthetic", "number": 77, "url": "https://github.com/idy/yesman/pull/77"}}}})
		} else {
			writeJSON(w, map[string]any{"data": map[string]any{"repository": repo, "viewer": map[string]string{"login": "synthetic-human"}}})
		}
	})}
	served := make(chan struct{})
	go func() { defer close(served); _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close(); <-served }()
	cfg, _ := json.Marshal(map[string]string{"http_unix_socket": filepath.Join(dir, "s")})
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), cfg, 0600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "GH_CONFIG_DIR"} {
		t.Setenv(k, dir)
	}
	for k, v := range map[string]string{"GH_TOKEN": "synthetic-personal-fixture", "GITHUB_TOKEN": "", "GH_HOST": "github.com", "GH_DEBUG": "", "GH_PROMPT_DISABLED": "1", "GH_NO_UPDATE_NOTIFIER": "1", "GH_NO_EXTENSION_UPDATE_NOTIFIER": "1", "YESMAN_GH_REAL": gh, "YESMAN_API": "http://127.0.0.1:1", "YESMAN_TOKEN": "synthetic-local-fixture"} {
		t.Setenv(k, v)
	}
	cases := [][]string{{"--help"}, {"-h"}, {"--help=true", "--help=false"}, {"--help=true", "--help=0"}, {"--help=false", "--help=true"}, {"-h", "--help=false"}, {"--help", "-h=false"}, {"-h", "-h=false"}, {"--help", "--help"}, {"--help=true", "--help=invalid"}, {"--help", "--", "--help=false"}, {"--future-option", "--help"}, {"-dh=false"}}
	for _, v := range []string{"true", "false", "1", "0", "TRUE", "False", "invalid"} {
		cases = append(cases, []string{"--help=" + v}, []string{"-h=" + v}, []string{"--help=true", "--help=" + v})
	}
	base := []string{"pr", "create", "--repo", "idy/yesman", "--head", "fixture", "--base", "main", "--title", "test: synthetic", "--body", "fixture"}
	var rawWrites int32
	for _, flags := range cases {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			args := append(append([]string{}, base...), flags...)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			before := writes.Load()
			cmd := exec.CommandContext(ctx, gh, args...)
			cmd.Dir = dir
			// Output is bounded by these fixed args/responses, contains no real secrets.
			_, _ = cmd.CombinedOutput()
			delta := writes.Load() - before
			rawWrites += delta
			if helpRequested(args) && delta != 0 {
				t.Fatal("wrapper misclassified gh write as help")
			}
			var out, errout bytes.Buffer
			code := RunShim(ctx, args, strings.NewReader(""), &out, &errout)
			if writes.Load() != before+delta {
				t.Fatal("wrapper bypassed unavailable broker using personal credentials")
			}
			if !helpRequested(args) && code == 0 {
				t.Fatal("ambiguous/write command succeeded without broker")
			}
		})
	}
	if rawWrites < 2 {
		t.Fatal("fixture did not exercise actual gh creation")
	}
	t.Logf("cases=%d rawSyntheticWrites=%d wrapperPersonalWrites=0", len(cases), rawWrites)
}
