package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (string, Registration, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	app := Registration{ID: 123, ClientID: "Iv1.test", Slug: "yesman-test", Name: "yesman test", Owner: "idy", URL: "https://github.com/apps/yesman-test"}
	return filepath.Join(t.TempDir(), "registration"), app, encoded
}

func clientFixture(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	_, app, key := fixture(t)
	private, err := parseKey(key)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newClient(app, private, []string{"idy/yesman", "other/project"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client.base = server.URL
	return client
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func installation() map[string]any {
	return map[string]any{"id": 456, "app_id": 123, "account": map[string]string{"login": "idy"}, "permissions": map[string]string{"contents": "read", "pull_requests": "write"}}
}
func tokenJSON(value string, expiry time.Time) map[string]any {
	return map[string]any{"token": value, "expires_at": expiry, "permissions": map[string]string{"contents": "read", "pull_requests": "write", "metadata": "read"}, "repositories": []map[string]string{{"full_name": "idy/yesman"}}}
}

const fixtureToken = "ghs_this_is_a_mock_installation_token"

func TestPrivateStorage(t *testing.T) {
	dir, app, key := fixture(t)
	if runtime.GOOS == "windows" {
		if err := StoreRegistration(dir, app, key); err == nil {
			t.Fatal("unenforced Windows privacy was accepted")
		}
		return
	}
	if err := StoreRegistration(dir, app, key); err != nil {
		t.Fatal(err)
	}
	stored, _, err := Load(dir)
	if err != nil || stored != app {
		t.Fatalf("Load = %v, %v", stored, err)
	}
	if err := StoreRegistration(dir, app, key); err == nil {
		t.Fatal("overwrote an existing registration")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "app.json")); strings.Contains(string(data), "PRIVATE KEY") {
		t.Fatal("key in metadata")
	}
	if err := os.Chmod(filepath.Join(dir, "private-key.pem"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(dir); err == nil {
		t.Fatal("accepted world-readable key")
	}
	if err := os.Chmod(filepath.Join(dir, "private-key.pem"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(link); err == nil {
		t.Fatal("accepted linked directory")
	}
}

func TestRegistrationCallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("standalone registration fails closed on Windows until credential-store integration")
	}
	dir, app, key := fixture(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/app-manifests/valid_code_123/conversions" {
			t.Error("wrong exchange request")
		}
		w.WriteHeader(201)
		writeJSON(w, map[string]any{"id": app.ID, "client_id": app.ClientID, "slug": app.Slug, "name": app.Name, "html_url": app.URL, "pem": string(key), "client_secret": "unused-client-secret", "webhook_secret": "unused-webhook-secret", "owner": map[string]string{"login": "idy", "type": "User"}, "permissions": map[string]string{"contents": "read", "pull_requests": "write", "metadata": "read"}})
	}))
	defer upstream.Close()
	s := NewRegistrationServer("idy", "yesman by idy", dir)
	s.base = upstream.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entry, stop, err := s.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, query := range []string{"code=valid_code_123", "code=valid_code_123&state=wrong", "code=valid_code_123&state=" + s.state + "&state=" + s.state, "code=../bad&state=" + s.state} {
		resp, err := http.Get(entry + "callback?" + query)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("bad callback = %d", resp.StatusCode)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("bad state exchanged code")
	}
	request, _ := http.NewRequest("GET", entry, nil)
	request.Host = "evil.example"
	resp, _ := http.DefaultClient.Do(request)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatal("accepted DNS rebinding host")
	}
	resp, err = http.Get(entry)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "Continue to GitHub") || strings.Contains(string(page), "PRIVATE KEY") {
		t.Fatal("wrong registration page")
	}
	resp, err = http.Get(entry + "callback?code=valid_code_123&state=" + url.QueryEscape(s.state))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.Contains(string(body), "PRIVATE KEY") || strings.Contains(string(body), "secret") {
		t.Fatal("callback response leaked or failed")
	}
	result := <-s.Results()
	if result.Err != nil || result.App != app {
		t.Fatalf("registration = %v", result.Err)
	}
	resp, _ = http.Get(entry + "callback?code=valid_code_123&state=" + s.state)
	resp.Body.Close()
	if resp.StatusCode != 409 || calls.Load() != 1 {
		t.Fatal("callback replay was not rejected")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatal("saved unnecessary OAuth or webhook credentials")
	}
}

func TestRegistrationExpiryAndErrorRedaction(t *testing.T) {
	dir, _, _ := fixture(t)
	s := NewRegistrationServer("idy", "test", dir)
	s.host, s.state, s.expires = "127.0.0.1:3456", "state", time.Now().Add(-time.Second)
	r := httptest.NewRequest("GET", "http://127.0.0.1:3456/", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 410 {
		t.Fatal("expired registration remained available")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "secret reflected: "+r.URL.Path, 500) }))
	defer upstream.Close()
	s.base = upstream.URL
	_, err := s.exchange(context.Background(), "a_secret_code")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error not sanitized: %v", err)
	}
}

