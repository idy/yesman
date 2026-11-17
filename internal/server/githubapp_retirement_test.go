package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/githubapp"
	"github.com/idy/yesman/internal/yesmen"
)

type appRetirementTransport func(*http.Request) (*http.Response, error)

func (f appRetirementTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type appGateBody struct {
	r                io.Reader
	entered, release chan struct{}
	once             sync.Once
}

func (r *appGateBody) Read(b []byte) (int, error) {
	r.once.Do(func() { close(r.entered); <-r.release })
	return r.r.Read(b)
}
func (r *appGateBody) Close() error { return nil }

// Real management tokens, Store Archive/rehire, App Manager and child process;
// all profiles/PEM/tokens/network endpoints below are disposable fixtures.
func TestGitHubAppRetirementDuringCreate(t *testing.T) {
	d := t.TempDir()
	src, child := filepath.Join(d, "child.go"), filepath.Join(d, "child")
	if runtime.GOOS == "windows" {
		child += ".exe"
	}
	code := `package main
import("os";"encoding/json";"bufio";"io";"net/http")
func main(){if p:=os.Getenv("APP_TEST_TOKEN_FILE");p!=""{os.WriteFile(p,[]byte(os.Getenv("YESMAN_TOKEN")),0600);s:=bufio.NewScanner(os.Stdin);for s.Scan(){var c map[string]any;json.Unmarshal(s.Bytes(),&c);json.NewEncoder(os.Stdout).Encode(map[string]any{"type":"response","id":c["id"],"command":c["type"],"success":true,"data":map[string]any{"messages":[]any{},"model":map[string]any{"id":"m","provider":"local"},"thinkingLevel":"off","isStreaming":false}})};return};for i,a:=range os.Args{if a=="--title"{os.WriteFile(os.Args[i+1],[]byte("synthetic-gh-executed"),0600)}};b,_:=io.ReadAll(os.Stdin);if len(b)>0{http.Get(string(b))};os.Stdout.WriteString("https://github.com/idy/yesman/pull/77\n")}`
	if err := os.WriteFile(src, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("go", "build", "-o", child, src).CombinedOutput(); err != nil {
		t.Fatalf("build %v %s", err, b)
	}
	for _, phase := range []string{"body", "authentication", "running-child"} {
		t.Run(phase, func(t *testing.T) {
			d := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			store := yesmen.New(filepath.Join(d, "yesmen"), filepath.Join(d, "archive"))
			y, err := store.Hire("alice")
			if err != nil {
				t.Fatal(err)
			}
			tokenFile := filepath.Join(d, "local-token")
			agents := agent.New(agent.Config{Pi: child, Extension: filepath.Join(d, "none.ts"), Env: []string{"APP_TEST_TOKEN_FILE=" + tokenFile}, Yesmen: store})
			defer agents.StopAll()
			if err := agents.Prompt(ctx, "alice/lead", "synthetic"); err != nil {
				t.Fatal(err)
			}
			local, err := os.ReadFile(tokenFile)
			if err != nil {
				t.Fatal(err)
			}
			m := githubapp.NewManager(filepath.Join(d, "app-vault"), nil, child)
			defer m.Close()
			if err := m.Prepare("synthetic-password"); err != nil {
				t.Fatal(err)
			}
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			app := githubapp.Registration{ID: 123, Slug: "retirement-fixture", Owner: "idy", URL: "https://github.com/apps/retirement-fixture"}
			if err := m.SaveRegistration(app, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})); err != nil {
				t.Fatal(err)
			}
			var armed atomic.Bool
			original := http.DefaultTransport
			defer func() { http.DefaultTransport = original }()
			http.DefaultTransport = appRetirementTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.github.com" {
					t.Error("wrong host")
					return nil, context.Canceled
				}
				if phase == "authentication" && armed.Load() && r.URL.Path == "/repos/idy/yesman" {
					close(entered)
					select {
					case <-r.Context().Done():
						return nil, r.Context().Err()
					case <-release:
					}
				}
				var v any
				switch r.URL.Path {
				case "/repos/idy/yesman/installation":
					v = map[string]any{"id": 456, "app_id": 123, "account": map[string]string{"login": "idy"}, "permissions": map[string]string{"contents": "read", "pull_requests": "write"}}
				case "/app/installations/456/access_tokens":
					v = map[string]any{"token": "ghs_retirement_synthetic_installation_only", "expires_at": time.Now().Add(time.Hour), "permissions": map[string]string{"contents": "read", "pull_requests": "write", "metadata": "read"}, "repositories": []map[string]string{{"full_name": "idy/yesman"}}}
				case "/repos/idy/yesman":
					v = map[string]string{"full_name": "idy/yesman"}
				case "/repos/idy/yesman/pulls/77":
					v = map[string]any{"number": 77, "user": map[string]string{"type": "Bot", "login": "retirement-fixture[bot]"}}
				default:
					t.Errorf("wrong path %s", r.URL.Path)
				}
				b, _ := json.Marshal(v)
				return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
			})
			if err := m.Configure(githubapp.Config{Repositories: []string{"idy/yesman"}, Workspaces: []string{"alice"}, WorkspaceGenerations: map[string]string{"alice": y.HiredAt.UTC().Format(time.RFC3339Nano)}}); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Test(ctx); err != nil {
				t.Fatal(err)
			}
			if err := m.Enable(true); err != nil {
				t.Fatal(err)
			}
			armed.Store(true)
			s := &Server{Agents: agents, Yesmen: store, GitHubApp: m}
			audit := filepath.Join(d, "write-audit")
			in := githubapp.PullInput{Repository: "idy/yesman", Head: "fixture", Base: "main", Title: audit}
			if phase == "running-child" {
				childGate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					close(entered)
					select {
					case <-r.Context().Done():
					case <-release:
					}
				}))
				defer func() { once.Do(func() { close(release) }); childGate.Close() }()
				in.Body = childGate.URL
			}
			data, _ := json.Marshal(in)
			var body io.Reader = strings.NewReader(string(data))
			if phase == "body" {
				body = &appGateBody{r: body, entered: entered, release: release}
			}
			req := httptest.NewRequest("POST", "/v1/github-app/pulls", body).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+string(local))
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); s.Handler().ServeHTTP(rec, req) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("request did not reach gate")
			}
			_, err = store.Archive("alice", func(yesmen.Yesman) error { return agents.StopYesmanContext(ctx, "alice") })
			if err != nil {
				t.Fatal(err)
			}
			replacement, err := store.Hire("alice")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = agents.Authenticate(string(local)); err == nil {
				t.Fatal("old credential still valid")
			}
			if _, err = m.Mode("alice", replacement.HiredAt.UTC().Format(time.RFC3339Nano)); err == nil {
				t.Fatal("new generation inherited scope")
			}
			once.Do(func() { close(release) })
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("old request never completed")
			}
			_, statErr := os.Stat(audit)
			if (statErr == nil) != (phase == "running-child") {
				t.Fatal("child dispatched after retirement or fixture never ran")
			}
			if rec.Code == 200 {
				t.Fatal("retired operation reported success")
			}
			t.Logf("phase=%s retiredHTTPStatus=%d childStartedBeforeRetirement=%v", phase, rec.Code, statErr == nil)
		})
	}
}
