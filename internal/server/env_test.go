package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/secrets"
)

func TestEnv(t *testing.T) {
	dir := t.TempDir()
	vault := secrets.NewVault(filepath.Join(dir, "secrets"))
	vault.WorkFactor = 10
	vars := secrets.NewVariables(filepath.Join(dir, "variables.yaml"))
	s := &Server{Secrets: vault, Variables: vars}

	if got := s.envContext(false); !strings.Contains(got, "No secrets or variables are set") {
		t.Errorf("context with none:\n%s", got)
	}

	if err := vault.Create("password1"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Save([]secrets.Entry{
		{Name: "GH_TOKEN", Value: "ghp_0123456789", Description: "GitHub token"},
		{Name: "PIN", Value: "1234"},
		{Name: "PEM", Value: "-----BEGIN-----\nline-one-of-the-key\n-----END-----\n"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := vars.Save([]secrets.Entry{{Name: "REGION", Value: "eu-west", Description: "Where to deploy"}}); err != nil {
		t.Fatal(err)
	}

	env := s.Env()
	if env.Env["GH_TOKEN"] != "ghp_0123456789" || env.Env["REGION"] != "eu-west" || env.Env["PIN"] != "1234" || env.Locked {
		t.Errorf("env: %#v", env)
	}
	hidden := map[string]string{}
	for _, h := range env.Hide {
		hidden[h.Value] = h.Name
	}
	if hidden["ghp_0123456789"] != "GH_TOKEN" || hidden["line-one-of-the-key"] != "PEM" {
		t.Errorf("hidden: %#v", env.Hide)
	}
	if _, ok := hidden["1234"]; ok {
		t.Error("a value too short to hide is hidden")
	}
	if _, ok := hidden["eu-west"]; ok {
		t.Error("a variable is hidden")
	}
	for i := 1; i < len(env.Hide); i++ {
		if len(env.Hide[i].Value) > len(env.Hide[i-1].Value) {
			t.Errorf("hidden values are not longest first: %#v", env.Hide)
		}
	}

	d := s.Describe()
	for _, v := range d.Vars {
		if v.Kind == "secret" && v.Value != "" {
			t.Errorf("Describe shows the value of %s", v.Name)
		}
		if v.Name == "REGION" && (v.Value != "eu-west" || v.Description != "Where to deploy") {
			t.Errorf("REGION: %#v", v)
		}
	}
	if got := s.envContext(false); !strings.Contains(got, "All secrets and variables are set as environment variables in bash commands. Call yesman_env to see what each one is.") {
		t.Errorf("context:\n%s", got)
	}
	if got := s.envContext(true); !strings.Contains(got, "in your workers' bash commands") {
		t.Errorf("a lead's context:\n%s", got)
	}

	// A hand-edited file that names PATH sets nothing, and says why.
	if err := os.WriteFile(vars.File(), []byte("variables:\n  REGION: eu\n  PATH: /tmp/evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if env := s.Env(); env.Env["PATH"] != "" || env.Env["REGION"] != "" || len(env.Errors) == 0 {
		t.Errorf("env from a refused file: %#v", env)
	}
	if err := vars.Save([]secrets.Entry{{Name: "REGION", Value: "eu-west", Description: "Where to deploy"}}); err != nil {
		t.Fatal(err)
	}

	vault.Lock()
	env = s.Env()
	if !env.Locked || env.Env["GH_TOKEN"] != "" || env.Env["REGION"] != "eu-west" || len(env.Hide) != 0 {
		t.Errorf("locked env: %#v", env)
	}
	if got := s.envContext(false); !strings.Contains(got, "locked") {
		t.Errorf("locked context:\n%s", got)
	}
	for _, v := range s.Describe().Vars {
		if v.Kind == "secret" && (!v.Locked || v.Description != "") {
			t.Errorf("locked secret described: %#v", v)
		}
	}
}
