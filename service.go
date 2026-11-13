package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/server"
	"github.com/idy/yesman/internal/yesmen"
)

// service is what the page calls, as mygo.call("Yesman.<Method>", ...).
type service struct{ app *app }

// State is everything the sidebar shows.
type State struct {
	Home    string       `json:"home"`
	Pool    string       `json:"pool"`
	PiError string       `json:"piError,omitempty"`
	Yesmen  []YesmanView `json:"yesmen"`
	Agents  []agent.Info `json:"agents"`
}

// YesmanView is a yesman with its worktrees.
type YesmanView struct {
	yesmen.Yesman
	Worktrees []pool.Worktree `json:"worktrees"`
}

// State returns the yesmen, their worktrees and agents.
func (s *service) State(ctx context.Context) (State, error) {
	a := s.app
	st := State{Home: a.home.Root, Pool: a.pool.Root(), Yesmen: []YesmanView{}, Agents: a.agents.List()}
	if a.piErr != nil {
		st.PiError = a.piErr.Error()
	}
	ys, err := a.yesmen.List()
	if err != nil {
		return st, err
	}
	if len(ys) > 0 {
		snapshot, scanErr := a.pool.Snapshot(ctx)
		if scanErr != nil {
			snapshot.Worktrees = nil
		}
		for _, y := range ys {
			st.Yesmen = append(st.Yesmen, YesmanView{Yesman: y, Worktrees: snapshot.Under(y.Path)})
		}
	}
	return st, nil
}

// Hire creates a yesman. Its lead starts when you first talk to it.
func (s *service) Hire(name string) (yesmen.Yesman, error) {
	y, err := s.app.yesmen.Hire(name)
	if err != nil {
		return y, err
	}
	s.app.notifyChanged()
	s.app.agentsChanged(s.app.agents.List())
	return y, nil
}

// Dismiss stops a yesman's agents and moves its workspace into the archive.
func (s *service) Dismiss(name string) (string, error) {
	a := s.app
	if _, err := a.yesmen.Get(name); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// While Archive holds the workspace no agent can start in it and no
	// checkout can run, so the worktrees listed here are all it has.
	var wts []pool.Worktree
	dest, err := a.yesmen.Archive(name, func(y yesmen.Yesman) error {
		a.agents.StopYesman(name)
		var err error
		wts, err = a.pool.Worktrees(ctx, y.Path)
		return err
	})
	if err != nil {
		return "", err
	}
	// The pool still records the worktrees at their old paths: point it at
	// the archive before pruning, or the prune would delete their records
	// and break the archived checkouts.
	moved := make([]string, 0, len(wts))
	for _, wt := range wts {
		moved = append(moved, filepath.Join(dest, wt.Dir))
	}
	if err := a.pool.Repair(ctx, moved); err != nil {
		log.Print("repair archived worktrees: ", err)
	} else {
		_ = a.pool.Prune(ctx)
	}
	a.notifyChanged()
	a.agentsChanged(a.agents.List())
	return dest, nil
}

// Repos lists the repo pool.
func (s *service) Repos() ([]pool.Repo, error) {
	repos, err := s.app.pool.List()
	if repos == nil {
		repos = []pool.Repo{}
	}
	return repos, err
}

// AddRepos starts cloning GitHub repositories into the pool, owner/repo or
// their URLs, and returns at once; the clones event follows them. A fork's
// parent is fetched too, as its upstream remote.
func (s *service) AddRepos(repos []string) ([]Clone, error) {
	return s.app.clones.Queue(repos)
}

// Clones lists the repositories on their way into the pool.
func (s *service) Clones() []Clone {
	return s.app.clones.List()
}

// RetryClone tries a clone that failed again.
func (s *service) RetryClone(repo string) {
	s.app.clones.Retry(repo)
}

// DismissClone forgets a clone that failed.
func (s *service) DismissClone(repo string) {
	s.app.clones.Dismiss(repo)
}

// OwnerRepos lists a GitHub user's or organization's repositories, to
// choose some to add.
func (s *service) OwnerRepos(owner string) ([]github.Listed, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return github.List(ctx, owner)
}

