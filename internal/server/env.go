package server

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/idy/yesman/internal/secrets"
)

// Every agent's commands get the user's variables, and their secrets while
// they are unlocked, as environment variables: the extension asks for them
// before each command, so a change reaches the next command without
// restarting anyone. Secrets never enter pi's own environment, so pi's
// other extensions and MCP servers do not see them, and their values are
// hidden from what tools return to the model.

// minHidden is the shortest value hidden from output: shorter ones would
// hide ordinary text.
const minHidden = 6

// Hidden is a secret's value, or a line of it, that output must not show.
type Hidden struct {
	Value string `json:"value"`
	Name  string `json:"name"`
}

// EnvView is what a command's environment gets.
type EnvView struct {
	Env    map[string]string `json:"env"`
	Hide   []Hidden          `json:"hide"`   // longest first
	Locked bool              `json:"locked"` // there are secrets, locked away
	Errors []string          `json:"errors,omitempty"`
}

// EnvVar is an environment variable as yesman_env describes it.
type EnvVar struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"` // secret or variable
	Description string `json:"description,omitempty"`
	Value       string `json:"value,omitempty"`  // a variable's; never a secret's
	Locked      bool   `json:"locked,omitempty"` // a secret, locked away with its description
}

// EnvDescription is what yesman_env shows.
type EnvDescription struct {
	Vars   []EnvVar `json:"vars"`
	Locked bool     `json:"locked"`
	Errors []string `json:"errors,omitempty"`
}

// sources reads the variables and secrets, with what failed.
func (s *Server) sources() (vars, secretList []secrets.Entry, locked []string, errs []string) {
	if s.Variables != nil {
		// A file that does not read sets none, rather than names it refuses.
		if v, err := s.Variables.Entries(); err != nil {
			errs = append(errs, "variables: "+err.Error())
		} else {
			vars = v
		}
	}
	if s.Secrets != nil {
		if s.Secrets.Unlocked() {
			e, err := s.Secrets.Entries()
			if err != nil {
				errs = append(errs, "secrets: "+err.Error())
			}
			secretList = e
		} else {
			names, err := s.Secrets.Names()
			if err != nil {
				errs = append(errs, "secrets: "+err.Error())
			}
			locked = names
		}
	}
	return vars, secretList, locked, errs
}

// Env is the environment variables for a command.
func (s *Server) Env() EnvView {
	vars, list, locked, errs := s.sources()
	out := EnvView{Env: map[string]string{}, Hide: []Hidden{}, Locked: len(locked) > 0, Errors: errs}
	for _, v := range vars {
		out.Env[v.Name] = v.Value
	}
	seen := map[string]bool{}
	hide := func(value, name string) {
		if len(value) >= minHidden && !seen[value] {
			seen[value] = true
			out.Hide = append(out.Hide, Hidden{Value: value, Name: name})
		}
	}
	for _, e := range list {
		out.Env[e.Name] = e.Value // a secret wins over a variable of its name
		hide(e.Value, e.Name)
		hide(strings.TrimSpace(e.Value), e.Name)
		if strings.Contains(e.Value, "\n") {
			for _, line := range strings.Split(e.Value, "\n") {
				hide(strings.TrimSpace(line), e.Name)
			}
		}
	}
	slices.SortStableFunc(out.Hide, func(a, b Hidden) int { return cmp.Compare(len(b.Value), len(a.Value)) })
	return out
}

// Describe says what each environment variable is, without a secret's value.
func (s *Server) Describe() EnvDescription {
	vars, list, locked, errs := s.sources()
	out := EnvDescription{Vars: []EnvVar{}, Locked: len(locked) > 0, Errors: errs}
	for _, e := range list {
		out.Vars = append(out.Vars, EnvVar{Name: e.Name, Kind: "secret", Description: e.Description})
	}
	for _, name := range locked {
		out.Vars = append(out.Vars, EnvVar{Name: name, Kind: "secret", Locked: true})
	}
	for _, v := range vars {
		out.Vars = append(out.Vars, EnvVar{Name: v.Name, Kind: "variable", Description: v.Description, Value: v.Value})
	}
	return out
}

func (s *Server) env(context.Context, caller, *http.Request) (any, error) { return s.Env(), nil }

func (s *Server) describeEnv(context.Context, caller, *http.Request) (any, error) {
	return s.Describe(), nil
}

// envContext is the context's part on environment variables. A lead has
// no shell: its workers' commands get them.
func (s *Server) envContext(lead bool) string {
	vars, list, locked, _ := s.sources()
	var b strings.Builder
	b.WriteString("\n## Environment\n\n")
	if len(vars)+len(list)+len(locked) == 0 {
		whose := "your commands"
		if lead {
			whose = "your workers' commands"
		}
		b.WriteString("No secrets or variables are set. When a task needs a credential or a setting, ask the user to add it in yesman's Settings › Secrets (keys and tokens) or Settings › Variables; it then reaches " + whose + " as an environment variable.\n")
		return b.String()
	}
	if lead {
		b.WriteString("All secrets and variables are set as environment variables in your workers' bash commands. Call yesman_env to see what each one is.\n")
	} else {
		b.WriteString("All secrets and variables are set as environment variables in bash commands. Call yesman_env to see what each one is.\n")
	}
	if len(locked) > 0 {
		b.WriteString("The secrets are locked now, so commands get only the variables. If you need a secret, ask the user to unlock them in yesman's Settings › Secrets.\n")
	} else if len(list) > 0 {
		b.WriteString("A secret's value never reaches you: output shows it as [secret NAME].\n")
	}
	return b.String()
}
