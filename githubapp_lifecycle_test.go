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
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/githubapp"
)

type closeNotice struct {
	io.Reader
	closed chan struct{}
}

func (n *closeNotice) Close() error { close(n.closed); return nil }

func TestGitHubAppCanceledCallbackCannotReplaceNewRegistration(t *testing.T) {
	a := newConfigApp(t, &fakeKeychain{})
	a.githubApp = githubapp.NewManager(filepath.Join(a.home.Root, "github-app"), a.keychain, "/installed/gh")
	t.Cleanup(func() { a.stopGitHubAppSetup(); a.githubApp.Close() })
	s := &service{app: a}
	if _, err := s.PrepareGitHubApp(""); err != nil {
		t.Fatal(err)
	}
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(map[string]any{"id": 123, "slug": "old-fixture", "name": "old fixture", "html_url": "https://github.com/apps/old-fixture", "pem": string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(private)})), "owner": map[string]string{"login": "idy", "type": "User"}, "permissions": map[string]string{"contents": "read", "pull_requests": "write", "metadata": "read"}})
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = githubAppTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() != "api.github.com" {
			return original.RoundTrip(r)
		}
		if !strings.HasPrefix(r.URL.Path, "/app-manifests/") {
			t.Error("unexpected external request")
		}
		close(started)
		<-release
		return &http.Response{StatusCode: 201, Header: make(http.Header), Request: r, Body: &closeNotice{Reader: strings.NewReader(string(wire)), closed: finished}}, nil
	})
	first, err := s.RegisterGitHubApp("idy", "old fixture")
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(first.Setup.URL)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(response.Body)
	response.Body.Close()
	match := regexp.MustCompile(`state=([A-Za-z0-9_-]+)`).FindStringSubmatch(string(page))
	if len(match) != 2 {
		t.Fatal("no form state")
	}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		request, _ := http.NewRequestWithContext(context.Background(), "GET", first.Setup.URL+"callback?code=fixture_code_123&state="+match[1], nil)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			response.Body.Close()
		}
	}()
	<-started
	if _, err := s.CancelGitHubAppRegistration(); err != nil {
		t.Fatal(err)
	}
	second, err := s.RegisterGitHubApp("idy", "new fixture")
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	<-finished
	<-requestDone
	current, err := s.GitHubApp()
	if err != nil {
		t.Fatal(err)
	}
	if current.App != nil || current.Setup.URL != second.Setup.URL || current.Setup.Complete {
		t.Fatal("late canceled callback changed the new registration or stored its key")
	}
}
