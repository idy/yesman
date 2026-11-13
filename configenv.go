package main

import (
	"errors"
	"log"
	"slices"

	"github.com/idy/yesman/internal/secrets"
)

// The configuration file's variables and secrets. Exporting writes the
// variables, and the vault's two files as they are, encrypted, with the
// password the keychain keeps. Importing merges or replaces the variables
// it lists; its secrets set up a vault that has none, as they are, with
// the password kept in the keychain, or join the secrets here, opened
// with the file's password. A file without them leaves those here alone.

// SecretsChanges are what importing a file does to the secrets.
type SecretsChanges struct {
	Names    []string      `json:"names"`    // the file's secrets
	Password bool          `json:"password"` // the file has their password
	Restore  bool          `json:"restore"`  // none here: the file's are set up as they are
	Plan     *secrets.Plan `json:"plan,omitempty"`
	Skipped  string        `json:"skipped,omitempty"` // why they are left out
}

// EnvChanges are what importing a file does to the variables and secrets.
type EnvChanges struct {
	Variables *secrets.Plan   `json:"variables,omitempty"` // nil: the file has none
	Secrets   *SecretsChanges `json:"secrets,omitempty"`   // nil: the file has none
	// Clash lists the names variables and secrets would both set after
	// merging, ReplaceClash after replacing; either stops that import.
	Clash        []string `json:"clash"`
	ReplaceClash []string `json:"replaceClash"`
}

// envPlan is what importing a file does to the variables and secrets, in
// one mode.
type envPlan struct {
	vars       []secrets.Entry // the variables after, when setVars
	setVars    bool
	restore    *configSecrets // the file's vault, to set up as it is
	secrets    []secrets.Entry
	setSecrets bool // the secrets after, when setSecrets
	changes    EnvChanges
	clash      []string
}

// planEnv says what importing c does to the variables and secrets.
func (a *app) planEnv(c config, mode importMode) (envPlan, error) {
	replace := mode == importReplace
	p := envPlan{changes: EnvChanges{Clash: []string{}, ReplaceClash: []string{}}}
	if c.variables != nil && a.variables != nil {
		current, err := a.variables.Entries()
		if err != nil {
			return p, trError(err)
		}
		plan := secrets.PlanImport(current, *c.variables)
		p.changes.Variables = &plan
		p.setVars, p.vars = true, secrets.Merge(current, *c.variables, replace)
	}
	if c.secrets != nil && a.vault != nil {
		sc, err := a.planSecrets(c.secrets, replace, &p)
		if err != nil {
			return p, err
		}
		p.changes.Secrets = sc
	}
	varNames, secretNames, err := a.namesAfter(p)
	if err != nil {
		return p, err
	}
	p.clash = []string{}
	for _, n := range varNames {
		if slices.Contains(secretNames, n) {
			p.clash = append(p.clash, n)
		}
	}
	return p, nil
}

func (a *app) planSecrets(s *configSecrets, replace bool, p *envPlan) (*SecretsChanges, error) {
	sc := &SecretsChanges{Names: []string{}, Password: s.Password != ""}
	if names, err := secrets.FileNames([]byte(s.File)); err == nil && names != nil {
		sc.Names = names
	}
	switch {
	case !a.vault.Configured():
		if sc.Password {
			if _, err := secrets.OpenFiles([]byte(s.Key), []byte(s.File), s.Password); err != nil {
				sc.Skipped = tr("the file's password does not open its secrets: %s", trError(err))
				return sc, nil
			}
		}
		sc.Restore = true
		p.restore = s
	case !sc.Password:
		sc.Skipped = tr("the file has no password for its secrets, so they cannot join the secrets here")
	default:
		a.unlockFromKeychain()
		current, err := a.vault.Entries()
		if errors.Is(err, secrets.ErrLocked) {
			sc.Skipped = tr("the secrets here are locked: unlock them in Settings › Secrets, then import the file again")
			return sc, nil
		}
		if err != nil {
			return nil, trError(err)
		}
		incoming, err := secrets.OpenFiles([]byte(s.Key), []byte(s.File), s.Password)
		if err != nil {
			sc.Skipped = tr("the file's password does not open its secrets: %s", trError(err))
			return sc, nil
		}
		plan := secrets.PlanImport(current, incoming)
		sc.Plan = &plan
		p.setSecrets, p.secrets = true, secrets.Merge(current, incoming, replace)
	}
	return sc, nil
}

