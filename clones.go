package main

import (
	"context"
	"slices"
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

	agents    int                // agents waiting for it, through Add
	cancel    context.CancelFunc // stops it while it clones for the page
	done      chan struct{}      // closed when the page's clone of it ends
	withdrawn bool               // Withdraw stopped it: it leaves no failure
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
			ctx, cancel := context.WithTimeout(context.Background(), cloneTimeout)
			j.State, j.cancel, j.done = CloneCloning, cancel, make(chan struct{})
			c.active++
			go c.run(ctx, j)
		}
	}
}

func (c *clones) run(ctx context.Context, j *Clone) {
	_, err := c.add(ctx, j.input)
	j.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active--
	if i := slices.Index(c.jobs, j); err != nil && !j.withdrawn {
		j.State, j.Error = CloneFailed, err.Error()
	} else if i >= 0 {
		c.jobs = slices.Delete(c.jobs, i, i+1)
	}
	j.cancel, j.withdrawn = nil, false
	close(j.done)
	c.startLocked()
	c.emitLocked(err == nil)
}

// Withdraw stops the page's clones of the repositories keep turns down, as
// replacing the configuration does: queued and failed ones are forgotten,
// and one under way is cancelled and waited for. A clone cancelled too
// late may still land in the pool. It returns the repositories it stopped,
// and those it left because an agent waits for them.
func (c *clones) Withdraw(ctx context.Context, keep func(repo string) bool) (stopped, held []string, err error) {
	c.mu.Lock()
	var wait []chan struct{}
	next := make([]*Clone, 0, len(c.jobs))
	for _, j := range c.jobs {
		switch {
		case keep(j.Repo):
			next = append(next, j)
		case j.agents > 0:
			held = append(held, j.Repo)
			next = append(next, j)
		case j.State == CloneCloning:
			// It stays listed until it ends, which drops it.
			j.withdrawn = true
			j.cancel()
			wait = append(wait, j.done)
			stopped = append(stopped, j.Repo)
			next = append(next, j)
		default:
			stopped = append(stopped, j.Repo)
		}
	}
	if len(next) != len(c.jobs) {
		c.jobs = next
		c.emitLocked(false)
	}
	c.mu.Unlock()
	for _, done := range wait {
		select {
		case <-done:
		case <-ctx.Done():
			return stopped, held, ctx.Err()
		}
	}
	return stopped, held, nil
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
		j = &Clone{Repo: name, State: CloneCloning, input: input}
		c.jobs = append(c.jobs, j)
	} else if mine {
		j.State, j.Error = CloneCloning, ""
	}
	// While an agent waits for a clone, the page's own included, replacing
	// the configuration leaves it alone.
	j.agents++
	if mine {
		c.emitLocked(false)
	}
	c.mu.Unlock()
	// The pool clones a repository once: when the page's clone of it is
	// under way, this one waits for it and then finds it in the pool.
	repo, err := c.add(ctx, input)
	c.mu.Lock()
	j.agents--
	c.mu.Unlock()
	if mine {
		c.remove(name, err == nil)
	}
	return repo, err
}
