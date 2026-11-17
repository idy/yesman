package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const fixtureGeneration = "2026-01-01T00:00:00Z"

type testKeychain struct {
	mu   sync.Mutex
	data map[string][]byte
	fail bool
}

func (k *testKeychain) Get(name string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail {
		return nil, errors.ErrUnsupported
	}
	return append([]byte(nil), k.data[name]...), nil
}
func (k *testKeychain) Set(name string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail {
		return errors.New("store failed")
	}
	k.data[name] = append([]byte(nil), value...)
	return nil
}
func (k *testKeychain) Delete(name string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.data, name)
	return nil
}

func managerFixture(t *testing.T, handler http.HandlerFunc) (*Manager, Registration, []byte, *testKeychain) {
	t.Helper()
	_, app, key := fixture(t)
	store := &testKeychain{data: map[string][]byte{}}
	manager := NewManager(filepath.Join(t.TempDir(), "github-app"), store, "/installed/gh")
	manager.vault.WorkFactor = 1
	if handler != nil {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		manager.newClient = func(a Registration, k []byte, repos []string) (*Client, error) {
			c, err := ClientFromKey(a, k, repos)
			if err == nil {
				c.base = server.URL
			}
			return c, err
		}
	}
	t.Cleanup(manager.Close)
	return manager, app, key, store
}
func connectionHandler(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/repos/idy/yesman/installation":
		writeJSON(w, installation())
	case "/app/installations/456/access_tokens":
		writeJSON(w, tokenJSON(fixtureToken, time.Now().Add(time.Hour)))
	case "/repos/idy/yesman":
		writeJSON(w, map[string]string{"full_name": "idy/yesman"})
	default:
		http.NotFound(w, r)
	}
}
func configureManager(t *testing.T, m *Manager, app Registration, key []byte) {
	t.Helper()
	if err := m.Prepare(""); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveRegistration(app, key); err != nil {
		t.Fatal(err)
	}
	if err := m.Configure(Config{Repositories: []string{"idy/yesman"}, Workspaces: []string{"alice"}, WorkspaceGenerations: map[string]string{"alice": fixtureGeneration}}); err != nil {
		t.Fatal(err)
	}
}

func TestEncryptedManagerScopeAndRestart(t *testing.T) {
	m, app, key, keychain := managerFixture(t, connectionHandler)
	if mode, err := m.Mode("alice", fixtureGeneration); err != nil || mode != "personal" {
		t.Fatal(mode, err)
	}
	configureManager(t, m, app, key)
	if err := m.Enable(true); err == nil {
		t.Fatal("enabled before a connection test")
	}
	if _, err := m.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(true); err != nil {
		t.Fatal(err)
	}
	for workspace, want := range map[string]string{"alice": "app", "bob": "personal"} {
		if mode, err := m.Mode(workspace, fixtureGeneration); err != nil || mode != want {
			t.Fatal(mode, err)
		}
	}
	if _, err := m.Create(context.Background(), "bob", fixtureGeneration, PullInput{}); err == nil {
		t.Fatal("foreign workspace used the App")
	}
	if mode, err := m.Mode("alice", "2026-02-01T00:00:00Z"); err == nil || mode != "" {
		t.Fatal("recreated workspace inherited App identity or silently became personal")
	}
	if _, err := m.Create(context.Background(), "alice", "2026-02-01T00:00:00Z", PullInput{}); err == nil {
		t.Fatal("recreated workspace used the previous generation's App")
	}
	for _, path := range []string{m.vault.KeyFile(), m.vault.File(), filepath.Join(m.dir, "config.json")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), string(key)) || strings.Contains(string(data), fixtureToken) || strings.Contains(string(data), "BEGIN RSA PRIVATE KEY") {
			t.Fatal("plaintext credential on disk")
		}
	}
	view, _ := json.Marshal(m.View())
	contextText := m.Context("alice", fixtureGeneration)
	if strings.Contains(string(view), "PRIVATE KEY") || strings.Contains(string(view), fixtureToken) || strings.Contains(contextText, fixtureToken) || strings.Contains(contextText, "APP_PRIVATE_KEY") {
		t.Fatal("credential in public view/context")
	}
	m.Lock()
	if mode, _ := m.Mode("alice", fixtureGeneration); mode != "app" {
		t.Fatal("locking downgraded identity to personal")
	}
	if _, err := m.Create(context.Background(), "alice", fixtureGeneration, PullInput{}); err == nil {
		t.Fatal("locked key accepted")
	}
	restarted := NewManager(m.dir, keychain, "/installed/gh")
	t.Cleanup(restarted.Close)
	if err := restarted.Prepare(""); err != nil {
		t.Fatal(err)
	}
	if view := restarted.View(); !view.Unlocked || view.App == nil || *view.App != app || !view.Config.Enabled {
		t.Fatalf("restart view: %+v", view)
	}
	if err := restarted.Enable(false); err != nil {
		t.Fatal(err)
	}
	if mode, _ := restarted.Mode("alice", fixtureGeneration); mode != "personal" {
		t.Fatal("explicit disable did not restore personal mode")
	}
}

func TestManagerFailedOrStaleTestCannotEnable(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	m, app, key, _ := managerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/idy/yesman" {
			close(started)
			<-release
		}
		connectionHandler(w, r)
	})
	configureManager(t, m, app, key)
	done := make(chan error, 1)
	go func() { _, err := m.Test(context.Background()); done <- err }()
	<-started
	if err := m.Configure(Config{Repositories: []string{"other/project"}, Workspaces: []string{"alice"}, WorkspaceGenerations: map[string]string{"alice": fixtureGeneration}}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("stale test passed")
	}
	if m.View().Verified {
		t.Fatal("stale test verified changed scope")
	}
	if err := m.Enable(true); err == nil {
		t.Fatal("enabled unverified scope")
	}
	if _, err := m.Test(context.Background()); err == nil {
		t.Fatal("missing installation passed")
	}
}

func TestManagerConfigCorruptionAndPasswordFallback(t *testing.T) {
	m, _, _, _ := managerFixture(t, nil)
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.dir, "config.json"), []byte("{bad"), 0600); err != nil {
		t.Fatal(err)
	}
	broken := NewManager(m.dir, nil, "/installed/gh")
	broken.vault.WorkFactor = 1
	t.Cleanup(broken.Close)
	if _, err := broken.Mode("alice", fixtureGeneration); err == nil {
		t.Fatal("corrupt config fell back to personal")
	}
	if err := broken.Prepare(""); err == nil {
		t.Fatal("created vault with no password storage")
	}
	if err := broken.Prepare("fixture-password"); err != nil {
		t.Fatal(err)
	}
	broken.Lock()
	if err := broken.Prepare("wrong-password"); err == nil {
		t.Fatal("wrong password unlocked")
	}
	if err := broken.Prepare("fixture-password"); err != nil {
		t.Fatal(err)
	}
}

func TestManagerKeyReplacementDisablesIdentity(t *testing.T) {
	m, app, key, _ := managerFixture(t, connectionHandler)
	configureManager(t, m, app, key)
	if _, err := m.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(true); err != nil {
		t.Fatal(err)
	}
	_, _, replacement := fixture(t)
	if err := m.SaveRegistration(app, replacement); err != nil {
		t.Fatal(err)
	}
	if view := m.View(); view.Config.Enabled || view.Verified {
		t.Fatal("replacement inherited previous enable/test")
	}
}
