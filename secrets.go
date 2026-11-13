package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"

	"github.com/idy/yesman/internal/secrets"
)

// Settings › Secrets and Settings › Variables edit the environment
// variables every agent's commands get, as a form or as YAML: secrets in a
// sops file that a password opens, kept in the credential store
// (keychain.go), and variables in a plain YAML file. The configuration
// file of Settings › Import and export backs both up (configenv.go).

// trError says a mistake the secrets package reports in the language
// yesman shows, keeping what wraps it, such as the file's name.
func trError(err error) error {
	var e *secrets.Error
	if errors.As(err, &e) {
		return errors.New(strings.Replace(err.Error(), e.Error(), tr(e.Msg, e.Args...), 1))
	}
	return err
}

// SecretsView is what Settings › Secrets shows.
type SecretsView struct {
	Configured bool            `json:"configured"` // a password was set
	Unlocked   bool            `json:"unlocked"`
	Names      []string        `json:"names"`   // known while locked too
	Entries    []secrets.Entry `json:"entries"` // only while unlocked
	Text       string          `json:"text"`    // the entries as YAML, while unlocked
	Error      string          `json:"error,omitempty"`
	File       string          `json:"file"`
	KeyFile    string          `json:"keyFile"`
	// Keychain says the system has a credential store, which keeps the
	// password; Stored, that it keeps this vault's.
	Keychain bool `json:"keychain"`
	Stored   bool `json:"stored"`
}

func (a *app) secretsView() SecretsView {
	v := SecretsView{
		Configured: a.vault.Configured(), Unlocked: a.vault.Unlocked(),
		Names: []string{}, Entries: []secrets.Entry{}, File: a.vault.File(), KeyFile: a.vault.KeyFile(),
		Keychain: a.hasKeychain(),
	}
	if v.Configured && v.Keychain {
		password, _ := a.storedPassword()
		v.Stored = password != ""
	}
	if names, err := a.vault.Names(); err != nil {
		v.Error = trError(err).Error()
	} else if names != nil {
		v.Names = names
	}
	if v.Unlocked {
		entries, err := a.vault.Entries()
		if err != nil {
			v.Error = trError(err).Error()
		} else if entries != nil {
			v.Entries = entries
		}
		v.Text = string(secrets.Format(secrets.KindSecrets, v.Entries))
	}
	return v
}

// VariablesView is what Settings › Variables shows.
type VariablesView struct {
	Entries []secrets.Entry `json:"entries"`
	Text    string          `json:"text"`
	Error   string          `json:"error,omitempty"` // the file could not be read
	File    string          `json:"file"`
}

func (a *app) variablesView() VariablesView {
	v := VariablesView{Entries: []secrets.Entry{}, File: a.variables.File()}
	entries, err := a.variables.Entries()
	if err != nil {
		v.Error = trError(err).Error()
		if data, rerr := os.ReadFile(a.variables.File()); rerr == nil {
			v.Text = string(data) // to mend in the YAML editor
		}
		return v
	}
	if entries != nil {
		v.Entries = entries
	}
	v.Text = string(secrets.Format(secrets.KindVariables, v.Entries))
	return v
}

func kindOf(kind string) (secrets.Kind, error) {
	switch secrets.Kind(kind) {
	case secrets.KindSecrets, secrets.KindVariables:
		return secrets.Kind(kind), nil
	}
	return "", fmt.Errorf("unknown kind %q", kind)
}

// checkApart refuses entries of one kind that the other sets too: a
// command could get only one of them.
func (a *app) checkApart(kind secrets.Kind, entries []secrets.Entry) error {
	other, err := a.otherNames(kind)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if slices.Contains(other, e.Name) {
			if kind == secrets.KindSecrets {
				return errors.New(tr("%s is a variable already: remove it from Variables first", e.Name))
			}
			return errors.New(tr("%s is a secret already: remove it from Secrets first", e.Name))
		}
	}
	return nil
}

// otherNames are the names the other kind sets.
func (a *app) otherNames(kind secrets.Kind) ([]string, error) {
	if kind == secrets.KindSecrets {
		vars, err := a.variables.Entries()
		return secrets.Names(vars), trError(err)
	}
	names, err := a.vault.Names()
	return names, trError(err)
}

// save keeps the entries of a kind.
func (a *app) save(kind secrets.Kind, entries []secrets.Entry) error {
	if err := secrets.Check(kind, entries); err != nil {
		return trError(err)
	}
	a.envmu.Lock()
	defer a.envmu.Unlock()
	if err := a.checkApart(kind, entries); err != nil {
		return err
	}
	if kind == secrets.KindSecrets {
		return trError(a.vault.Save(entries))
	}
	return trError(a.variables.Save(entries))
}

