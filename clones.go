package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/pool"
)

// Clone is a repository on its way into the pool.
type Clone struct {
	Repo  string     `json:"repo"` // owner/repo
	State CloneState `json:"state"`
	Error string     `json:"error,omitempty"`

	input string // as it was asked for, which may name a protocol
}

// CloneState is where a clone stands.
type CloneState string

const (
	CloneQueued  CloneState = "queued"  // waiting for a slot
	CloneCloning CloneState = "cloning" // under way
	CloneFailed  CloneState = "failed"  // stopped, with its error
)

// cloneSlots is how many of the page's additions clone at once.
const cloneSlots = 3

// cloneTimeout bounds one clone.
const cloneTimeout = 20 * time.Minute

// clones adds repositories to the pool in the background, so that adding
// returns at once and the page shows each repository cloning, the way it
// shows one fetching. A clone that fails stays listed with its error until
// it is tried again or dismissed. Clones end with the app; a repository
// appears in the pool only once its clone is complete.
type clones struct {
	// add resolves a repository and clones it into the pool.
	add func(ctx context.Context, repo string) (pool.Repo, error)
	// changed is told the list after every change, in order, and whether a
	// repository came into the pool. It must not call back into clones.
	changed func(list []Clone, arrived bool)

	mu     sync.Mutex
	jobs   []*Clone // in the order they were asked for
	active int      // queued clones that took a slot and are under way
}

func newClones(add func(context.Context, string) (pool.Repo, error), changed func([]Clone, bool)) *clones {
	return &clones{add: add, changed: changed}
}

// addRepo resolves a GitHub repository and clones it into a pool.
func addRepo(p *pool.Pool) func(context.Context, string) (pool.Repo, error) {
	return func(ctx context.Context, repo string) (pool.Repo, error) {
		gh, err := github.Resolve(ctx, repo)
		if err != nil {
			return pool.Repo{}, err
		}
		return p.Add(ctx, gh.URL, gh.Upstream)
	}
}

// List returns the clones under way and the ones that failed.
func (c *clones) List() []Clone {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listLocked()
}

func (c *clones) listLocked() []Clone {
	list := make([]Clone, 0, len(c.jobs))
	for _, j := range c.jobs {
		list = append(list, *j)
	}
	return list
}

// emitLocked tells changed while c.mu is held, so lists arrive in order.
func (c *clones) emitLocked(arrived bool) {
	if c.changed != nil {
		c.changed(c.listLocked(), arrived)
	}
}

func (c *clones) find(repo string) (int, *Clone) {
	for i, j := range c.jobs {
		if strings.EqualFold(j.Repo, repo) {
			return i, j
		}
	}
	return -1, nil
}

// Queue starts cloning GitHub repositories, owner/repo or their URLs, in
// the background, a few at a time in the order asked. It checks them all
// before it queues any. A repository already on its way stays as it is;
// one that failed is tried again.
func (c *clones) Queue(inputs []string) ([]Clone, error) {
	asked := make([]*Clone, 0, len(inputs))
	for _, in := range inputs {
		r, err := github.Parse(in)
		if err != nil {
			return nil, err
		}
		asked = append(asked, &Clone{Repo: r.FullName(), State: CloneQueued, input: strings.TrimSpace(in)})
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	queued := false
	for _, a := range asked {
		switch _, j := c.find(a.Repo); {
		case j == nil:
			c.jobs = append(c.jobs, a)
		case j.State == CloneFailed:
			j.State, j.Error, j.input = CloneQueued, "", a.input
		default:
			continue
		}
		queued = true
	}
	if queued {
		c.startLocked()
		c.emitLocked(false)
	}
	return c.listLocked(), nil
}

// startLocked starts queued clones, oldest first, while slots are free.
func (c *clones) startLocked() {
	for _, j := range c.jobs {
		if c.active >= cloneSlots {
			return
		}
		if j.State == CloneQueued {
			j.State = CloneCloning
			c.active++
			go c.run(j.Repo, j.input)
		}
	}
}

func (c *clones) run(name, input string) {
	ctx, cancel := context.WithTimeout(context.Background(), cloneTimeout)
	_, err := c.add(ctx, input)
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active--
	if i, j := c.find(name); err != nil && j != nil {
		j.State, j.Error = CloneFailed, err.Error()
	} else if i >= 0 {
		c.jobs = append(c.jobs[:i], c.jobs[i+1:]...)
	}
	c.startLocked()
	c.emitLocked(err == nil)
}

func (c *clones) remove(name string, arrived bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i, _ := c.find(name); i >= 0 {
		c.jobs = append(c.jobs[:i], c.jobs[i+1:]...)
	}
	c.emitLocked(arrived)
}

// Retry queues a clone that failed again, as it was first asked for.
func (c *clones) Retry(repo string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, j := c.find(repo); j != nil && j.State == CloneFailed {
		j.State, j.Error = CloneQueued, ""
		c.startLocked()
		c.emitLocked(false)
	}
}

// Dismiss forgets a clone that failed.
func (c *clones) Dismiss(repo string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i, j := c.find(repo); j != nil && j.State == CloneFailed {
		c.jobs = append(c.jobs[:i], c.jobs[i+1:]...)
		c.emitLocked(false)
	}
}

// Add clones one repository now, for an agent that waits for it, and lists
// it as cloning meanwhile. It takes no slot and leaves no failure behind:
// the agent gets the error. A clone of it the page queued is taken over.
func (c *clones) Add(ctx context.Context, input string) (pool.Repo, error) {
	r, err := github.Parse(input)
	if err != nil {
		return pool.Repo{}, err
	}
	name := r.FullName()
	c.mu.Lock()
	_, j := c.find(name)
	mine := j == nil || j.State != CloneCloning
	if j == nil {
		c.jobs = append(c.jobs, &Clone{Repo: name, State: CloneCloning, input: input})
	} else if mine {
		j.State, j.Error = CloneCloning, ""
	}
	if mine {
		c.emitLocked(false)
	}
	c.mu.Unlock()
	// The pool clones a repository once: when the page's clone of it is
	// under way, this one waits for it and then finds it in the pool.
	repo, err := c.add(ctx, input)
	if mine {
		c.remove(name, err == nil)
	}
	return repo, err
}