// namesAfter are the names variables and secrets set after p.
func (a *app) namesAfter(p envPlan) (vars, secretNames []string, err error) {
	switch {
	case p.setVars:
		vars = secrets.Names(p.vars)
	case a.variables != nil:
		v, err := a.variables.Entries()
		if err != nil {
			return nil, nil, trError(err)
		}
		vars = secrets.Names(v)
	}
	switch {
	case p.restore != nil:
		secretNames, _ = secrets.FileNames([]byte(p.restore.File))
	case p.setSecrets:
		secretNames = secrets.Names(p.secrets)
	case a.vault != nil:
		if secretNames, err = a.vault.Names(); err != nil {
			return nil, nil, trError(err)
		}
	}
	return vars, secretNames, nil
}

// envChanges says what importing c would do, in both modes.
func (a *app) envChanges(c config) (EnvChanges, error) {
	merge, err := a.planEnv(c, importMerge)
	if err != nil {
		return EnvChanges{}, err
	}
	replace, err := a.planEnv(c, importReplace)
	if err != nil {
		return EnvChanges{}, err
	}
	ch := merge.changes
	ch.Clash, ch.ReplaceClash = merge.clash, replace.clash
	return ch, nil
}

// EnvImported says what importing did to the variables and secrets.
type EnvImported struct {
	Variables *int   `json:"variables,omitempty"` // how many after, when the file had them
	Secrets   *int   `json:"secrets,omitempty"`
	Restored  bool   `json:"restored,omitempty"` // the file's vault was set up here
	Locked    bool   `json:"locked,omitempty"`   // it stays locked: the file had no password
	Skipped   string `json:"skipped,omitempty"`  // why the file's secrets were left out
}

// applyEnv imports the variables and secrets as p planned.
func (a *app) applyEnv(p envPlan) (EnvImported, error) {
	var res EnvImported
	if p.changes.Secrets != nil {
		res.Skipped = p.changes.Secrets.Skipped
	}
	a.envmu.Lock()
	defer a.envmu.Unlock()
	if p.setVars {
		if err := a.variables.Save(p.vars); err != nil {
			return res, trError(err)
		}
		n := len(p.vars)
		res.Variables = &n
	}
	switch {
	case p.restore != nil:
		s := p.restore
		if err := a.vault.Restore([]byte(s.Key), []byte(s.File), s.Password); err != nil {
			return res, trError(err)
		}
		if s.Password != "" {
			if err := a.keepPassword(s.Password); err != nil {
				log.Print("secrets: keeping the imported password in the keychain: ", err)
			}
		}
		names, _ := secrets.FileNames([]byte(s.File))
		n := len(names)
		res.Secrets, res.Restored, res.Locked = &n, true, s.Password == ""
	case p.setSecrets:
		if err := a.vault.Save(p.secrets); err != nil {
			return res, trError(err)
		}
		n := len(p.secrets)
		res.Secrets = &n
	}
	return res, nil
}

// exportedEnv are the variables and secrets a configuration file carries:
// the vault's files, with the keychain's password, when it is set up.
func (a *app) exportedEnv() ([]secrets.Entry, *configSecrets, error) {
	vars := []secrets.Entry{}
	if a.variables != nil {
		v, err := a.variables.Entries()
		if err != nil {
			return nil, nil, trError(err)
		}
		if v != nil {
			vars = v
		}
	}
	if a.vault == nil || !a.vault.Configured() {
		return vars, nil, nil
	}
	key, file, err := a.vault.Files()
	if err != nil {
		return nil, nil, trError(err)
	}
	password, _ := a.storedPassword()
	return vars, &configSecrets{Password: password, Key: string(key), File: string(file)}, nil
}
