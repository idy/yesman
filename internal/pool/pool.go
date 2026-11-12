// Package pool keeps the repo pool: one bare repository per remote, which
// only yesman writes to, and the git worktrees checked out of them.
//
// A pool repository mirrors its remote in refs/remotes/origin/* (and
// refs/remotes/upstream/* for a fork's upstream). Its refs/heads/* hold only
// the branches of worktrees, so a fetch never conflicts with local work.
// The pool is fetched when someone checks a worktree out of it, not on a
// timer.
package pool

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// fetchInterval is how long a fetch counts as fresh: checkouts within it
// reuse the last one.
const fetchInterval = 20 * time.Second

// Pool is the repo pool rooted at a directory.
type Pool struct {
	root string

	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	fetched map[string]time.Time
}

// New returns the pool at root.
func New(root string) *Pool {
	return &Pool{root: root, locks: map[string]*sync.Mutex{}, fetched: map[string]time.Time{}}
}

// Root is the pool's directory.
func (p *Pool) Root() string { return p.root }

// Repo is a repository of the pool.
type Repo struct {
	// Name identifies it by host and path, such as github.com/idy/yesman.
	Name          string    `json:"name"`
	Path          string    `json:"path"`
	URL           string    `json:"url"`
	Upstream      string    `json:"upstream,omitempty"`
	DefaultBranch string    `json:"defaultBranch,omitempty"`
	FetchedAt     time.Time `json:"fetchedAt,omitzero"`
}

// Worktree is a git worktree checked out of a pool repository.
type Worktree struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
	// Dir is the path relative to the directory Worktrees was asked about,
	// or the directory's name for a new checkout.
	Dir      string `json:"dir,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Head     string `json:"head,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	// Warning reports a problem that did not stop the checkout, such as a
	// failed fetch that left the pool's copy of the remote stale.
	Warning string `json:"warning,omitempty"`
}

// NameFromURL derives a pool name from a remote URL: host and path without
// ".git". Local paths and file URLs become local/<directory name>.
func NameFromURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("empty repository URL")
	}
	var host, path string
	switch {
	case strings.HasPrefix(raw, "file://"):
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("parse %q: %w", raw, err)
		}
		return localName(u.Path)
	case !strings.Contains(raw, "://") && filepath.IsAbs(raw):
		return localName(raw)
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("parse %q: %w", raw, err)
		}
		host, path = u.Hostname(), u.Path
	default: // scp-like: [user@]host:owner/repo.git
		i := strings.Index(raw, ":")
		if i <= 0 {
			return "", fmt.Errorf("%q is not a repository URL", raw)
		}
		host, path = raw[:i], raw[i+1:]
		if j := strings.LastIndex(host, "@"); j >= 0 {
			host = host[j+1:]
		}
	}
	host = strings.ToLower(host)
	if !validSegment(host) {
		return "", fmt.Errorf("%q has no valid host", raw)
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	segs := strings.Split(path, "/")
	for _, s := range segs {
		if !validSegment(s) {
			return "", fmt.Errorf("%q has an invalid path", raw)
		}
	}
	return host + "/" + strings.Join(segs, "/"), nil
}

func localName(path string) (string, error) {
	base := strings.TrimSuffix(filepath.Base(filepath.Clean(path)), ".git")
	if !validSegment(base) {
		return "", fmt.Errorf("%q has no usable directory name", path)
	}
	return "local/" + base, nil
}

func validSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func (p *Pool) dir(name string) string {
	return filepath.Join(p.root, filepath.FromSlash(name)) + ".git"
}

func (p *Pool) lock(name string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.locks[name]
	if l == nil {
		l = &sync.Mutex{}
		p.locks[name] = l
	}
	return l
}

// List returns the pool's repositories, sorted by name.
func (p *Pool) List() ([]Repo, error) {
	var repos []Repo
	err := filepath.WalkDir(p.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() || path == p.root {
			return nil
		}
		if strings.HasSuffix(path, ".partial") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(path, ".git") && fileExists(filepath.Join(path, "HEAD")) {
			rel, err := filepath.Rel(p.root, strings.TrimSuffix(path, ".git"))
			if err != nil {
				return err
			}
			repos = append(repos, p.info(filepath.ToSlash(rel), path))
			return filepath.SkipDir
		}
		return nil
	})
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	return repos, err
}

// Get finds a repository by its full name or by a unique trailing part of
// it, such as "yesman" or "idy/yesman".
func (p *Pool) Get(name string) (Repo, error) {
	name = strings.TrimSuffix(strings.Trim(strings.TrimSpace(name), "/"), ".git")
	if name == "" {
		return Repo{}, errors.New("no repository named")
	}
	if strings.Contains(name, "://") || strings.Contains(name, "@") {
		n, err := NameFromURL(name)
		if err != nil {
			return Repo{}, err
		}
		name = n
	}
	repos, err := p.List()
	if err != nil {
		return Repo{}, err
	}
	var matches []Repo
	for _, r := range repos {
		if r.Name == name {
			return r, nil
		}
		if strings.HasSuffix(r.Name, "/"+name) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Repo{}, fmt.Errorf("repository %q is not in the pool; add it first", name)
	default:
		var names []string
		for _, m := range matches {
			names = append(names, m.Name)
		}
		return Repo{}, fmt.Errorf("%q matches several repositories: %s", name, strings.Join(names, ", "))
	}
}

