package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/crew"
	"github.com/idy/yesman/internal/filelock"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/restart"
	"github.com/idy/yesman/internal/schedule"
	"github.com/idy/yesman/internal/secrets"
	"github.com/idy/yesman/internal/server"
	"github.com/idy/yesman/internal/yesmen"
)

type runOptions struct {
	name, root, promptFile string
	create                 bool
	pin                    agent.Choice
	timeout                time.Duration
}

func runCLI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var o runOptions
	f := flag.NewFlagSet("yesman run", flag.ContinueOnError)
	f.SetOutput(stderr)
	f.StringVar(&o.name, "workspace", "", "stable workspace name (required)")
	f.BoolVar(&o.create, "new", false, "hire a fresh workspace, refusing an existing name")
	f.StringVar(&o.root, "root", "", "use this existing project directory for a new workspace")
	f.StringVar(&o.promptFile, "prompt-file", "-", "read one UTF-8 message from this file or - for stdin")
	f.StringVar(&o.pin.Provider, "provider", "", "exact Pi provider for the Lead and crew (with --model and --effort)")
	f.StringVar(&o.pin.Model, "model", "", "full Pi model ID within the provider")
	f.StringVar(&o.pin.Effort, "effort", "", "exact Pi thinking level")
	f.DurationVar(&o.timeout, "timeout", 30*time.Minute, "deadline for the whole run, including preparation")
	err := f.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	out := agent.RunResult{Workspace: o.name}
	if err == nil {
		switch {
		case f.NArg() != 0:
			err = errors.New("unexpected arguments (give the message through --prompt-file or stdin)")
		case o.timeout <= 0:
			err = errors.New("--timeout must be positive")
		case o.root != "" && !o.create:
			err = errors.New("--root requires --new")
		case o.pin != (agent.Choice{}) && (o.pin.IsZero() || !slices.Contains(crew.Levels, o.pin.Effort)):
			err = fmt.Errorf("give --provider, --model and --effort together (effort: %v)", crew.Levels)
		default:
			err = yesmen.ValidName(o.name)
		}
	}
	if err == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
		var prompt string
		prompt, err = readRunPrompt(ctx, o.promptFile, stdin)
		if err == nil {
			out, err = executeRun(ctx, o, prompt)
		}
	}
	if err != nil {
		out.Status, out.Error = agent.ContextStatus(err), err.Error()
	}
	if err := json.NewEncoder(stdout).Encode(out); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch out.Status {
	case "completed":
		return 0
	case "needs_input":
		return 2
	case "timed_out":
		return 124
	case "cancelled":
		return 130
	default:
		return 1
	}
}