func TestConcurrentMintScopeAndRefresh(t *testing.T) {
	var mints atomic.Int32
	gate := make(chan struct{})
	now := time.Now().UTC()
	client := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/idy/yesman/installation":
			writeJSON(w, installation())
		case "/app/installations/456/access_tokens":
			count := mints.Add(1)
			var body struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Repositories) != 1 || body.Repositories[0] != "yesman" || !permissionsMatch(body.Permissions) {
				t.Error("mint was not scoped")
			}
			if count == 1 {
				<-gate
			}
			writeJSON(w, tokenJSON(fmt.Sprintf("%s_%d", fixtureToken, count), now.Add(time.Hour)))
		default:
			t.Error("unexpected request")
			http.NotFound(w, r)
		}
	})
	client.now = func() time.Time { return now }
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			token, err := client.token(context.Background(), "idy/yesman")
			if err != nil || token.value == "" {
				t.Errorf("token: %v", err)
			}
		})
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.token(cancelCtx, "idy/yesman"); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored cancellation")
	}
	close(gate)
	wg.Wait()
	if mints.Load() != 1 {
		t.Fatalf("concurrent mints: %d", mints.Load())
	}
	old, _ := client.token(context.Background(), "idy/yesman")
	if _, err := client.token(context.Background(), "unconfigured/repo"); err == nil {
		t.Fatal("unconfigured repository was accepted")
	}
	if _, err := client.token(context.Background(), "idy/../tokens"); err == nil {
		t.Fatal("path traversal was accepted")
	}
	now = now.Add(59*time.Minute + time.Second)
	fresh, err := client.token(context.Background(), "idy/yesman")
	if err != nil || fresh.value == old.value || mints.Load() != 2 {
		t.Fatalf("refresh: %v", err)
	}
	client.invalidate("idy/yesman", old)
	again, _ := client.token(context.Background(), "idy/yesman")
	if again.value != fresh.value || mints.Load() != 2 {
		t.Fatal("late invalidation evicted new token")
	}
}

func TestCreatePullUsesAppAndRetriesOnlyAuthentication(t *testing.T) {
	var mints, creates atomic.Int32
	client := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/idy/yesman/installation":
			writeJSON(w, installation())
		case "/app/installations/456/access_tokens":
			mints.Add(1)
			writeJSON(w, tokenJSON(fixtureToken, time.Now().Add(time.Hour)))
		case "/repos/idy/yesman/pulls":
			if r.Header.Get("Authorization") != "Bearer "+fixtureToken {
				t.Error("not installation authentication")
			}
			if creates.Add(1) == 1 {
				http.Error(w, "revoked "+fixtureToken, 401)
				return
			}
			var input map[string]any
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input["draft"] != true || input["head"] != "test/app-identity" {
				t.Error("wrong PR request")
			}
			w.WriteHeader(201)
			writeJSON(w, map[string]any{"number": 77, "html_url": "https://github.com/idy/yesman/pull/77", "draft": true, "user": map[string]string{"login": "yesman-test[bot]", "type": "Bot", "avatar_url": "https://avatars.githubusercontent.com/mock"}})
		default:
			http.NotFound(w, r)
		}
	})
	pull, err := client.CreatePull(context.Background(), PullInput{Repository: "idy/yesman", Head: "test/app-identity", Base: "main", Title: "test: GitHub App identity smoke test", Draft: true})
	if err != nil || pull.Number != 77 || creates.Load() != 2 || mints.Load() != 2 {
		t.Fatalf("create: %v %+v", err, pull)
	}
}

func TestDenialsDoNotFallbackOrLeak(t *testing.T) {
	for _, status := range []int{403, 404, 422, 500, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			client := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "http://127.0.0.1:1/steal")
				http.Error(w, "reflected "+r.Header.Get("Authorization"), status)
			})
			_, err := client.Test(context.Background(), "idy/yesman")
			if err == nil || strings.Contains(err.Error(), "Bearer") || calls.Load() != 1 {
				t.Fatalf("denial: %v, calls %d", err, calls.Load())
			}
		})
	}
}

func TestRejectBroadTokenAndForeignInstallation(t *testing.T) {
	for _, mode := range []string{"broad", "permissions", "short", "owner", "suspended", "appid"} {
		t.Run(mode, func(t *testing.T) {
			client := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/installation") {
					i := installation()
					switch mode {
					case "owner":
						i["account"] = map[string]string{"login": "elsewhere"}
					case "suspended":
						i["suspended_at"] = "2026-01-01"
					case "appid":
						i["app_id"] = 789
					}
					writeJSON(w, i)
					return
				}
				v := tokenJSON(fixtureToken, time.Now().Add(time.Hour))
				switch mode {
				case "broad":
					v["repositories"] = []map[string]string{{"full_name": "idy/yesman"}, {"full_name": "idy/other"}}
				case "permissions":
					v["permissions"] = map[string]string{"contents": "write", "pull_requests": "write"}
				case "short":
					v["expires_at"] = time.Now().Add(10 * time.Second)
				}
				writeJSON(w, v)
			})
			if _, err := client.token(context.Background(), "idy/yesman"); err == nil {
				t.Fatal("unsafe installation/token was accepted")
			}
		})
	}
}