func (p *Pool) info(name, dir string) Repo {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := Repo{Name: name, Path: dir}
	r.URL, _ = git(ctx, dir, "config", "--get", "remote.origin.url")
	r.Upstream, _ = git(ctx, dir, "config", "--get", "remote.upstream.url")
	if head, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		r.DefaultBranch = strings.TrimPrefix(head, "origin/")
	}
	if st, err := os.Stat(filepath.Join(dir, "FETCH_HEAD")); err == nil {
		r.FetchedAt = st.ModTime()
	}
	return r
}

// Add clones a remote into the pool, or returns it when it is already
// there. A non-empty upstream adds the remote a fork was made from.
func (p *Pool) Add(ctx context.Context, rawURL, upstream string) (Repo, error) {
	name, err := NameFromURL(rawURL)
	if err != nil {
		return Repo{}, err
	}
	dir := p.dir(name)
	l := p.lock(name)
	l.Lock()
	defer l.Unlock()

	if fileExists(filepath.Join(dir, "HEAD")) {
		r := p.info(name, dir)
		if upstream != "" && r.Upstream == "" {
			if _, err := git(ctx, dir, "remote", "add", "upstream", upstream); err != nil {
				return r, err
			}
			if _, err := git(ctx, dir, "fetch", "--quiet", "upstream"); err != nil {
				return r, err
			}
			r = p.info(name, dir)
		}
		return r, nil
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return Repo{}, err
	}
	tmp := dir + ".partial"
	_ = os.RemoveAll(tmp)
	if _, err := git(ctx, "", "clone", "--bare", "--quiet", rawURL, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return Repo{}, err
	}
	if err := configure(ctx, tmp, upstream); err != nil {
		_ = os.RemoveAll(tmp)
		return Repo{}, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		return Repo{}, err
	}
	p.mu.Lock()
	p.fetched[name] = time.Now()
	p.mu.Unlock()
	return p.info(name, dir), nil
}

