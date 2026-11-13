package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pipkg"
)

// pi's packages are installed for the user: in pi's agent directory, where
// every yesman agent loads them, as pi does wherever the user runs it.
// yesman changes them only through pi's own commands, so pi keeps writing
// its settings and choosing its package manager (its npmCommand setting).

// PiJob is a change to pi's packages under way, or one that failed.
type PiJob struct {
	ID     int     `json:"id"`
	Op     PiOp    `json:"op"`
	Source string  `json:"source"` // empty for updating them all
	Title  string  `json:"title"`  // what the page calls the package
	State  JobStep `json:"state"`
	Error  string  `json:"error,omitempty"`
}

// PiOp is what a job does.
type PiOp string

const (
	PiInstall PiOp = "install"
	PiUpdate  PiOp = "update"
	PiRemove  PiOp = "remove"
)

// JobStep is where a job stands.
type JobStep string

const (
	JobQueued  JobStep = "queued"
	JobRunning JobStep = "running"
	JobFailed  JobStep = "failed"
)

// PiJobs is what the page follows: the jobs, and when one last changed
// the packages, so that it can tell agents started before then.
type PiJobs struct {
	Jobs      []PiJob   `json:"jobs"`
	ChangedAt time.Time `json:"changedAt,omitzero"`
	// Version counts the changes to the jobs. Lists reach the page by
	// events, answers and reloads, in any order: the highest is the newest.
	Version int `json:"version"`
}

// piJobTimeout bounds one pi command; installing runs npm.
const piJobTimeout = 10 * time.Minute

// piJobs runs pi's package commands one at a time, in the order asked,
// since they share pi's package directory and settings. A job that fails
// stays listed with its error until it is tried again or dismissed.
type piJobs struct {
	// run runs pi with the arguments and returns what it printed.
	run func(ctx context.Context, args ...string) (string, error)
	// changed is told the jobs after every change, in order. It must not
	// call back into piJobs.
	changed func(PiJobs)

	mu        sync.Mutex
	jobs      []*PiJob
	seq       int
	busy      bool
	changedAt time.Time
	version   int
}

func newPiJobs(run func(context.Context, ...string) (string, error), changed func(PiJobs)) *piJobs {
	return &piJobs{run: run, changed: changed}
}

// runPi runs a pi command in dir, a folder with no project settings, so
// that it reads and writes only the user's.
func runPi(pi, dir string) func(context.Context, ...string) (string, error) {
	return func(ctx context.Context, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, pi, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "NO_COLOR=1", "FORCE_COLOR=0")
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		text := pipkg.StripANSI(out.String())
		if err != nil {
			if ctx.Err() != nil {
				return text, errors.New(tr("pi %s took too long", args[0]))
			}
			if msg := failureText(text); msg != "" {
				return text, errors.New(msg)
			}
			return text, fmt.Errorf("pi %s: %w", args[0], err)
		}
		return text, nil
	}
}

// failureText picks why a pi command failed out of what it printed: npm's
// first errors when npm failed, which say more than pi's summary of them,
// else pi's error, else its last line.
func failureText(text string) string {
	var npm []string
	var piErr, last string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		last = l
		if rest, ok := strings.CutPrefix(l, "Error: "); ok && piErr == "" {
			piErr = rest
			continue
		}
		rest, ok := strings.CutPrefix(l, "npm error ")
		if !ok {
			rest, ok = strings.CutPrefix(l, "npm ERR! ")
		}
		// Leave out npm's error code, bare status numbers and the log's path.
		if rest = strings.TrimSpace(rest); ok && len(npm) < 2 && len(rest) > 4 &&
			!strings.HasPrefix(rest, "code ") && !strings.HasPrefix(rest, "A complete log") {
			npm = append(npm, rest)
		}
	}
	msg := last
	switch {
	case len(npm) > 0:
		msg = strings.Join(npm, "\n")
	case piErr != "":
		msg = piErr
	}
	if r := []rune(msg); len(r) > 600 {
		msg = string(r[:600]) + "…"
	}
	return msg
}

func (j *piJobs) List() PiJobs {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.listLocked()
}

func (j *piJobs) listLocked() PiJobs {
	list := PiJobs{Jobs: make([]PiJob, 0, len(j.jobs)), ChangedAt: j.changedAt, Version: j.version}
	for _, job := range j.jobs {
		list.Jobs = append(list.Jobs, *job)
	}
	return list
}

// emitLocked follows every change to the jobs.
func (j *piJobs) emitLocked() {
	j.version++
	if j.changed != nil {
		j.changed(j.listLocked())
	}
}

