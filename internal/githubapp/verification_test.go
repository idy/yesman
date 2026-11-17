package githubapp

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

type setupKeychain struct {
	probes, sets         int
	probeError, setError error
}

func (k *setupKeychain) Get(string) ([]byte, error) {
	k.probes++
	if k.probes > 1 {
		return nil, errors.ErrUnsupported
	}
	return nil, k.probeError
}
func (k *setupKeychain) Set(string, []byte) error { k.sets++; return k.setError }
func (k *setupKeychain) Delete(string) error      { return nil }

func TestGeneratedPasswordRequiresPersistence(t *testing.T) {
	for _, storeError := range []error{errors.ErrUnsupported, errors.New("synthetic access denied")} {
		t.Run(storeError.Error(), func(t *testing.T) {
			k := &setupKeychain{probeError: os.ErrNotExist, setError: storeError}
			m := NewManager(filepath.Join(t.TempDir(), "app"), k, "/fixture/gh")
			m.vault.WorkFactor = 1
			defer m.Close()
			if err := m.Configure(Config{Repositories: []string{"idy/yesman"}}); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(m.dir, "config.json")
			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Prepare(""); err == nil {
				t.Fatal("generated setup accepted failed persistence")
			}
			if k.sets != 1 || m.Configured() || m.vault.Unlocked() {
				t.Fatal("failed generated setup was not rolled back and locked")
			}
			after, err := os.ReadFile(configPath)
			if err != nil || string(after) != string(before) {
				t.Fatal("failed setup changed scope")
			}
			// A user-known password remains recoverable without the native store.
			if err := m.Prepare("synthetic-user-password"); err != nil {
				t.Fatal(err)
			}
			m.Lock()
			keyBefore, err := os.ReadFile(m.vault.KeyFile())
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Prepare(""); err == nil {
				t.Fatal("unavailable store unlocked existing vault")
			}
			keyAfter, err := os.ReadFile(m.vault.KeyFile())
			if err != nil || string(keyAfter) != string(keyBefore) {
				t.Fatal("failed unlock replaced existing key")
			}
			if err := m.Prepare("synthetic-user-password"); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, err := range []error{errors.ErrUnsupported, errors.New("synthetic denied")} {
		k := &setupKeychain{probeError: err}
		m := NewManager(filepath.Join(t.TempDir(), "app"), k, "/fixture/gh")
		if err := m.Prepare(""); err == nil || k.sets != 0 || m.Configured() {
			t.Fatal("unavailable/failed probe treated as a missing item")
		}
		m.Close()
	}
}

func TestLatestConnectionTestOwnsVerification(t *testing.T) {
	for _, latestSucceeds := range []bool{false, true} {
		name := "older-success-latest-failure"
		if latestSucceeds {
			name = "older-failure-latest-success"
		}
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			m, app, key, _ := managerFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/idy/yesman" {
					n := requests.Add(1)
					if n == 2 {
						close(entered)
						<-release
					}
					if n == 2 && latestSucceeds || n == 3 && !latestSucceeds {
						http.Error(w, "synthetic revoked", 403)
						return
					}
				}
				connectionHandler(w, r)
			})
			defer once.Do(func() { close(release) })
			configureManager(t, m, app, key)
			if _, err := m.Test(context.Background()); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := m.Test(context.Background()); done <- err }()
			<-entered
			if v := m.View(); v.Verified || len(v.Connections) != 0 {
				t.Fatal("in-flight retest retained proof")
			}
			if err := m.Enable(true); err == nil {
				t.Fatal("enabled during retest")
			}
			_, err := m.Test(context.Background())
			if (err == nil) != latestSucceeds {
				t.Fatal("unexpected latest result", err)
			}
			once.Do(func() { close(release) })
			if err := <-done; err == nil {
				t.Fatal("superseded test succeeded")
			}
			if v := m.View(); v.Verified != latestSucceeds || (len(v.Connections) != 0) != latestSucceeds {
				t.Fatal("stale completion replaced current proof")
			}
			if err := m.Enable(true); (err == nil) != latestSucceeds {
				t.Fatal("enable disagrees with latest result")
			}
		})
	}
}

func TestCanceledRetestRevokesVerification(t *testing.T) {
	m, app, key, _ := managerFixture(t, connectionHandler)
	configureManager(t, m, app, key)
	if _, err := m.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Test(ctx); err == nil {
		t.Fatal("canceled test succeeded")
	}
	if v := m.View(); v.Verified || len(v.Connections) != 0 {
		t.Fatal("cancellation retained proof")
	}
	if err := m.Enable(true); err == nil {
		t.Fatal("canceled retest allowed enable")
	}
}