// Secrets returns what Settings › Secrets shows.
func (s *service) Secrets() SecretsView { return s.app.secretsView() }

// SetUpSecrets sets up an empty secrets file, unlocked: with a random
// password the keychain keeps, or, given one, with that.
func (s *service) SetUpSecrets(password string) (SecretsView, error) {
	if err := s.app.setUpSecrets(password); err != nil {
		return SecretsView{}, err
	}
	return s.app.secretsView(), nil
}

// UnlockSecrets opens the secrets until LockSecrets or the app quits: with
// the password the keychain keeps, or with one typed, which the keychain
// then keeps.
func (s *service) UnlockSecrets(password string) (SecretsView, error) {
	a := s.app
	if password == "" {
		if !a.unlockFromKeychain() {
			return SecretsView{}, errors.New(tr("the keychain has no password for these secrets: enter it"))
		}
		return a.secretsView(), nil
	}
	if err := a.vault.Unlock(password); err != nil {
		return SecretsView{}, trError(err)
	}
	if err := a.keepPassword(password); err != nil {
		log.Print("secrets: keeping the password in the keychain: ", err)
	}
	return a.secretsView(), nil
}

// CopySecretsPassword puts the password the keychain keeps on the
// clipboard.
func (s *service) CopySecretsPassword() error {
	password, _ := s.app.storedPassword()
	if password == "" {
		return errors.New(tr("the keychain has no password for these secrets"))
	}
	return mygo.Clipboard.Write(transfer.TextData(password))
}

// LockSecrets forgets the key: commands get no secrets until unlocked again.
func (s *service) LockSecrets() SecretsView {
	s.app.vault.Lock()
	return s.app.secretsView()
}

// ChangeSecretsPassword keeps the key with a new password, in the
// keychain too.
func (s *service) ChangeSecretsPassword(old, password string) error {
	if err := s.app.vault.ChangePassword(old, password); err != nil {
		return trError(err)
	}
	if s.app.vault.Unlocked() {
		return s.app.keepPassword(password)
	}
	return nil
}

// SaveSecrets replaces the secrets with those of the form.
func (s *service) SaveSecrets(entries []secrets.Entry) (SecretsView, error) {
	if err := s.app.save(secrets.KindSecrets, entries); err != nil {
		return SecretsView{}, err
	}
	return s.app.secretsView(), nil
}

// SaveSecretsText replaces the secrets with those of YAML.
func (s *service) SaveSecretsText(text string) (SecretsView, error) {
	entries, err := secrets.Parse(secrets.KindSecrets, []byte(text))
	if err != nil {
		return SecretsView{}, trError(err)
	}
	return s.SaveSecrets(entries)
}

// Variables returns what Settings › Variables shows.
func (s *service) Variables() VariablesView { return s.app.variablesView() }

// SaveVariables replaces the variables with those of the form.
func (s *service) SaveVariables(entries []secrets.Entry) (VariablesView, error) {
	if err := s.app.save(secrets.KindVariables, entries); err != nil {
		return VariablesView{}, err
	}
	return s.app.variablesView(), nil
}

// SaveVariablesText replaces the variables with those of YAML.
func (s *service) SaveVariablesText(text string) (VariablesView, error) {
	entries, err := secrets.Parse(secrets.KindVariables, []byte(text))
	if err != nil {
		return VariablesView{}, trError(err)
	}
	return s.SaveVariables(entries)
}

// FormatEntries writes a form's entries as YAML, for the YAML editor.
func (s *service) FormatEntries(kind string, entries []secrets.Entry) (string, error) {
	k, err := kindOf(kind)
	if err != nil {
		return "", err
	}
	return string(secrets.Format(k, entries)), nil
}

// ParseEntries reads the YAML editor's text, for the form.
func (s *service) ParseEntries(kind, text string) ([]secrets.Entry, error) {
	k, err := kindOf(kind)
	if err != nil {
		return nil, err
	}
	entries, err := secrets.Parse(k, []byte(text))
	if entries == nil {
		entries = []secrets.Entry{}
	}
	return entries, trError(err)
}

// CopyValue puts a secret's or variable's value on the clipboard.
func (s *service) CopyValue(value string) error {
	return mygo.Clipboard.Write(transfer.TextData(value))
}
