package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/githubapp"
	"github.com/idy/yesman/internal/yesmen"
)

// Real service/Store/App vault and Settings DTOs; only GitHub and pi are local
// synthetic actors. The browser supplies explicit fake OS Grant reads separately.
func TestGitHubAppCombinedBrowserBridge(t *testing.T) {
	if os.Getenv("YESMAN_TEST_APP_BRIDGE") != "1" {
		t.Skip("driven by check-github-app-combination.mjs")
	}
	a := newConfigApp(t, &fakeKeychain{})
	a.yesmen = yesmen.New(a.home.Yesmen(), a.home.Archive())
	if _, err := a.yesmen.Hire("alice"); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a.agents = agent.New(agent.Config{Pi: exe, Yesmen: a.yesmen, Extension: filepath.Join(t.TempDir(), "none.ts"), Env: []string{"YESMAN_NEEDS_YOU_FAKE_PI=1"}})
	defer a.agents.StopAll()
	a.githubApp = githubapp.NewManager(filepath.Join(a.home.Root, "github-app"), githubAppKeychain{a.keychain}, "/synthetic/gh")
	defer a.githubApp.Close()
	if err := a.githubApp.Prepare(""); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	app := githubapp.Registration{ID: 123, Owner: "idy", Slug: "fixture", Name: "Fixture", URL: "https://github.com/apps/fixture"}
	if err := a.githubApp.SaveRegistration(app, private); err != nil {
		t.Fatal(err)
	}
	var denied atomic.Bool
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	http.DefaultTransport = githubAppTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" {
			return nil, fmt.Errorf("fixture refused host")
		}
		status := 200
		var value any
		if denied.Load() {
			status = 403
			value = map[string]string{"error": "synthetic revoked"}
		} else {
			switch r.URL.Path {
			case "/repos/idy/yesman/installation":
				value = map[string]any{"id": 456, "app_id": 123, "account": map[string]string{"login": "idy"}, "permissions": map[string]string{"contents": "read", "pull_requests": "write"}}
			case "/app/installations/456/access_tokens":
				value = map[string]any{"token": "ghs_synthetic_browser_installation", "expires_at": time.Now().Add(time.Hour), "permissions": map[string]string{"contents": "read", "pull_requests": "write", "metadata": "read"}, "repositories": []map[string]string{{"full_name": "idy/yesman"}}}
			case "/repos/idy/yesman":
				value = map[string]string{"full_name": "idy/yesman"}
			default:
				status = 404
				value = map[string]string{"error": "fixture scope"}
			}
		}
		b, _ := json.Marshal(value)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b)), Request: r}, nil
	})
	s := &service{app: a}
	if _, err := s.ConfigureGitHubApp(githubapp.Config{Repositories: []string{"idy/yesman"}, Workspaces: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, m := range []string{"State", "Info", "Usage", "Pulls", "Context", "Settings", "Shortcuts", "Repos", "TakeFocus", "Models", "Messages", "ModelUsage", "GitHubApp", "ConfigureGitHubApp", "TestGitHubApp", "EnableGitHubApp", "LockGitHubApp"} {
		allowed[m] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fixture/revoke", func(w http.ResponseWriter, r *http.Request) { denied.Store(true); w.WriteHeader(204) })
	mux.HandleFunc("POST /fixture/call", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string
			Args   []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad fixture request", 400)
			return
		}
		if !allowed[req.Method] {
			t.Errorf("unhandled fixture method %s", req.Method)
			http.Error(w, "unsupported fixture method", 400)
			return
		}
		method := reflect.ValueOf(s).MethodByName(req.Method)
		typ := method.Type()
		values := []reflect.Value{}
		n := 0
		for i := 0; i < typ.NumIn(); i++ {
			kind := typ.In(i)
			if kind == reflect.TypeFor[context.Context]() {
				values = append(values, reflect.ValueOf(r.Context()))
				continue
			}
			if n >= len(req.Args) {
				http.Error(w, "missing argument", 400)
				return
			}
			v := reflect.New(kind)
			if err := json.Unmarshal(req.Args[n], v.Interface()); err != nil {
				http.Error(w, "bad argument", 400)
				return
			}
			n++
			values = append(values, v.Elem())
		}
		if n != len(req.Args) {
			http.Error(w, "extra argument", 400)
			return
		}
		response := map[string]any{"value": nil}
		for _, v := range method.Call(values) {
			if v.Type().Implements(reflect.TypeFor[error]()) {
				if !v.IsNil() {
					response["error"] = v.Interface().(error).Error()
				}
			} else {
				response["value"] = v.Interface()
			}
		}
		data, _ := json.Marshal(response)
		if bytes.Contains(data, private) || strings.Contains(string(data), "ghs_synthetic_browser_installation") {
			t.Error("backend secret escaped into public DTO")
			http.Error(w, "private data refused", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	})
	web, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", http.FileServerFS(web))
	page := httptest.NewServer(mux)
	defer page.Close()
	fmt.Println("GITHUB_APP_COMBINATION_URL=" + page.URL)
	_, _ = io.Copy(io.Discard, os.Stdin)
}
