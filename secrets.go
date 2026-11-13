package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"

	"github.com/idy/yesman/internal/secrets"
)

// Settings › Secrets and Settings › Variables edit the environment
// variables every agent's commands get, as a form or as YAML: secrets in a
// sops file that a password opens, variables in a plain YAML file. Each
// can be exported and imported, the secrets sealed with a password.

// envLimit bounds a file to import.
const envLimit = 1 << 20

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
}

func (a *app) secretsView() SecretsView {
	v := SecretsView{
		Configured: a.vault.Configured(), Unlocked: a.vault.Unlocked(),
		Names: []string{}, Entries: []secrets.Entry{}, File: a.vault.File(), KeyFile: a.vault.KeyFile(),
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

// SetUpSecrets sets the password and an empty secrets file, unlocked.
func (s *service) SetUpSecrets(password string) (SecretsView, error) {
	if err := s.app.vault.Create(password); err != nil {
		return SecretsView{}, trError(err)
	}
	return s.app.secretsView(), nil
}

// UnlockSecrets opens the secrets with their password, until LockSecrets
// or the app quits.
func (s *service) UnlockSecrets(password string) (SecretsView, error) {
	if err := s.app.vault.Unlock(password); err != nil {
		return SecretsView{}, trError(err)
	}
	return s.app.secretsView(), nil
}

// LockSecrets forgets the key: commands get no secrets until unlocked again.
func (s *service) LockSecrets() SecretsView {
	s.app.vault.Lock()
	return s.app.secretsView()
}

// ChangeSecretsPassword keeps the key with a new password.
func (s *service) ChangeSecretsPassword(old, password string) error {
	return trError(s.app.vault.ChangePassword(old, password))
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

// ---------- Export and import ----------

// EnvExport says what an export wrote; no path when the dialog was
// cancelled.
type EnvExport struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

func (s *service) saveAs(title, name string, filters []mygo.FileFilter, data []byte) (string, error) {
	dir, _ := os.UserHomeDir()
	path, err := configSaveDialog(mygo.SaveDialogOptions{
		Parent: s.app.window(), Title: title, ButtonLabel: tr("Export"),
		DefaultPath: filepath.Join(dir, name), Filters: filters, CreateDirectories: true,
	})
	if err != nil || path == "" {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return path, err
	}
	// WriteFile keeps the permissions of a file it replaces: an export over
	// one others can read is made the user's alone too.
	return path, os.Chmod(path, 0o600)
}

// ExportSecrets saves the secrets as YAML: sealed with a password, which
// `age -d` also opens, or, with no password, in plain text.
func (s *service) ExportSecrets(password string) (EnvExport, error) {
	entries, err := s.app.vault.Entries()
	if err != nil {
		return EnvExport{}, trError(err)
	}
	data := secrets.Format(secrets.KindSecrets, entries)
	name, filters := "yesman-secrets.yaml", []mygo.FileFilter{{Name: tr("YAML"), Extensions: []string{"yaml", "yml"}}}
	if password != "" {
		if data, err = secrets.Seal(data, password, 0); err != nil {
			return EnvExport{}, trError(err)
		}
		name, filters = "yesman-secrets.yaml.age", []mygo.FileFilter{{Name: tr("Encrypted with age"), Extensions: []string{"age"}}}
	}
	path, err := s.saveAs(tr("Export secrets"), name, filters, data)
	return EnvExport{Path: path, Count: len(entries)}, err
}

// ExportSecretsKey saves the age key that opens the secrets file, for
// sops (SOPS_AGE_KEY_FILE). Anyone with it reads the secrets.
func (s *service) ExportSecretsKey() (EnvExport, error) {
	key, err := s.app.vault.Key()
	if err != nil {
		return EnvExport{}, trError(err)
	}
	path, err := s.saveAs(tr("Export the secrets key"), "yesman-age-key.txt",
		[]mygo.FileFilter{{Name: tr("Text"), Extensions: []string{"txt"}}}, []byte(key))
	return EnvExport{Path: path, Count: 1}, err
}

// ExportVariables saves the variables as YAML.
func (s *service) ExportVariables() (EnvExport, error) {
	entries, err := s.app.variables.Entries()
	if err != nil {
		return EnvExport{}, trError(err)
	}
	path, err := s.saveAs(tr("Export variables"), "yesman-variables.yaml",
		[]mygo.FileFilter{{Name: tr("YAML"), Extensions: []string{"yaml", "yml"}}}, secrets.Format(secrets.KindVariables, entries))
	return EnvExport{Path: path, Count: len(entries)}, err
}

// EnvFile is a file chosen to import; no path when the dialog was
// cancelled.
type EnvFile struct {
	Path      string `json:"path"`
	Encrypted bool   `json:"encrypted"` // it needs its password
}

// ChooseEnvFile asks for a file of secrets or variables to import.
func (s *service) ChooseEnvFile(kind string) (EnvFile, error) {
	k, err := kindOf(kind)
	if err != nil {
		return EnvFile{}, err
	}
	title := tr("Import secrets")
	if k == secrets.KindVariables {
		title = tr("Import variables")
	}
	dir, _ := os.UserHomeDir()
	paths, err := configOpenDialog(mygo.OpenDialogOptions{
		Parent: s.app.window(), Title: title, ButtonLabel: tr("Choose"), DefaultPath: dir,
		Filters: []mygo.FileFilter{{Name: tr("YAML, or encrypted with age"), Extensions: []string{"yaml", "yml", "age"}}},
	})
	if err != nil || len(paths) == 0 {
		return EnvFile{}, err
	}
	f, err := os.Open(paths[0])
	if err != nil {
		return EnvFile{}, err
	}
	defer f.Close()
	head := make([]byte, 64)
	n, _ := f.Read(head)
	return EnvFile{Path: paths[0], Encrypted: secrets.IsEncrypted(head[:n])}, nil
}

// EnvImportPlan is what importing a file would change.
type EnvImportPlan struct {
	Path string `json:"path"`
	secrets.Plan
	// Clash lists the file's names the other kind sets, which stop it.
	Clash []string `json:"clash"`
}

func (a *app) readImport(k secrets.Kind, path, password string) (current, incoming []secrets.Entry, err error) {
	incoming, err = secrets.ReadFile(k, path, password, envLimit)
	if err != nil {
		return nil, nil, trError(err)
	}
	if k == secrets.KindSecrets {
		current, err = a.vault.Entries()
	} else {
		current, err = a.variables.Entries()
	}
	return current, incoming, trError(err)
}

// PreviewEnvImport says what importing a file of secrets or variables
// would change. An encrypted file needs its password.
func (s *service) PreviewEnvImport(kind, path, password string) (EnvImportPlan, error) {
	k, err := kindOf(kind)
	if err != nil {
		return EnvImportPlan{}, err
	}
	current, incoming, err := s.app.readImport(k, path, password)
	if err != nil {
		return EnvImportPlan{}, err
	}
	other, err := s.app.otherNames(k)
	if err != nil {
		return EnvImportPlan{}, err // not a clash: the other file cannot be read
	}
	plan := EnvImportPlan{Path: path, Plan: secrets.PlanImport(current, incoming), Clash: []string{}}
	for _, e := range incoming {
		if slices.Contains(other, e.Name) {
			plan.Clash = append(plan.Clash, e.Name)
		}
	}
	return plan, nil
}

// ImportEnv imports a file of secrets or variables: merge changes and
// adds what it lists; replace also removes what it leaves out.
func (s *service) ImportEnv(kind, path, password, mode string) (int, error) {
	k, err := kindOf(kind)
	if err != nil {
		return 0, err
	}
	if mode != string(importMerge) && mode != string(importReplace) {
		return 0, fmt.Errorf("unknown import mode %q", mode)
	}
	current, incoming, err := s.app.readImport(k, path, password)
	if err != nil {
		return 0, err
	}
	next := secrets.Merge(current, incoming, mode == string(importReplace))
	return len(next), s.app.save(k, next)
}