func readRunPrompt(ctx context.Context, path string, stdin io.Reader) (string, error) {
	// Reading a pipe can block. The process must still respond to a deadline
	// or signal before any workspace is created.
	type read struct {
		data []byte
		err  error
	}
	ch := make(chan read, 1)
	go func() {
		r := stdin
		if path != "-" {
			f, err := os.Open(path)
			if err != nil {
				ch <- read{err: err}
				return
			}
			defer f.Close()
			r = f
		}
		const maxBytes = 32 << 20 // one agent's native submission payload budget
		data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
		if err == nil && (len(data) > maxBytes || !utf8.Valid(data) || len(bytes.TrimSpace(data)) == 0) {
			err = errors.New("provide a nonempty UTF-8 prompt of at most 32 MiB")
		}
		ch <- read{data, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-ch:
		return string(result.data), result.err
	}
}

func executeRun(ctx context.Context, o runOptions, prompt string) (out agent.RunResult, err error) {
	out.Workspace = o.name
	h, _, err := openStorage()
	if err != nil {
		return out, err
	}
	lock, err := h.Lock()
	if err != nil {
		return out, err
	}
	defer lock.Close()
	// A desktop from before the profile lock already holds its scheduler lock.
	legacy, err := filelock.Try(filepath.Join(h.Runtime(), "schedules.lock"))
	if err != nil {
		return out, fmt.Errorf("lock workspace profile %s: %w", h.Root, err)
	}
	defer legacy.Close()
	if err := h.Ensure(); err != nil {
		return out, err
	}
	a := &app{home: h, pool: pool.New(h.Repos()), yesmen: yesmen.New(h.Yesmen(), h.Archive()), settings: loadSettings(h.Settings())}
	var y yesmen.Yesman
	if !o.create {
		a.yesmen, y, err = a.yesmen.RunWorkspace(o.name, "", "", false)
		if err != nil {
			return out, err
		}
		out.Root = y.Path
		if pending, err := restart.Open(restart.Path(h.Root)).PendingFor(y.Name, y.HiredAt); err != nil {
			return out, err
		} else if pending {
			return out, fmt.Errorf("workspace %s has work in a pending desktop restart; open Yes Man to complete recovery", y.Name)
		}
		if o.pin.IsZero() && a.settings.Crew.IsZero() {
			if saved, ok := agent.ParseChoice(y.Model); ok && saved.Effort != "" {
				o.pin = saved
			}
		}
	}
	cfg, err := a.agentConfig()
	if err != nil || a.piErr != nil {
		return out, errors.Join(err, a.piErr)
	}
	if a.piRuntime != nil {
		defer a.piRuntime.Close()
	}
	cfg.ExactModel = o.pin
	models, err := cfg.Catalog(ctx, h.Runtime())
	if err != nil {
		if ctx.Err() != nil {
			err = errors.Join(ctx.Err(), err)
		}
		return out, fmt.Errorf("read Pi model catalog: %w", err)
	}
	team := a.settings.Crew
	if !o.pin.IsZero() {
		team = crew.Config{Yesman: o.pin.String(), Options: []crew.Option{{Provider: o.pin.Provider, Model: o.pin.Model, Effort: o.pin.Effort}}}
	}
	patch, _ := a.effectiveScorePatch()
	members := crew.Resolve(team, crewModels(models), crew.Embedded(), patch)
	if (o.create || !o.pin.IsZero()) && !crew.Usable(members, team.Yesman) {
		return out, fmt.Errorf("no available Lead crew option %q (check the exact provider, model, effort and Pi credentials)", team.Yesman)
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if o.create {
		a.yesmen, y, err = a.yesmen.RunWorkspace(o.name, o.root, team.Yesman, true)
		if err != nil {
			return out, err
		}
		out.Root = y.Path
	}
	if y.TaskRoot {
		// A copied profile can have a second registration for the same
		// external directory. Reserve the physical workspace as well.
		rootLock, err := filelock.Try(filepath.Join(y.MetaDir(), "run.lock"))
		if err != nil {
			return out, fmt.Errorf("lock task root %s: %w", y.Path, err)
		}
		defer rootLock.Close()
	}
	a.vault, a.variables = secrets.NewVault(h.Secrets()), secrets.NewVariables(h.Variables())
	srv := &server.Server{Crew: func(context.Context) []crew.Member { return members }, BoardChanged: func(name string) { a.agents.TasksChanged(name) }}
	// Permit the Lead's ordinary list/draft tools. The single human submission
	// owns this process, so the timer that sends scheduled messages stays off.
	schedules := schedule.New(schedule.Config{Yesmen: a.yesmen})
	srv.Schedules = schedules
	defer schedules.Close()
	if err := a.serveAgents(srv, cfg); err != nil {
		return out, err
	}
	defer a.githubApp.Close()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := srv.Shutdown(cleanup); shutdownErr != nil {
			if out.Status == "completed" || out.Status == "needs_input" {
				out.Status = "failure"
			}
			if out.Error != "" {
				out.Error += "\n"
			}
			out.Error += "close agent API: " + shutdownErr.Error()
		}
	}()
	return a.agents.RunOnce(ctx, y, prompt), nil
}
