package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo"

	"github.com/idy/yesman/internal/home"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/secrets"
)

// fakeKeychain is a credential store in memory, or none when unsupported.
type fakeKeychain struct {
	mu          sync.Mutex
	m           map[string][]byte
	unsupported bool
}

func (k *fakeKeychain) Get(key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.unsupported {
		return nil, errors.ErrUnsupported
	}
	v, ok := k.m[key]
	if !ok {
		return nil, mygo.ErrSecretNotFound
	}
	return v, nil
}

func (k *fakeKeychain) Set(key string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.unsupported {
		return errors.ErrUnsupported
	}
	if k.m == nil {
		k.m = map[string][]byte{}
	}
	k.m[key] = value
	return nil
}

func (k *fakeKeychain) Delete(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, key)
	return nil
}

// newConfigApp is an app with an empty pool, its variables and secrets,
// and kc as its credential store.
func newConfigApp(t *testing.T, kc keychain) *app {
	t.Helper()
	h := home.Home{Root: filepath.Join(t.TempDir(), "home")}
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	p := pool.New(h.Repos())
	v := secrets.NewVault(h.Secrets())
	v.WorkFactor = 10
	a := &app{home: h, pool: p, vault: v, variables: secrets.NewVariables(h.Variables()), keychain: kc}
	a.fetcher = newFetcher(p, nil)
	a.clones = newClones(func(context.Context, string) (pool.Repo, error) { return pool.Repo{}, nil }, nil)
	return a
}

func TestSecretsPasswordInTheKeychain(t *testing.T) {
	kc := &fakeKeychain{}
	a := newConfigApp(t, kc)
	s := &service{app: a}
	view, err := s.SetUpSecrets("")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Unlocked || !view.Keychain || !view.Stored {
		t.Fatalf("after setting up: %+v", view)
	}
	password, recipient := a.storedPassword()
	if len(password) < 40 || !strings.HasPrefix(recipient, "age1") {
		t.Errorf("stored %q for %q", password, recipient)
	}
	if _, err := s.SaveSecrets([]secrets.Entry{{Name: "GH_TOKEN", Value: "ghp_secret"}}); err != nil {
		t.Fatal(err)
	}

	// Locked, as after a restart: the keychain unlocks them, by the button
	// or when the app starts.
	s.LockSecrets()
	if view, err := s.UnlockSecrets(""); err != nil || !view.Unlocked || view.Entries[0].Value != "ghp_secret" {
		t.Fatalf("unlocking from the keychain: %+v, %v", view, err)
	}
	a.vault.Lock()
	if !a.unlockFromKeychain() {
		t.Error("the app did not unlock the secrets when it started")
	}

	// A keychain without the password needs it typed, and keeps it then.
	a.vault.Lock()
	kc.m = nil
	if _, err := s.UnlockSecrets(""); err == nil {
		t.Error("unlocked without a password")
	}
	if _, err := s.UnlockSecrets(password); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.storedPassword(); got != password {
		t.Errorf("the typed password was not kept: %q", got)
	}
}

func TestSecretsWithoutAKeychain(t *testing.T) {
	a := newConfigApp(t, &fakeKeychain{unsupported: true})
	s := &service{app: a}
	if _, err := s.SetUpSecrets(""); err == nil {
		t.Fatal("set up with a random password nothing keeps")
	}
	if a.vault.Configured() {
		t.Fatal("a vault was left set up")
	}
	view, err := s.SetUpSecrets("chosen password")
	if err != nil {
		t.Fatal(err)
	}
	if view.Keychain || view.Stored || !view.Unlocked {
		t.Errorf("view: %+v", view)
	}
}

