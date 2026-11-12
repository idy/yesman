package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/egoist/mygo"

	"github.com/idy/yesman/internal/agent"
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
	for _, y := range ys {
		wts, _ := a.pool.Worktrees(ctx, y.Path)
		if wts == nil {
			wts = []pool.Worktree{}
		}
		st.Yesmen = append(st.Yesmen, YesmanView{Yesman: y, Worktrees: wts})
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
	a.agents.StopYesman(name)
	dest, err := a.yesmen.Archive(name)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_ = a.pool.Prune(ctx)
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

// AddRepo clones a remote into the pool.
func (s *service) AddRepo(url, upstream string) (pool.Repo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	r, err := s.app.pool.Add(ctx, url, upstream)
	if err == nil {
		s.app.notifyChanged()
	}
	return r, err
}

// FetchRepo fetches a pool repository from its remotes now.
func (s *service) FetchRepo(name string) (pool.Repo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := s.app.pool.Fetch(ctx, name, true); err != nil {
		return pool.Repo{}, err
	}
	s.app.notifyChanged()
	return s.app.pool.Get(name)
}

// Checkout checks a worktree out into a yesman's workspace.
func (s *service) Checkout(yesman string, req server.CheckoutRequest) (pool.Worktree, error) {
	y, err := s.app.yesmen.Get(yesman)
	if err != nil {
		return pool.Worktree{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	wt, err := server.Checkout(ctx, s.app.pool, y, req)
	if err == nil {
		s.app.notifyChanged()
	}
	return wt, err
}

// Messages returns an agent's conversation, starting it if needed.
func (s *service) Messages(ctx context.Context, id string) (json.RawMessage, error) {
	if s.app.piErr != nil {
		return nil, s.app.piErr
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

// OpenURL opens a web link from a reply in the default browser.
func (s *service) OpenURL(url string) error {
	return mygo.Shell.OpenExternal(url)
}

// Reveal shows a file or directory in the file manager.
func (s *service) Reveal(path string) error {
	if path == "" {
		return errors.New("no path")
	}
	mygo.Shell.ShowItemInFolder(path)
	return nil
}
