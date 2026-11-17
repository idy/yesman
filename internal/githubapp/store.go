// Package githubapp keeps per-user GitHub App credentials out of agent environments.
package githubapp

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Registration contains public metadata only. The key is a separate private file.
type Registration struct {
	ID       int64  `json:"id"`
	ClientID string `json:"client_id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Owner    string `json:"owner"`
	URL      string `json:"html_url"`
}

// StoreRegistration never replaces an existing registration or follows a target
// symlink. Its directory is private, outside any workspace or export bundle.
func StoreRegistration(dir string, app Registration, key []byte) error {
	if runtime.GOOS == "windows" {
		return errors.New("private GitHub App file storage needs Windows credential-store integration")
	}
	if _, err := parseKey(key); err != nil {
		return err
	}
	if app.ID <= 0 || app.Slug == "" || app.Owner == "" {
		return errors.New("incomplete GitHub App registration")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return fmt.Errorf("create private GitHub App directory: %w", err)
	}
	// Deliberately retain partially written credentials on disk on failure:
	// replacing them or retrying an already-consumed manifest would lose a key.
	if err := privateWrite(filepath.Join(dir, "private-key.pem"), key); err != nil {
		return err
	}
	data, err := json.MarshalIndent(app, "", "  ")
	if err != nil {
		return err
	}
	return privateWrite(filepath.Join(dir, "app.json"), append(data, '\n'))
}

func privateWrite(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create private GitHub App file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return errors.New("write private GitHub App file failed")
	}
	if err := f.Sync(); err != nil {
		return errors.New("sync private GitHub App file failed")
	}
	return nil
}

func privateRead(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > max {
		return nil, errors.New("GitHub App credentials must be bounded regular files")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("GitHub App credentials must be readable only by their owner (0600)")
	}
	return os.ReadFile(path)
}

func parseKey(data []byte) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || len(rest) != 0 {
		return nil, errors.New("GitHub App key must contain one PEM private key")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, _ = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		v, _ := x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = v.(*rsa.PrivateKey)
	}
	if key == nil || key.N.BitLen() < 2048 {
		return nil, errors.New("GitHub App key must be an RSA private key of at least 2048 bits")
	}
	if err := key.Validate(); err != nil {
		return nil, errors.New("invalid GitHub App private key")
	}
	return key, nil
}

// Load reads a registration from a dedicated private directory. It never uses
// GH_TOKEN, a personal gh login, or the app's shell Secrets vault as a fallback.
func Load(dir string) (Registration, *rsa.PrivateKey, error) {
	var app Registration
	if runtime.GOOS == "windows" {
		return app, nil, errors.New("private GitHub App file storage needs Windows credential-store integration")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return app, nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return app, nil, errors.New("GitHub App directory must be private (0700) and not a symlink")
	}
	data, err := privateRead(filepath.Join(dir, "app.json"), 16384)
	if err != nil {
		return app, nil, err
	}
	if err := json.Unmarshal(data, &app); err != nil {
		return app, nil, errors.New("invalid GitHub App metadata")
	}
	if app.ID <= 0 || app.Slug == "" || app.Owner == "" {
		return app, nil, errors.New("incomplete GitHub App metadata")
	}
	data, err = privateRead(filepath.Join(dir, "private-key.pem"), 16384)
	if err != nil {
		return app, nil, err
	}
	key, err := parseKey(data)
	return app, key, err
}