// FetchRepo fetches a pool repository from its remotes now. The fetches
// event shows it fetching, and its error if it fails.
func (s *service) FetchRepo(name string) (pool.Repo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	return s.app.fetcher.Fetch(ctx, name)
}

// FetchAll starts fetching every pool repository and returns at once; the
// fetches event follows them.
func (s *service) FetchAll() error {
	return s.app.fetcher.FetchAll(true)
}

// Fetches reports the repositories being fetched and their last errors.
func (s *service) Fetches() FetchState {
	return s.app.fetcher.State()
}

// Checkout checks a worktree out into a yesman's workspace.
func (s *service) Checkout(yesman string, req server.CheckoutRequest) (pool.Worktree, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var wt pool.Worktree
	err := s.app.yesmen.Use(yesman, func(y yesmen.Yesman) error {
		var err error
		wt, err = server.Checkout(ctx, s.app.pool, y, req)
		return err
	})
	if err == nil {
		s.app.notifyChanged()
	}
	return wt, err
}

// Board returns a yesman's task board, which its agents keep.
func (s *service) Board(yesman string) (yesmen.Board, error) {
	return s.app.yesmen.Board(yesman)
}

// Messages returns an agent's conversation, starting it if needed.
func (s *service) Messages(ctx context.Context, id string) (agent.Conversation, error) {
	if s.app.piErr != nil {
		return agent.Conversation{}, s.app.piErr
	}
	return s.app.agents.Messages(ctx, id)
}

// Send sends a message to an agent, starting it if needed.
func (s *service) Send(ctx context.Context, id, text string) error {
	if s.app.piErr != nil {
		return s.app.piErr
	}
	return s.app.agents.Prompt(ctx, id, text)
}

// Models lists pi's available models and this agent's effective selection.
func (s *service) Models(ctx context.Context, id string) (agent.ModelOptions, error) {
	return s.app.agents.Models(ctx, id)
}

// SetModel changes an idle agent's model, keeping its conversation.
func (s *service) SetModel(ctx context.Context, id, provider, model string) (agent.Info, error) {
	return s.app.agents.SetModel(ctx, id, provider, model)
}

// SetEffort changes an idle agent's reasoning effort.
func (s *service) SetEffort(ctx context.Context, id, level string) (agent.Info, error) {
	return s.app.agents.SetEffort(ctx, id, level)
}

// Context reads an agent's current context usage and cumulative session statistics.
func (s *service) Context(ctx context.Context, id string) (agent.ContextStats, error) {
	return s.app.agents.Context(ctx, id)
}

// Abort stops an agent's current run.
func (s *service) Abort(ctx context.Context, id string) error {
	return s.app.agents.Abort(ctx, id)
}

// Stop ends an agent's pi process; its session resumes next time.
func (s *service) Stop(id string) {
	s.app.agents.Stop(id)
}

// Answer answers the question an agent waits on.
func (s *service) Answer(id, requestID string, answer map[string]any) error {
	return s.app.agents.Answer(id, requestID, answer)
}

// RemoveWorker stops a worker and removes it from its yesman.
func (s *service) RemoveWorker(id string) error {
	y, name, err := agent.SplitID(id)
	if err != nil {
		return err
	}
	return s.app.agents.RemoveWorker(y, name)
}

// TakeFocus returns the agent a notification click asked to show, once.
// A window opened by that click asks for it when its page is ready, since
// the click's focus-agent event can arrive before the page listens.
func (s *service) TakeFocus() string {
	a := s.app
	a.fmu.Lock()
	defer a.fmu.Unlock()
	id := a.pendingFocus
	a.pendingFocus = ""
	return id
}

// OpenURL opens a web link from a reply in the default browser.
func (s *service) OpenURL(url string) error {
	return mygo.Shell.OpenExternal(url)
}

// Reveal shows a file or directory in the file manager.
func (s *service) Reveal(path string) error {
	if path == "" {
		return errors.New("no path")
	}
	// Settings files may not have been written yet. Reveal their nearest
	// existing parent so the location can still be found.
	for {
		if _, err := os.Stat(path); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return errors.New("the path is unavailable")
		}
		path = parent
	}
	mygo.Shell.ShowItemInFolder(path)
	return nil
}