// configure turns a fresh bare clone into a pool repository: the remote's
// branches move to refs/remotes/origin/*, and refs/heads/* is left for the
// branches of worktrees.
func configure(ctx context.Context, dir, upstream string) error {
	steps := [][]string{
		{"config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
	}
	if runtime.GOOS == "windows" {
		steps = append(steps, []string{"config", "core.longpaths", "true"})
	}
	if upstream != "" {
		steps = append(steps, []string{"remote", "add", "upstream", upstream})
	}
	steps = append(steps, []string{"fetch", "--all", "--prune", "--quiet"})
	for _, s := range steps {
		if _, err := git(ctx, dir, s...); err != nil {
			return err
		}
	}
	// The clone's HEAD names the remote's default branch.
	if head, err := git(ctx, dir, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		branch := strings.TrimPrefix(head, "refs/heads/")
		if _, err := git(ctx, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+branch); err != nil {
			return err
		}
	}
	heads, err := git(ctx, dir, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if err != nil {
		return err
	}
	for _, ref := range strings.Fields(heads) {
		if _, err := git(ctx, dir, "update-ref", "-d", ref); err != nil {
			return err
		}
	}
	return nil
}

// Fetch updates a repository from all its remotes, unless it was fetched
// moments ago and force is false.
func (p *Pool) Fetch(ctx context.Context, name string, force bool) error {
	r, err := p.Get(name)
	if err != nil {
		return err
	}
	l := p.lock(r.Name)
	l.Lock()
	defer l.Unlock()
	p.mu.Lock()
	last := p.fetched[r.Name]
	p.mu.Unlock()
	if !force && time.Since(last) < fetchInterval {
		return nil
	}
	if _, err := git(ctx, r.Path, "fetch", "--all", "--prune", "--quiet"); err != nil {
		return err
	}
	p.mu.Lock()
	p.fetched[r.Name] = time.Now()
	p.mu.Unlock()
	return nil
}

// WorktreeOptions describes a checkout.
type WorktreeOptions struct {
	// Repo is a pool name, or a unique trailing part of one.
	Repo string
	// Dest is the absolute path of the new worktree.
	Dest string
	// Branch is checked out, or created when it does not exist yet.
	Branch string
	// Base is where a new branch starts: origin/<branch> when the remote
	// has that branch, otherwise the remote's default branch.
	Base string
	// Ref checks out a tag, commit or remote branch detached, for reading,
	// instead of a branch.
	Ref string
}

// AddWorktree fetches the repository, then checks a worktree out of it.
func (p *Pool) AddWorktree(ctx context.Context, opts WorktreeOptions) (Worktree, error) {
	r, err := p.Get(opts.Repo)
	if err != nil {
		return Worktree{}, err
	}
	if !filepath.IsAbs(opts.Dest) {
		return Worktree{}, fmt.Errorf("worktree path %q is not absolute", opts.Dest)
	}
	if _, err := os.Lstat(opts.Dest); err == nil {
		return Worktree{}, fmt.Errorf("%s already exists", opts.Dest)
	}
	var warning string
	if err := p.Fetch(ctx, r.Name, false); err != nil {
		warning = "fetch failed, so the pool's copy of the remote may be stale: " + err.Error()
	}

	l := p.lock(r.Name)
	l.Lock()
	defer l.Unlock()

	var args []string
	switch {
	case opts.Ref != "":
		args = []string{"worktree", "add", "--detach", opts.Dest, opts.Ref}
	case opts.Branch == "":
		return Worktree{}, errors.New("name a branch to work on, or a ref to read")
	case refExists(ctx, r.Path, "refs/heads/"+opts.Branch):
		args = []string{"worktree", "add", opts.Dest, opts.Branch}
	default:
		base, track := opts.Base, "--no-track"
		if base == "" {
			switch {
			case refExists(ctx, r.Path, "refs/remotes/origin/"+opts.Branch):
				base, track = "origin/"+opts.Branch, "--track"
			case r.DefaultBranch != "":
				base = "origin/" + r.DefaultBranch
			default:
				return Worktree{}, fmt.Errorf("%s has no default branch; name a base", r.Name)
			}
		}
		args = []string{"worktree", "add", track, "-b", opts.Branch, opts.Dest, base}
	}
	if err := os.MkdirAll(filepath.Dir(opts.Dest), 0o755); err != nil {
		return Worktree{}, err
	}
	if _, err := git(ctx, r.Path, args...); err != nil {
		return Worktree{}, err
	}
	wts, err := p.worktrees(ctx, r)
	if err != nil {
		return Worktree{}, err
	}
	dest := realPath(opts.Dest)
	for _, wt := range wts {
		if realPath(wt.Path) == dest {
			wt.Dir = filepath.Base(opts.Dest)
			wt.Warning = warning
			return wt, nil
		}
	}
	return Worktree{Repo: r.Name, Path: opts.Dest, Dir: filepath.Base(opts.Dest), Branch: opts.Branch, Warning: warning}, nil
}

// Worktrees returns the pool's worktrees inside a directory.
func (p *Pool) Worktrees(ctx context.Context, under string) ([]Worktree, error) {
	repos, err := p.List()
	if err != nil {
		return nil, err
	}
	under = realPath(under)
	var out []Worktree
	for _, r := range repos {
		wts, err := p.worktrees(ctx, r)
		if err != nil {
			return nil, err
		}
		for _, wt := range wts {
			real := realPath(wt.Path)
			if within(real, under) {
				wt.Dir, _ = filepath.Rel(under, real)
				out = append(out, wt)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (p *Pool) worktrees(ctx context.Context, r Repo) ([]Worktree, error) {
	text, err := git(ctx, r.Path, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var out []Worktree
	var cur *Worktree
	skip := false
	flush := func() {
		if cur != nil && !skip {
			out = append(out, *cur)
		}
		cur, skip = nil, false
	}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "":
			flush()
		case "worktree":
			flush()
			cur = &Worktree{Repo: r.Name, Path: value}
		case "HEAD":
			if cur != nil {
				cur.Head = value
			}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(value, "refs/heads/")
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "bare", "prunable":
			skip = true
		}
	}
	flush()
	return out, nil
}

// RemoveWorktree removes a worktree checked out of the pool. Force removes
// it even with uncommitted changes.
func (p *Pool) RemoveWorktree(ctx context.Context, path string, force bool) error {
	repoDir, err := p.owner(path)
	if err != nil {
		return err
	}
	args := []string{"worktree", "remove", path}
	if force {
		args = []string{"worktree", "remove", "--force", path}
	}
	_, err = git(ctx, repoDir, args...)
	return err
}

// Prune forgets worktrees whose directories are gone.
func (p *Pool) Prune(ctx context.Context) error {
	repos, err := p.List()
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range repos {
		if _, err := git(ctx, r.Path, "worktree", "prune"); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// owner returns the pool repository a worktree belongs to.
func (p *Pool) owner(path string) (string, error) {
	data, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return "", fmt.Errorf("%s is not a worktree of the pool: %w", path, err)
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		return "", fmt.Errorf("%s is not a worktree of the pool", path)
	}
	// <pool>/<name>.git/worktrees/<id>
	repoDir := filepath.Dir(filepath.Dir(gitdir))
	if !within(realPath(repoDir), realPath(p.root)) {
		return "", fmt.Errorf("%s belongs to %s, not the pool", path, repoDir)
	}
	return repoDir, nil
}

func refExists(ctx context.Context, dir, ref string) bool {
	_, err := git(ctx, dir, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// realPath resolves symbolic links, such as macOS's /tmp, so that paths
// git reports compare with the ones yesman builds.
func realPath(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	// The path may not exist yet: resolve its parent.
	if r, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(r, filepath.Base(path))
	}
	return filepath.Clean(path)
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