func TestConfigCarriesVariablesAndSecrets(t *testing.T) {
	ctx := context.Background()
	from := newConfigApp(t, &fakeKeychain{})
	if err := from.setUpSecrets(""); err != nil {
		t.Fatal(err)
	}
	if err := from.vault.Save([]secrets.Entry{{Name: "GH_TOKEN", Value: "ghp_secret", Description: "GitHub"}}); err != nil {
		t.Fatal(err)
	}
	if err := from.variables.Save([]secrets.Entry{{Name: "REGION", Value: "eu", Description: "multi\nline"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "yesman.toml")
	if err := os.WriteFile(path, []byte("an older export, readable by others\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := from.writeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Variables != 1 || res.Secrets == nil || *res.Secrets != 1 || !res.Password {
		t.Errorf("export: %+v", res)
	}
	data, _ := os.ReadFile(path)
	password, _ := from.storedPassword()
	for _, want := range []string{"version = 2", "[[variables]]", `name = "REGION"`, "[secrets]", "password = " + tomlString(password), "-----BEGIN AGE ENCRYPTED FILE-----", "keep it private"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the export lacks %q:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "ghp_secret") {
		t.Error("a secret's value is in the clear")
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows" {
		t.Errorf("the export is readable by others: %v", info.Mode())
	}

	// Another computer, without secrets yet: the file's are set up there,
	// unlocked, with the password kept in its keychain.
	kc := &fakeKeychain{}
	to := newConfigApp(t, kc)
	plan, err := to.previewImport(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Env.Secrets == nil || !plan.Env.Secrets.Restore || !plan.Env.Secrets.Password || plan.Env.Variables == nil || len(plan.Env.Variables.Add) != 1 {
		t.Fatalf("preview: %+v", plan.Env)
	}
	got, err := to.importConfig(ctx, path, importMerge)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Env.Restored || got.Env.Locked || *got.Env.Secrets != 1 || *got.Env.Variables != 1 {
		t.Errorf("import: %+v", got.Env)
	}
	entries, err := to.vault.Entries()
	if err != nil || !reflect.DeepEqual(entries, []secrets.Entry{{Name: "GH_TOKEN", Value: "ghp_secret", Description: "GitHub"}}) {
		t.Errorf("secrets there: %#v, %v", entries, err)
	}
	if vars, _ := to.variables.Entries(); len(vars) != 1 || vars[0].Description != "multi\nline" {
		t.Errorf("variables there: %#v", vars)
	}
	if p, _ := to.storedPassword(); p != password {
		t.Errorf("its keychain keeps %q", p)
	}

	// Secrets there already: the file's join them, opened with its password.
	if err := to.vault.Save([]secrets.Entry{{Name: "GH_TOKEN", Value: "changed"}, {Name: "LOCAL", Value: "x"}}); err != nil {
		t.Fatal(err)
	}
	plan, err = to.previewImport(ctx, path)
	if err != nil || plan.Env.Secrets.Plan == nil || len(plan.Env.Secrets.Plan.Change) != 1 || len(plan.Env.Secrets.Plan.Extra) != 1 {
		t.Fatalf("preview over secrets: %+v, %v", plan.Env.Secrets, err)
	}
	if _, err := to.importConfig(ctx, path, importReplace); err != nil {
		t.Fatal(err)
	}
	if entries, _ := to.vault.Entries(); len(entries) != 1 || entries[0].Value != "ghp_secret" {
		t.Errorf("after replacing: %#v", entries)
	}

	// A file whose variable a secret here is named as stops before anything changes.
	if err := to.variables.Save(nil); err != nil {
		t.Fatal(err)
	}
	clash := filepath.Join(t.TempDir(), "clash.toml")
	if err := os.WriteFile(clash, []byte("version = 2\nappearance = \"dark\"\n[[variables]]\nname = \"GH_TOKEN\"\nvalue = \"v\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if plan, err := to.previewImport(ctx, clash); err != nil || len(plan.Env.Clash) != 1 {
		t.Errorf("preview of a clash: %+v, %v", plan.Env, err)
	}
	if _, err := to.importConfig(ctx, clash, importMerge); err == nil || to.settings.Appearance == mygo.ThemeDark {
		t.Errorf("a clash imported: %v, %+v", err, to.settings)
	}

	// A file of version 1 has neither: those here stay, replacing too.
	old := filepath.Join(t.TempDir(), "old.toml")
	if err := os.WriteFile(old, []byte("version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := to.importConfig(ctx, old, importReplace); err != nil {
		t.Fatal(err)
	}
	if names, _ := to.vault.Names(); len(names) != 1 {
		t.Errorf("replacing with a file without secrets removed them: %v", names)
	}
}
