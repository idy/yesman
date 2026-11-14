package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/idy/yesman/internal/githubapp"
	"github.com/idy/yesman/internal/server"
	"github.com/idy/yesman/internal/yesmen"
)

type githubAppTransport func(*http.Request) (*http.Response, error)

func (f githubAppTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubAppNativeImportAndVaultIsolation(t *testing.T) {
	a := newConfigApp(t, &fakeKeychain{})
	a.yesmen = yesmen.New(a.home.Yesmen(), a.home.Archive())
	if _, err := a.yesmen.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	a.githubApp = githubapp.NewManager(filepath.Join(a.home.Root, "github-app"), a.keychain, "/installed/gh")
	t.Cleanup(a.githubApp.Close)
	s := &service{app: a}
	if _, err := s.PrepareGitHubApp(""); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemData := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	keyFile := filepath.Join(t.TempDir(), "fixture private.pem")
	if err := os.WriteFile(keyFile, pemData, 0600); err != nil {
		t.Fatal(err)
	}
	originalDialog := githubAppOpenDialog
	t.Cleanup(func() { githubAppOpenDialog = originalDialog })
	githubAppOpenDialog = func(options mygo.OpenDialogOptions) ([]string, error) {
		if options.Title == "" || len(options.Filters) != 1 {
			t.Error("missing native key picker options")
		}
		return []string{keyFile}, nil
	}
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	http.DefaultTransport = githubAppTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.String() != "https://api.github.com/app" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("unexpected import authentication request")
		}
		body := `{"id":123,"slug":"yesman-fixture","name":"yesman fixture","html_url":"https://github.com/apps/yesman-fixture","owner":{"login":"idy"},"permissions":{"contents":"read","pull_requests":"write","metadata":"read"}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})
	view, err := s.ImportGitHubAppKey(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if view.App == nil || view.App.Slug != "yesman-fixture" || !view.Unlocked || view.Config.Enabled {
		t.Fatalf("unexpected public import result: %+v", view)
	}
	wire, _ := json.Marshal(view)
	if strings.Contains(string(wire), "PRIVATE KEY") || strings.Contains(string(wire), "APP_PRIVATE_KEY") {
		t.Fatal("private key exposed to frontend")
	}
	api := &server.Server{Secrets: a.vault, Variables: a.variables, GitHubApp: a.githubApp}
	env, _ := json.Marshal(api.Env())
	description, _ := json.Marshal(api.Describe())
	if strings.Contains(string(env), "APP_") || strings.Contains(string(description), "APP_") {
		t.Fatal("App vault entered agent environment")
	}
	_, exported, err := a.exportedEnv()
	if err != nil || exported != nil {
		t.Fatal("App vault entered normal configuration export")
	}
	if _, err := s.ConfigureGitHubApp(githubapp.Config{Repositories: []string{"idy/yesman"}, Workspaces: []string{"not-a-workspace"}}); err == nil {
		t.Fatal("missing workspace accepted")
	}
	view, err = s.ConfigureGitHubApp(githubapp.Config{Repositories: []string{"idy/yesman"}, Workspaces: []string{"alice"}})
	if err != nil {
		t.Fatal(err)
	}
	// Native picker cancellation leaves the saved App untouched.
	githubAppOpenDialog = func(mygo.OpenDialogOptions) ([]string, error) { return nil, nil }
	canceled, err := s.ImportGitHubAppKey(context.Background(), "123")
	if err != nil || canceled.App == nil || *canceled.App != *view.App {
		t.Fatal("picker cancel changed credentials")
	}
	if _, err := s.LockGitHubApp(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportGitHubAppKey(context.Background(), "123"); err == nil {
		t.Fatal("import while locked was accepted")
	}
	if path := os.Getenv("YESMAN_TEST_GITHUB_APP_DTO"); path != "" {
		// Public, synthetic fixture data only, for production-page browser tests.
		view.Verified = true
		view.Config.Enabled = false
		view.Config.Repositories = []string{"idy/yesman"}
		view.Config.Workspaces = []string{"alice"}
		data, _ := json.Marshal(view)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGitHubAppRegistrationLifecycle(t *testing.T) {
	a := newConfigApp(t, &fakeKeychain{})
	a.githubApp = githubapp.NewManager(filepath.Join(a.home.Root, "github-app"), a.keychain, "/installed/gh")
	t.Cleanup(func() { a.stopGitHubAppSetup(); a.githubApp.Close() })
	s := &service{app: a}
	if _, err := s.PrepareGitHubApp(""); err != nil {
		t.Fatal(err)
	}
	view, err := s.RegisterGitHubApp("idy", "yesman fixture")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(view.Setup.URL, "http://127.0.0.1:") {
		t.Fatal("registration did not bind loopback")
	}
	if _, err := s.RegisterGitHubApp("idy", "another"); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	response, err := http.Get(view.Setup.URL)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || strings.Contains(string(data), "PRIVATE KEY") {
		t.Fatal("invalid registration entry page")
	}
	canceled, err := s.CancelGitHubAppRegistration()
	if err != nil || canceled.Setup.URL != "" {
		t.Fatal("registration cancel failed")
	}
}
