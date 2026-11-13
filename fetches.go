package main

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/idy/yesman/internal/pool"
)

// FetchState is where fetching the pool stands, for the page.
type FetchState struct {
	// Fetching names the pool repositories being fetched now.
	Fetching []string `json:"fetching"`
	// Errors holds each repository's last fetch error, until a fetch of it
	// succeeds.
	Errors map[string]string `json:"errors"`
}

// fetchSlots is how many repositories fetching all fetches at once.
const fetchSlots = 3

// fetchTimeout bounds one fetch.
const fetchTimeout = 10 * time.Minute

// fetcher fetches pool repositories when the page asks, one or all of
// them, and all of them on a schedule when the user sets one. Checkouts
// still fetch what they check out, through the pool.
type fetcher struct {
	pool *pool.Pool
	// changed is told the state after every change, in order. It must not
	// call back into the fetcher.
	changed func(FetchState)

	mu      sync.Mutex
	running map[string]int // fetches under way, by repository
	errors  map[string]string
	all     bool          // a fetch of all is under way
	every   time.Duration // the schedule; 0 fetches only when asked
	wake    chan struct{} // tells the schedule its interval changed
}

func newFetcher(p *pool.Pool, changed func(FetchState)) *fetcher {
	return &fetcher{pool: p, changed: changed, running: map[string]int{}, errors: map[string]string{}, wake: make(chan struct{}, 1)}
}

// State reports the repositories being fetched and the last errors.
func (f *fetcher) State() FetchState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stateLocked()
}

func (f *fetcher) stateLocked() FetchState {
	s := FetchState{Fetching: []string{}, Errors: map[string]string{}}
	for name := range f.running {
		s.Fetching = append(s.Fetching, name)
	}
	sort.Strings(s.Fetching)
	for name, msg := range f.errors {
		s.Errors[name] = msg
	}
	return s
}

func (f *fetcher) emitLocked() {
	if f.changed != nil {
		f.changed(f.stateLocked())
	}
}

func (f *fetcher) start(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running[name]++
	f.emitLocked()
}

func (f *fetcher) finish(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running[name]--; f.running[name] <= 0 {
		delete(f.running, name)
	}
	if err != nil {
		f.errors[name] = err.Error()
	} else {
		delete(f.errors, name)
	}
	f.emitLocked()
}

// Fetch fetches one repository from its remotes now.
func (f *fetcher) Fetch(ctx context.Context, name string) (pool.Repo, error) {
	r, err := f.pool.Get(name)
	if err != nil {
		return r, err
	}
	f.start(r.Name)
	err = f.pool.FetchRepo(ctx, r, true)
	f.finish(r.Name, err)
	if err != nil {
		return r, err
	}
	return f.pool.Get(r.Name)
}

// FetchAll starts fetching every repository of the pool in the background,
// a few at a time, and returns at once. Force fetches even those fetched
// moments ago. It does nothing while a fetch of all is under way, and
// leaves out repositories being fetched already.
func (f *fetcher) FetchAll(force bool) error {
	repos, err := f.pool.List()
	if err != nil {
		return err
	}
	f.mu.Lock()
	if f.all {
		f.mu.Unlock()
		return nil
	}
	var todo []pool.Repo
	for _, r := range repos {
		if f.running[r.Name] == 0 {
			todo = append(todo, r)
			f.running[r.Name]++
		}
	}
	if len(todo) == 0 {
		f.mu.Unlock()
		return nil
	}
	f.all = true
	f.emitLocked()
	f.mu.Unlock()

	go func() {
		var wg sync.WaitGroup
		slots := make(chan struct{}, fetchSlots)
		for _, r := range todo {
			wg.Add(1)
			slots <- struct{}{}
			go func() {
				defer func() { <-slots; wg.Done() }()
				ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
				defer cancel()
				f.finish(r.Name, f.pool.FetchRepo(ctx, r, force))
			}()
		}
		wg.Wait()
		f.mu.Lock()
		f.all = false
		f.mu.Unlock()
	}()
	return nil
}

// SetInterval sets how often the schedule fetches all repositories; 0
// stops it.
func (f *fetcher) SetInterval(every time.Duration) {
	f.mu.Lock()
	f.every = every
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// Schedule fetches all repositories at the interval set, until ctx ends.
// A changed interval counts from when it changed.
func (f *fetcher) Schedule(ctx context.Context) {
	var tick <-chan time.Time
	var timer *time.Timer
	for {
		f.mu.Lock()
		every := f.every
		f.mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		tick = nil
		if every > 0 {
			timer = time.NewTimer(every)
			tick = timer.C
		}
		select {
		case <-ctx.Done():
			return
		case <-f.wake:
		case <-tick:
			// A repository fetched moments ago, as a checkout does, is
			// left as it is.
			_ = f.FetchAll(false)
		}
	}
}
