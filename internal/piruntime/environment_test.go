package piruntime

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idy/yesman/internal/githubapp"
)

func TestInstallerEnvironmentBoundary(t *testing.T) {
	in := []string{"Path=fixture-path", "HOME=fixture-home", "SystemRoot=fixture-system", "https_proxy=fixture-proxy", "npm_config_cafile=fixture-ca", "NPM_CONFIG_REGISTRY=fixture-registry", "NODE_EXTRA_CA_CERTS=fixture-certs"}
	bad := []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "OPENAI_API_KEY", "APP_PRIVATE_KEY", "SOPS_AGE_KEY", "YESMAN_TOKEN", "YESMAN_API", "NODE_OPTIONS", "YESMAN_RUNTIME_NPM", "npm_config_cache"}
	for _, k := range bad {
		in = append(in, k+"=synthetic-secret")
	}
	got := installerEnvironment(in)
	if len(got) != 7 || strings.Contains(strings.Join(got, "\n"), "synthetic-secret") {
		t.Fatal("installer leaked credential or lost OS/registry configuration")
	}
	if !strings.Contains(strings.Join(in, "\n"), "GH_TOKEN=synthetic-secret") {
		t.Fatal("parent credential changed")
	}
}

// Opt-in network acceptance: a loaded synthetic App vault -> real Go preparer
// -> actual install.mjs -> audited Node shim -> real npm. The immutable runtime
// can then drive the normal production pi/agent fixtures, without another build.
func TestAppVaultWithActualManagedInstaller(t *testing.T) {
	parent := os.Getenv("YESMAN_TEST_APP_RUNTIME_ROOT")
	if parent == "" {
		t.Skip("set a dedicated synthetic runtime destination")
	}
	if !filepath.IsAbs(parent) {
		t.Fatal("absolute fixture destination required")
	}
	home := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "PI_CODING_AGENT_DIR", "YESMAN_HOME"} {
		t.Setenv(name, home)
	}
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "OPENAI_API_KEY", "APP_PRIVATE_KEY", "SOPS_AGE_KEY", "YESMAN_TOKEN", "YESMAN_API"} {
		t.Setenv(name, "synthetic-parent-"+name)
	}
	m := githubapp.NewManager(filepath.Join(home, "app"), nil, "/fixture/gh")
	defer m.Close()
	if err := m.Prepare("synthetic-vault-password"); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	app := githubapp.Registration{ID: 123, Owner: "idy", Slug: "fixture", URL: "https://github.com/apps/fixture"}
	if err := m.SaveRegistration(app, private); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(os.Environ(), "\n"), string(private)) {
		t.Fatal("backend key entered parent env")
	}
	r, err := New(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := os.Stat(r.Directory()); !os.IsNotExist(err) {
		t.Fatal("acceptance requires a fresh fixture cache")
	}
	npm := r.npm
	if npm == "" {
		t.Fatal("actual npm required")
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	audit := filepath.Join(parent, "installer-audit.json")
	auditJSON, _ := json.Marshal(audit)
	npmJSON, _ := json.Marshal(npm)
	r.npm = filepath.Join(home, "npm-cli.js")
	code := `const fs=require('node:fs'),{spawnSync}=require('node:child_process');
const denied=['GH_TOKEN','GITHUB_TOKEN','OPENAI_API_KEY','APP_PRIVATE_KEY','SOPS_AGE_KEY','YESMAN_TOKEN','YESMAN_API','NODE_OPTIONS'];
const checks={credentialsAbsent:denied.every(k=>!process.env[k]),ownCache:!!process.env.npm_config_cache,explicitNpm:!!process.env.YESMAN_RUNTIME_NPM};
fs.writeFileSync(` + string(auditJSON) + `,JSON.stringify(checks));
if(!Object.values(checks).every(Boolean))process.exit(91);
const child=spawnSync(process.execPath,[` + string(npmJSON) + `,...process.argv.slice(2)],{stdio:'inherit',env:process.env});process.exit(child.status??92);`
	if err := os.WriteFile(r.npm, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	node, args, err := r.Command(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	var checks map[string]bool
	if err := json.Unmarshal(data, &checks); err != nil {
		t.Fatal(err)
	}
	for k, v := range checks {
		if !v {
			t.Error(k)
		}
	}
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "OPENAI_API_KEY"} {
		if os.Getenv(name) != "synthetic-parent-"+name {
			t.Fatal("normal pi/provider override changed")
		}
	}
	info, _ := json.Marshal(map[string]any{"root": r.Directory(), "node": node, "args": args, "version": Version})
	if err := os.WriteFile(filepath.Join(parent, "runtime.json"), info, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("loaded App vault remained Go-only; real managed install/npm saw no App, model or agent credentials; parent overrides unchanged")
}
