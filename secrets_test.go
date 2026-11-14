package main

import (
	"path/filepath"
	"strings"
	"testing"

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
