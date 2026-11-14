//go:build windows

package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/idy/yesman/internal/githubapp"
	"github.com/idy/yesman/internal/secrets"
)

// The real Windows Credential Manager is exercised only in disposable CI,
// under a new service/account namespace. It never reads or replaces user keys.
func TestGitHubAppWindowsCredentialManager(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" {
		t.Skip("native credential-store fixture is limited to disposable GitHub-hosted runners")
	}
	suffix := make([]byte, 16)
	_, _ = rand.Read(suffix)
	native := mygo.Secrets.Service("yesman.github-app-fixture." + hex.EncodeToString(suffix))
	dir := filepath.Join(t.TempDir(), "app vault")
	manager := githubapp.NewManager(dir, native, `C:\fixture\gh.exe`)
	t.Cleanup(manager.Close)
	t.Cleanup(func() {
		recipients, err := secrets.NewVault(filepath.Join(dir, "vault")).Recipients()
		if err == nil {
			for _, recipient := range recipients {
				if err := native.Delete("github-app-password/" + recipient); err != nil {
					t.Error("remove fixture credential:", err)
				}
			}
		}
	})
	if err := manager.Prepare(""); err != nil {
		t.Fatal("create vault with native credential password:", err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	app := githubapp.Registration{ID: 123, Owner: "idy", Slug: "yesman-fixture", Name: "yesman fixture", URL: "https://github.com/apps/yesman-fixture"}
	if err := manager.SaveRegistration(app, data); err != nil {
		t.Fatal(err)
	}
	manager.Lock()
	restarted := githubapp.NewManager(dir, native, `C:\fixture\gh.exe`)
	t.Cleanup(restarted.Close)
	if err := restarted.Prepare(""); err != nil {
		t.Fatal("native credential auto-unlock:", err)
	}
	view := restarted.View()
	if !view.Unlocked || view.App == nil || *view.App != app {
		t.Fatal("native credential store did not restore the App vault")
	}
}

// A GUI-subsystem executable still has to behave as a normal console command
// when yesman installs it as the private gh wrapper. This uses the production
// main, including its early dispatch before MyGo startup, in a path with spaces.
func TestGitHubAppWindowsGUIWrapper(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" {
		t.Skip("production Windows subprocess fixture is limited to disposable GitHub-hosted runners")
	}
	realGH, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "Yes Man.exe")
	build := exec.Command("go", "build", "-ldflags=-H=windowsgui", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build GUI-subsystem fixture: %v: %s", err, out)
	}
	shim, err := githubapp.InstallShim(filepath.Join(dir, "runtime with spaces"), binary)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-worker" {
			t.Error("wrong wrapper caller")
			http.Error(w, "refused", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/github-app/mode":
			_ = json.NewEncoder(w).Encode(map[string]string{"mode": "app"})
		case "/v1/github-app/pulls":
			var in githubapp.PullInput
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.Title != "test: spaces & 中文" || in.Head != "test/windows" {
				t.Error("Windows argv changed")
				http.Error(w, "bad request", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 77, "html_url": "https://github.com/idy/yesman/pull/77", "user": map[string]string{"type": "Bot", "login": "yesman-fixture[bot]"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	env := []string{"YESMAN_GH_REAL=" + realGH, "YESMAN_API=" + api.URL, "YESMAN_TOKEN=fixture-worker", "HOME=" + dir, "USERPROFILE=" + dir, "GH_CONFIG_DIR=" + dir, "GH_NO_UPDATE_NOTIFIER=1"}
	for _, name := range []string{"PATH", "SystemRoot", "WINDIR", "TMP", "TEMP", "COMSPEC", "PATHEXT"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	for _, args := range [][]string{{"--version"}, {"pr", "create", "-R", "idy/yesman", "-H", "test/windows", "-B", "main", "-t", "test: spaces & 中文", "-b", "fixture", "--draft"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, filepath.Join(shim, "gh.exe"), args...)
		cmd.Env = env
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("GUI wrapper subprocess: %v: %s", err, out)
		}
		if len(args) == 1 {
			if !strings.Contains(string(out), "gh version") {
				t.Fatal("GUI wrapper lost stdout")
			}
		} else if !strings.Contains(string(out), "https://github.com/idy/yesman/pull/77") || !strings.Contains(string(out), "yesman-fixture[bot]") {
			t.Fatal("GUI wrapper did not report the verified App result")
		}
	}
}
