package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/egoist/mygo"
	"github.com/idy/yesman/internal/githubapp"
)

// GitHubAppSetup contains only an expiring entry URL and public status. Callback
// code/state, PEM, JWTs, tokens and vault passwords never reach this DTO.
type GitHubAppSetup struct {
	URL      string    `json:"url,omitempty"`
	Expires  time.Time `json:"expires,omitempty"`
	Complete bool      `json:"complete"`
	Error    string    `json:"error,omitempty"`
}
type GitHubAppWorkspace struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}
type GitHubAppView struct {
	githubapp.View
	Setup      GitHubAppSetup       `json:"setup"`
	Workspaces []GitHubAppWorkspace `json:"workspaces"`
}

func (s *service) GitHubApp() (GitHubAppView, error) {
	a := s.app
	if a.githubApp == nil {
		return GitHubAppView{}, errors.New("GitHub App settings are unavailable")
	}
	v := GitHubAppView{View: a.githubApp.View(), Workspaces: []GitHubAppWorkspace{}}
	if a.yesmen != nil {
		list, err := a.yesmen.List()
		if err != nil {
			return v, err
		}
		for _, y := range list {
			v.Workspaces = append(v.Workspaces, GitHubAppWorkspace{Name: y.Name, Label: y.Label()})
		}
	}
	if v.Config.Enabled {
		if err := s.checkGitHubAppWorkspaces(); err != nil {
			v.Error = err.Error()
		}
	}
	a.githubSetupMu.Lock()
	v.Setup = a.githubSetup
	a.githubSetupMu.Unlock()
	return v, nil
}
func (s *service) PrepareGitHubApp(password string) (GitHubAppView, error) {
	if err := s.app.githubApp.Prepare(password); err != nil {
		return GitHubAppView{}, err
	}
	return s.GitHubApp()
}
func (s *service) LockGitHubApp() (GitHubAppView, error) {
	s.app.stopGitHubAppSetup()
	s.app.githubApp.Lock()
	return s.GitHubApp()
}
func (s *service) ConfigureGitHubApp(cfg githubapp.Config) (GitHubAppView, error) {
	cfg.WorkspaceGenerations = make(map[string]string, len(cfg.Workspaces))
	for _, name := range cfg.Workspaces {
		y, err := s.app.yesmen.Get(name)
		if err != nil {
			return GitHubAppView{}, errors.New("select existing yesman workspaces")
		}
		cfg.WorkspaceGenerations[name] = y.HiredAt.UTC().Format(time.RFC3339Nano)
	}
	if err := s.app.githubApp.Configure(cfg); err != nil {
		return GitHubAppView{}, err
	}
	return s.GitHubApp()
}
func (s *service) checkGitHubAppWorkspaces() error {
	cfg := s.app.githubApp.View().Config
	for _, name := range cfg.Workspaces {
		y, err := s.app.yesmen.Get(name)
		if err != nil || cfg.WorkspaceGenerations[name] != y.HiredAt.UTC().Format(time.RFC3339Nano) {
			return errors.New("selected workspaces changed; save and test the GitHub App scope again")
		}
	}
	return nil
}

