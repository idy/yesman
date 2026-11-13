package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/egoist/mygo"

	"github.com/idy/yesman/internal/secrets"
)

func newEnvService(t *testing.T) *service {
	t.Helper()
	dir := t.TempDir()
	v := secrets.NewVault(filepath.Join(dir, "secrets"))
	v.WorkFactor = 10
	return &service{app: &app{vault: v, variables: secrets.NewVariables(filepath.Join(dir, "variables.yaml"))}}
}

func TestSecretsAndVariablesStayApart(t *testing.T) {
	s := newEnvService(t)
	if _, err := s.SetUpSecrets("password1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveSecrets([]secrets.Entry{{Name: "GH_TOKEN", Value: "ghp_x"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveVariablesText("variables:\n  GH_TOKEN: plain\n"); err == nil || !strings.Contains(err.Error(), "secret already") {
		t.Errorf("a variable named as a secret: %v", err)
	}
	// Locked, the secrets' names still keep variables apart.
	s.LockSecrets()
	if _, err := s.SaveVariables([]secrets.Entry{{Name: "GH_TOKEN", Value: "plain"}}); err == nil {
		t.Error("a variable named as a locked secret was saved")
	}
	v, err := s.SaveVariables([]secrets.Entry{{Name: "REGION", Value: "eu", Description: "where"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Text, "REGION:") || len(v.Entries) != 1 {
		t.Errorf("variables view: %#v", v)
	}
	view := s.Secrets()
	if view.Unlocked || len(view.Entries) != 0 || view.Text != "" || len(view.Names) != 1 {
		t.Errorf("a locked view shows %#v", view)
	}
	if _, err := s.UnlockSecrets("nope-nope"); err == nil {
		t.Error("a wrong password unlocked")
	}
	if view, err := s.UnlockSecrets("password1"); err != nil || view.Entries[0].Value != "ghp_x" {
		t.Errorf("unlocked: %#v, %v", view, err)
	}
	if _, err := s.SaveSecrets([]secrets.Entry{{Name: "REGION", Value: "x"}}); err == nil {
		t.Error("a secret named as a variable was saved")
	}
}

func TestExportAndImportSecrets(t *testing.T) {
	s := newEnvService(t)
	if _, err := s.SetUpSecrets("password1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveSecrets([]secrets.Entry{{Name: "A", Value: "aaaaaa"}, {Name: "B", Value: "bbbbbb"}}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "backup.yaml.age")
	saved := configSaveDialog
	t.Cleanup(func() { configSaveDialog = saved })
	configSaveDialog = func(mygo.SaveDialogOptions) (string, error) { return out, nil }
	if e, err := s.ExportSecrets("backup-pass"); err != nil || e.Count != 2 {
		t.Fatalf("export: %#v, %v", e, err)
	}
	data, _ := os.ReadFile(out)
	if !secrets.IsEncrypted(data) || strings.Contains(string(data), "aaaaaa") {
		t.Fatalf("the export is not sealed:\n%s", data)
	}

	if _, err := s.SaveSecrets([]secrets.Entry{{Name: "B", Value: "changed"}, {Name: "C", Value: "cccccc"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewEnvImport("secrets", out, ""); err == nil {
		t.Error("an encrypted file previewed without its password")
	}
	plan, err := s.PreviewEnvImport("secrets", out, "backup-pass")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(plan.Add, ",") != "A" || strings.Join(plan.Change, ",") != "B" || strings.Join(plan.Extra, ",") != "C" {
		t.Errorf("plan: %#v", plan)
	}
	if n, err := s.ImportEnv("secrets", out, "backup-pass", "merge"); err != nil || n != 3 {
		t.Fatalf("merge: %d, %v", n, err)
	}
	if n, err := s.ImportEnv("secrets", out, "backup-pass", "replace"); err != nil || n != 2 {
		t.Fatalf("replace: %d, %v", n, err)
	}
	view := s.Secrets()
	if len(view.Entries) != 2 || view.Entries[1].Value != "bbbbbb" {
		t.Errorf("after replacing: %#v", view.Entries)
	}

	// The key exported opens the file, as sops would use it.
	keyFile := filepath.Join(t.TempDir(), "key.txt")
	configSaveDialog = func(mygo.SaveDialogOptions) (string, error) { return keyFile, nil }
	if _, err := s.ExportSecretsKey(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(keyFile); err != nil || runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("key file: %v, %v", info, err)
	}
}
