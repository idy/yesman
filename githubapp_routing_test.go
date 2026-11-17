package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/idy/yesman/internal/githubapp"
	"github.com/idy/yesman/internal/yesmen"
)

func TestGitHubAppRoutingFailureKeepsDesktopAvailable(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "unused"
		if enabled {
			name = "saved-app-mode"
		}
		t.Run(name, func(t *testing.T) {
			a := newConfigApp(t, &fakeKeychain{})
			a.yesmen = yesmen.New(a.home.Yesmen(), a.home.Archive())
			workspace, err := a.yesmen.Hire("alice")
			if err != nil {
				t.Fatal(err)
			}
			generation := workspace.HiredAt.UTC().Format(time.RFC3339Nano)
			source := filepath.Join(t.TempDir(), "app executable")
			if err := os.WriteFile(source, []byte("fixture executable"), 0700); err != nil {
				t.Fatal(err)
			}
			executable := "gh"
			if runtime.GOOS == "windows" {
				executable += ".exe"
			}
			// A non-replaceable target exercises the real installer failure without
			// starting a GUI app or touching the user's profile or an orphan process.
			blocker := filepath.Join(a.home.Runtime(), "github-app-gh", "bin", executable)
			if err := os.MkdirAll(blocker, 0700); err != nil {
				t.Fatal(err)
			}
			if enabled {
				path := filepath.Join(a.home.Root, "github-app")
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(githubapp.Config{Enabled: true, Repositories: []string{"idy/yesman"}, Workspaces: []string{"alice"}, WorkspaceGenerations: map[string]string{"alice": generation}})
				if err := os.WriteFile(filepath.Join(path, "config.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			shim, err := a.prepareGitHubAppRouting("/installed/gh", source)
			if err == nil || shim != "" || a.githubApp == nil {
				t.Fatal("routing failure did not retain an accessible desktop service")
			}
			defer a.githubApp.Close()
			view, err := (&service{app: a}).GitHubApp()
			if err != nil {
				t.Fatal(err)
			}
			if view.RoutingAvailable || view.Config.Enabled != enabled || a.githubApp.RequiresRouting() != enabled {
				t.Fatal("routing failure changed the user's selected mode")
			}
			if _, err := os.Stat(blocker); err != nil {
				t.Fatal("installer removed the protected target")
			}
			if enabled {
				mode, err := a.githubApp.Mode("alice", generation)
				if err != nil || mode != "app" {
					t.Fatal("saved mode fell back to personal")
				}
				if err := a.githubApp.Enable(false); err != nil {
					t.Fatal(err)
				}
				if mode, err := a.githubApp.Mode("alice", generation); err != nil || mode != "personal" {
					t.Fatal("explicit recovery could not disable App mode")
				}
			}
		})
	}
}