func (s *service) TestGitHubApp(ctx context.Context) (GitHubAppView, error) {
	if err := s.checkGitHubAppWorkspaces(); err != nil {
		return GitHubAppView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if _, err := s.app.githubApp.Test(ctx); err != nil {
		return GitHubAppView{}, err
	}
	return s.GitHubApp()
}
func (s *service) EnableGitHubApp(enabled bool) (GitHubAppView, error) {
	if enabled {
		if err := s.checkGitHubAppWorkspaces(); err != nil {
			return GitHubAppView{}, err
		}
	}
	if err := s.app.githubApp.Enable(enabled); err != nil {
		return GitHubAppView{}, err
	}
	return s.GitHubApp()
}

// Only a native chooser reads the PEM file. There is deliberately no binding
// that accepts, returns, copies or reveals the key's contents in JavaScript.
var githubAppOpenDialog = mygo.Dialog.Open
var githubAppSaveDialog = mygo.Dialog.Save

// ExportGitHubAppLogo saves only the existing public brand artwork, for GitHub's
// App avatar chooser. It creates no new artwork and exports no credentials.
func (s *service) ExportGitHubAppLogo() error {
	home, _ := os.UserHomeDir()
	path, err := githubAppSaveDialog(mygo.SaveDialogOptions{Parent: s.app.window(), Title: tr("Save App logo"), ButtonLabel: tr("Save"), DefaultPath: filepath.Join(home, "yesman.png"), Filters: []mygo.FileFilter{{Name: "PNG", Extensions: []string{"png"}}}})
	if err != nil || path == "" {
		return err
	}
	return os.WriteFile(path, appIcon, 0644)
}

func (s *service) ImportGitHubAppKey(ctx context.Context, appID string) (GitHubAppView, error) {
	id, err := strconv.ParseInt(appID, 10, 64)
	if err != nil || id <= 0 {
		return GitHubAppView{}, errors.New("enter the numeric GitHub App ID")
	}
	if !s.app.githubApp.View().Unlocked {
		return GitHubAppView{}, errors.New("unlock the GitHub App vault first")
	}
	paths, err := githubAppOpenDialog(mygo.OpenDialogOptions{Parent: s.app.window(), Title: tr("Import GitHub App private key"), ButtonLabel: tr("Import"), Filters: []mygo.FileFilter{{Name: tr("PEM private key"), Extensions: []string{"pem"}}}})
	if err != nil {
		return GitHubAppView{}, err
	}
	if len(paths) == 0 {
		return s.GitHubApp()
	}
	data, err := githubapp.ReadKeyFile(paths[0])
	if err != nil {
		return GitHubAppView{}, err
	}
	s.app.stopGitHubAppSetup() // this explicit import supersedes an open registration
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := s.app.githubApp.ImportKey(ctx, id, data); err != nil {
		return GitHubAppView{}, err
	}
	return s.GitHubApp()
}

func (s *service) RegisterGitHubApp(owner, name string) (GitHubAppView, error) {
	a := s.app
	if !a.githubApp.View().Unlocked {
		return GitHubAppView{}, errors.New("unlock the GitHub App vault first")
	}
	a.githubSetupMu.Lock()
	defer a.githubSetupMu.Unlock()
	if a.githubSetupCancel != nil {
		return GitHubAppView{}, errors.New("a GitHub App registration is already open; finish or cancel it first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	registration := githubapp.NewRegistrationServer(owner, name, "")
	entry := ""
	registration.Save = func(app githubapp.Registration, key []byte) error {
		a.githubSetupMu.Lock()
		defer a.githubSetupMu.Unlock()
		if a.githubSetup.URL != entry || ctx.Err() != nil {
			return errors.New("registration expired or was canceled; inspect the App on GitHub before retrying")
		}
		return a.githubApp.SaveRegistration(app, key)
	}
	entry, stop, err := registration.Start(ctx)
	if err != nil {
		cancel()
		return GitHubAppView{}, err
	}
	a.githubSetup = GitHubAppSetup{URL: entry, Expires: time.Now().Add(30 * time.Minute)}
	a.githubSetupCancel = cancel
	go func() {
		defer stop()
		select {
		case result := <-registration.Results():
			a.githubSetupMu.Lock()
			// Only this invocation may update its UI state; cancellation clears URL.
			if a.githubSetup.URL == entry {
				a.githubSetup.Complete = result.Err == nil
				if result.Err != nil {
					a.githubSetup.Error = result.Err.Error()
				}
			}
			a.githubSetupMu.Unlock()
			<-ctx.Done()
		case <-ctx.Done():
		}
		a.githubSetupMu.Lock()
		if a.githubSetup.URL == entry {
			a.githubSetup.URL = ""
			a.githubSetupCancel = nil
			if !a.githubSetup.Complete && a.githubSetup.Error == "" {
				a.githubSetup.Error = "Registration expired or was canceled. Inspect GitHub before registering another App."
			}
		}
		a.githubSetupMu.Unlock()
	}()
	// Do not call GitHubApp while holding githubSetupMu.
	return GitHubAppView{View: a.githubApp.View(), Setup: a.githubSetup}, nil
}
func (a *app) stopGitHubAppSetup() {
	a.githubSetupMu.Lock()
	defer a.githubSetupMu.Unlock()
	if a.githubSetupCancel != nil {
		a.githubSetupCancel()
		a.githubSetupCancel = nil
	}
	a.githubSetup.URL = ""
}
func (s *service) CancelGitHubAppRegistration() (GitHubAppView, error) {
	s.app.stopGitHubAppSetup()
	return s.GitHubApp()
}
