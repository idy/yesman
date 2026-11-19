package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/piruntime"
	"github.com/idy/yesman/internal/server"
)

// agentConfig is shared by the desktop and the single-message client. Both
// load the embedded extension and use the same managed/external Pi selection.
func (a *app) agentConfig() (agent.Config, error) {
	extension := filepath.Join(a.home.Runtime(), "pi", "yesman.ts")
	if err := os.MkdirAll(filepath.Dir(extension), 0o755); err != nil {
		return agent.Config{}, err
	}
	if err := os.WriteFile(extension, extensionSource, 0o644); err != nil {
		return agent.Config{}, err
	}
	a.pi, a.piErr = findPi()
	var command func(context.Context) (string, []string, error)
	if os.Getenv("YESMAN_PI") == "" && (a.piErr != nil || piruntime.OfficialNPM(a.pi)) {
		a.managedPi = true
		a.piRuntime, a.piErr = piruntime.New(a.home.Runtime())
		if a.piErr == nil {
			a.pi, command = a.piRuntime.Description(), a.piRuntime.Command
		}
	}
	return agent.Config{Pi: a.pi, Command: command, Extension: extension}, nil
}

// serveAgents attaches the authenticated manager before serving any requests.
// The frontends supply only their presentation and event callbacks.
func (a *app) serveAgents(srv *server.Server, cfg agent.Config) error {
	gh, err := exec.LookPath("gh")
	if err == nil {
		gh, _ = filepath.Abs(gh)
	}
	executable, _ := os.Executable()
	shim, err := a.prepareGitHubAppRouting(gh, executable)
	if err != nil {
		log.Print("GitHub App command routing is unavailable: ", err)
	}
	required := ""
	if shim == "" && a.githubApp.RequiresRouting() {
		required = "1"
	}
	srv.GitHubApp, srv.Yesmen, srv.Pool = a.githubApp, a.yesmen, a.pool
	srv.Secrets, srv.Variables = a.vault, a.variables
	return srv.StartWith(func(url string) {
		cfg.Yesmen = a.yesmen
		cfg.Env = append(cfg.Env, "YESMAN_API="+url, "YESMAN_HOME="+a.home.Root, "YESMAN_GH_SHIM="+shim, "YESMAN_GH_REAL="+gh, "YESMAN_GH_REQUIRED="+required)
		a.agents = agent.New(cfg)
		srv.Agents = a.agents
	})
}