// Queue asks for a job. The same job queued or under way stays as it is;
// the same job that failed is tried again. A source must be one pi takes,
// except for updating all, which has none.
func (j *piJobs) Queue(op PiOp, source, title string) (PiJobs, error) {
	switch op {
	case PiInstall, PiUpdate, PiRemove:
	default:
		return PiJobs{}, fmt.Errorf("unknown package operation %q", op)
	}
	if source != "" || op != PiUpdate {
		src, err := pipkg.ParseSource(source)
		if err != nil {
			return PiJobs{}, sourceError(err)
		}
		source = src.Raw
		if title == "" {
			title = src.Name
		}
		if title == "" {
			title = source
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if i := slices.IndexFunc(j.jobs, func(x *PiJob) bool { return x.Op == op && x.Source == source }); i >= 0 {
		if x := j.jobs[i]; x.State == JobFailed {
			x.State, x.Error = JobQueued, ""
			j.startLocked()
			j.emitLocked()
		}
		return j.listLocked(), nil
	}
	j.seq++
	j.jobs = append(j.jobs, &PiJob{ID: j.seq, Op: op, Source: source, Title: title, State: JobQueued})
	j.startLocked()
	j.emitLocked()
	return j.listLocked(), nil
}

// startLocked runs the oldest queued job unless one runs.
func (j *piJobs) startLocked() {
	if j.busy {
		return
	}
	for _, job := range j.jobs {
		if job.State == JobQueued {
			job.State = JobRunning
			j.busy = true
			go j.do(job)
			return
		}
	}
}

func (j *piJobs) do(job *PiJob) {
	ctx, cancel := context.WithTimeout(context.Background(), piJobTimeout)
	_, err := j.run(ctx, piArgs(job.Op, job.Source)...)
	cancel()
	j.mu.Lock()
	defer j.mu.Unlock()
	j.busy = false
	if err != nil {
		job.State, job.Error = JobFailed, err.Error()
	} else {
		if i := slices.Index(j.jobs, job); i >= 0 {
			j.jobs = slices.Delete(j.jobs, i, i+1)
		}
		j.changedAt = time.Now()
	}
	j.startLocked()
	j.emitLocked()
}

// sourceError says why a source was refused in the user's language.
func sourceError(err error) error {
	var e *pipkg.Error
	if !errors.As(err, &e) {
		return err
	}
	if strings.Contains(e.Format, "%") {
		return errors.New(tr(e.Format, e.Arg))
	}
	return errors.New(tr(e.Format))
}

// piArgs is the pi command for a job.
func piArgs(op PiOp, source string) []string {
	switch {
	case op == PiUpdate && source == "":
		return []string{"update", "--extensions", "--no-approve"}
	case op == PiUpdate:
		return []string{"update", "--extension", source, "--no-approve"}
	default:
		return []string{string(op), source, "--no-approve"}
	}
}

// Retry queues a job that failed again.
func (j *piJobs) Retry(id int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, job := range j.jobs {
		if job.ID == id && job.State == JobFailed {
			job.State, job.Error = JobQueued, ""
			j.startLocked()
			j.emitLocked()
			return
		}
	}
}

// Dismiss forgets a job that failed.
func (j *piJobs) Dismiss(id int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if i := slices.IndexFunc(j.jobs, func(x *PiJob) bool { return x.ID == id && x.State == JobFailed }); i >= 0 {
		j.jobs = slices.Delete(j.jobs, i, i+1)
		j.emitLocked()
	}
}

// ChangedAt is when a job last changed the packages.
func (j *piJobs) ChangedAt() time.Time {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.changedAt
}

// ---------- The page's methods ----------

// PiView is what the pi settings page shows.
type PiView struct {
	Pi      string `json:"pi"`
	Version string `json:"version,omitempty"`
	PiError string `json:"piError,omitempty"`
	// SettingsFile is pi's user settings, which list the packages.
	SettingsFile string `json:"settingsFile"`
	// NpmCommand is what pi installs npm packages with.
	NpmCommand []string          `json:"npmCommand"`
	Packages   []pipkg.Installed `json:"packages"`
	ListError  string            `json:"listError,omitempty"`
	PiJobs
}

// piAgentDir is pi's agent directory, as pi finds it.
func piAgentDir() string {
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		if rest, ok := strings.CutPrefix(d, "~"); ok {
			if home, err := os.UserHomeDir(); err == nil {
				return filepath.Join(home, rest)
			}
		}
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".pi", "agent")
	}
	return filepath.Join(home, ".pi", "agent")
}

// npmCommand reads pi's npmCommand setting, npm when it sets none.
func npmCommand(settingsFile string) []string {
	var s struct {
		NpmCommand []string `json:"npmCommand"`
	}
	if data, err := os.ReadFile(settingsFile); err == nil && json.Unmarshal(data, &s) == nil && len(s.NpmCommand) > 0 {
		return s.NpmCommand
	}
	return []string{"npm"}
}

