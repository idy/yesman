//go:build windows

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
	"github.com/idy/yesman/internal/githubapp"
	"github.com/idy/yesman/internal/secrets"
)

// The real Windows Credential Manager is exercised only in disposable CI,
// under a new service/account namespace. It never reads or replaces user keys.
func TestGitHubAppWindowsCredentialManager(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("native credential-store fixture is limited to disposable GitHub Actions runners")
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