// PiPackages lists the packages pi's user settings install, and pi's own
// version.
func (s *service) PiPackages(ctx context.Context) PiView {
	a := s.app
	settings := filepath.Join(piAgentDir(), "settings.json")
	v := PiView{Pi: a.pi, SettingsFile: settings, NpmCommand: npmCommand(settings), Packages: []pipkg.Installed{}}
	if a.piErr != nil {
		v.PiError = a.piErr.Error()
		v.PiJobs = a.piJobs.List()
		return v
	}
	var wg sync.WaitGroup
	var version string
	wg.Go(func() {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if out, err := a.runPi(ctx, "--version"); err == nil {
			version = strings.TrimSpace(out)
		}
	})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := a.runPi(ctx, "list", "--no-approve")
	if err != nil {
		v.ListError = err.Error()
	} else {
		v.Packages = pipkg.ParseList(out)
	}
	wg.Wait()
	v.Version = version
	// The jobs as they are once pi has listed the packages, not before.
	v.PiJobs = a.piJobs.List()
	return v
}

// PiPackageUpdates names the latest version of each installed npm package
// that updates can move, by package name. Packages the registry does not
// answer for are left out.
func (s *service) PiPackageUpdates(ctx context.Context, names []string) map[string]string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	latest := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, name := range slices.Compact(slices.Sorted(slices.Values(names))) {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			if v, err := pipkg.Latest(ctx, name); err == nil && v != "" {
				mu.Lock()
				latest[name] = v
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return latest
}

// SearchPiPackages lists the pi packages the npm registry has that match
// the query, from the from-th on.
func (s *service) SearchPiPackages(ctx context.Context, query string, from int) (pipkg.Results, error) {
	return pipkg.Search(ctx, query, from)
}

// PiPackage reads a package's latest version and README from the npm
// registry.
func (s *service) PiPackage(ctx context.Context, name string) (pipkg.Details, error) {
	return pipkg.Package(ctx, name)
}

// InstallPiPackage installs a package for the user in the background: an
// npm package by name, at its latest version, or any source pi takes.
func (s *service) InstallPiPackage(source string) (PiJobs, error) {
	source = strings.TrimSpace(source)
	if !strings.Contains(source, ":") && !strings.ContainsAny(source, `/\~`) || strings.HasPrefix(source, "@") {
		// A bare name is an npm package's.
		src, err := pipkg.NPM(source)
		if err != nil {
			return PiJobs{}, sourceError(err)
		}
		source = src.Raw
	}
	return s.app.piJobs.Queue(PiInstall, source, "")
}

// UpdatePiPackage updates one installed package, or all of them for an
// empty source, in the background.
func (s *service) UpdatePiPackage(source, title string) (PiJobs, error) {
	return s.app.piJobs.Queue(PiUpdate, strings.TrimSpace(source), title)
}

// RemovePiPackage removes an installed package in the background.
func (s *service) RemovePiPackage(source, title string) (PiJobs, error) {
	return s.app.piJobs.Queue(PiRemove, strings.TrimSpace(source), title)
}

// PiJobs lists the package changes under way and those that failed.
func (s *service) PiJobs() PiJobs { return s.app.piJobs.List() }

// RetryPiJob tries a package change that failed again.
func (s *service) RetryPiJob(id int) { s.app.piJobs.Retry(id) }

// DismissPiJob forgets a package change that failed.
func (s *service) DismissPiJob(id int) { s.app.piJobs.Dismiss(id) }

// staleAgents are the agents whose pi process started before the packages
// last changed, and so runs with the packages as they were.
func staleAgents(list []agent.Info, changedAt time.Time) []agent.Info {
	var out []agent.Info
	if changedAt.IsZero() {
		return out
	}
	for _, i := range list {
		switch i.Status {
		case agent.Stopped, agent.Failed:
			continue
		}
		if !i.StartedAt.IsZero() && i.StartedAt.Before(changedAt) {
			out = append(out, i)
		}
	}
	return out
}

// SleepStaleAgents puts the agents that run with the packages as they were
// before the last change to sleep, unless they are busy: working, waiting
// for an answer, starting, or sent a message meanwhile. They load the
// packages as they are now when they next start, resuming their sessions.
// It returns how many it stopped.
func (s *service) SleepStaleAgents() int {
	a := s.app
	changed := a.piJobs.ChangedAt()
	var wg sync.WaitGroup
	var n atomic.Int32
	for _, i := range staleAgents(a.agents.List(), changed) {
		if i.Status != agent.Idle {
			continue
		}
		// The list only picks candidates: the manager checks again.
		wg.Go(func() {
			if a.agents.SleepIfIdle(i.ID, changed) {
				n.Add(1)
			}
		})
	}
	wg.Wait()
	return int(n.Load())
}
