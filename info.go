package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/idy/yesman/internal/github"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

// The info panel, beside the conversation, shows a yesman at a glance: its
// knowledge, its worktrees with what each changed or the pull request it
// is in, and the disk its workspace takes. The files pane shows a
// worktree's changes as a tree of the files, opened a level at a time, and
// each file's diff when it is chosen.

const (
	// changesFresh is how long a worktree's changes are reused.
	changesFresh = 3 * time.Second
	// pullsFresh is how long a pull request lookup is reused, unless the
	// page asks again.
	pullsFresh = 2 * time.Minute
	// worktreeFresh is how long finding a worktree by its directory is
	// reused, so that opening the folders of its changes one after another
	// does not ask git for the pool's worktrees each time.
	worktreeFresh = 10 * time.Second
	// usageFresh is how long a measure of a workspace is reused.
	usageFresh = time.Minute
	// maxUsageEntries caps how many files a measure visits.
	maxUsageEntries = 3_000_000
	// Diffs are read up to diffLimit bytes, and up to maxDiffLimit when
	// the page asks for the rest.
	diffLimit    = 256 << 10
	maxDiffLimit = 16 << 20
	// treeBudget is how many rows of the changes tree arrive at once.
	treeBudget = 200
)

// infoCache keeps what the panel measured, so that drawing it again, or
// opening a folder of a diff, does not run git or walk the disk each time.
type infoCache struct {
	mu      sync.Mutex
	changes map[string]*changesEntry // by worktree path
	pulls   map[string]pullEntry     // by repository and branch
	usage   map[string]*usageEntry   // by workspace path
	found   map[string]foundEntry    // by yesman and worktree directory
}

type foundEntry struct {
	at            time.Time
	wt            pool.Worktree
	defaultBranch string
}

type changesEntry struct {
	at   time.Time
	done chan struct{} // closed once measured
	c    *pool.Changes
	err  error
}

type pullEntry struct {
	at time.Time
	pr *github.PullRequest
}

type usageEntry struct {
	at   time.Time
	done chan struct{}
	u    WorkspaceUsage
	err  error
}

func (a *app) infoCache() *infoCache {
	a.infoOnce.Do(func() {
		a.info = &infoCache{changes: map[string]*changesEntry{}, pulls: map[string]pullEntry{}, usage: map[string]*usageEntry{}, found: map[string]foundEntry{}}
	})
	return a.info
}

// KnowledgeInfo sums a yesman's knowledge notes.
type KnowledgeInfo struct {
	Notes     int       `json:"notes"`
	Index     bool      `json:"index"`
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
}

// WorktreeInfo is a worktree with what it changed.
type WorktreeInfo struct {
	pool.Worktree
	// RemoteBranch is the branch it pushes to, for finding its pull request.
	RemoteBranch string            `json:"remoteBranch,omitempty"`
	Base         string            `json:"base,omitempty"`
	Ahead        int               `json:"ahead"`
	Behind       int               `json:"behind"`
	Changes      *pool.ChangeStats `json:"changes,omitempty"`
	Error        string            `json:"error,omitempty"`
}

// YesmanInfo is what the info panel shows of a yesman, but for the pull
// requests and storage, which take longer and arrive by themselves.
type YesmanInfo struct {
	Knowledge KnowledgeInfo  `json:"knowledge"`
	Worktrees []WorktreeInfo `json:"worktrees"`
}

// Info reads a yesman's knowledge and its worktrees with their changes.
func (s *service) Info(ctx context.Context, name string) (YesmanInfo, error) {
	y, err := s.app.yesmen.Get(name)
	if err != nil {
		return YesmanInfo{}, err
	}
	info := YesmanInfo{Knowledge: knowledgeInfo(y), Worktrees: []WorktreeInfo{}}
	snapshot, err := s.app.pool.Snapshot(ctx)
	if err != nil && len(snapshot.Repos) == 0 {
		return info, err
	}
	defaults := map[string]string{}
	for _, r := range snapshot.Repos {
		defaults[r.Name] = r.DefaultBranch
	}
	wts := snapshot.Under(y.Path)
	info.Worktrees = make([]WorktreeInfo, len(wts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, wt := range wts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			w := WorktreeInfo{Worktree: wt}
			w.Dir = filepath.ToSlash(wt.Dir)
			if !wt.Detached {
				w.RemoteBranch = pool.UpstreamBranch(ctx, wt.Path)
				if w.RemoteBranch == "" {
					w.RemoteBranch = wt.Branch
				}
			}
			c, err := s.changes(ctx, wt, defaults[wt.Repo], false)
			if err != nil {
				w.Error = err.Error()
			} else {
				w.Base, w.Ahead, w.Behind = c.Base, c.Ahead, c.Behind
				stats := c.ChangeStats
				w.Changes = &stats
			}
			info.Worktrees[i] = w
		}()
	}
	wg.Wait()
	return info, nil
}

// knowledgeInfo counts the Markdown notes in a yesman's knowledge.
func knowledgeInfo(y yesmen.Yesman) KnowledgeInfo {
	var k KnowledgeInfo
	dir := y.KnowledgeDir()
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		if rel, _ := filepath.Rel(dir, p); rel == "INDEX.md" {
			k.Index = true
		} else {
			k.Notes++
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(k.UpdatedAt) {
			k.UpdatedAt = info.ModTime()
		}
		return nil
	})
	return k
}

// changes measures a worktree's changes, or reuses a fresh measure. Calls
// that arrive while one runs wait for it.
func (s *service) changes(ctx context.Context, wt pool.Worktree, defaultBranch string, refresh bool) (*pool.Changes, error) {
	cache := s.app.infoCache()
	cache.mu.Lock()
	e := cache.changes[wt.Path]
	if e != nil {
		select {
		case <-e.done:
			if !refresh && time.Since(e.at) < changesFresh && e.err == nil {
				cache.mu.Unlock()
				return e.c, nil
			}
			e = nil
		default: // measuring: wait for it
		}
	}
	if e == nil {
		e = &changesEntry{done: make(chan struct{})}
		cache.changes[wt.Path] = e
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			c, err := pool.WorktreeChanges(ctx, wt.Path, defaultBranch, wt.Detached)
			cache.mu.Lock()
			e.c, e.err, e.at = c, err, time.Now()
			cache.mu.Unlock()
			close(e.done)
		}()
	}
	cache.mu.Unlock()
	select {
	case <-e.done:
		return e.c, e.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// measured returns a worktree's last measure, without measuring again, so
// that the folders of one diff open on the same files.
func (s *service) measured(wt pool.Worktree) *pool.Changes {
	cache := s.app.infoCache()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if e := cache.changes[wt.Path]; e != nil {
		select {
		case <-e.done:
			return e.c
		default:
		}
	}
	return nil
}

// worktree finds a yesman's worktree by its directory in the workspace,
// and the default branch of the repository it came from.
func (s *service) worktree(ctx context.Context, name, dir string) (pool.Worktree, string, error) {
	dir = path.Clean(strings.ReplaceAll(dir, "\\", "/"))
	key := name + "\x00" + dir
	cache := s.app.infoCache()
	cache.mu.Lock()
	f, ok := cache.found[key]
	cache.mu.Unlock()
	if ok && time.Since(f.at) < worktreeFresh {
		if _, err := os.Stat(f.wt.Path); err == nil {
			return f.wt, f.defaultBranch, nil
		}
	}
	y, err := s.app.yesmen.Get(name)
	if err != nil {
		return pool.Worktree{}, "", err
	}
	snapshot, err := s.app.pool.Snapshot(ctx)
	if err != nil && len(snapshot.Repos) == 0 {
		return pool.Worktree{}, "", err
	}
	for _, wt := range snapshot.Under(y.Path) {
		if filepath.ToSlash(wt.Dir) != dir {
			continue
		}
		f := foundEntry{at: time.Now(), wt: wt}
		for _, r := range snapshot.Repos {
			if r.Name == wt.Repo {
				f.defaultBranch = r.DefaultBranch
			}
		}
		cache.mu.Lock()
		cache.found[key] = f
		cache.mu.Unlock()
		return wt, f.defaultBranch, nil
	}
	return pool.Worktree{}, "", errors.New(name + " has no worktree " + dir)
}

// ChangesView is a folder of a worktree's changes.
type ChangesView struct {
	Base      string            `json:"base"`
	MergeBase string            `json:"mergeBase"`
	Ahead     int               `json:"ahead"`
	Behind    int               `json:"behind"`
	Stats     pool.ChangeStats  `json:"stats"`
	Nodes     []pool.ChangeNode `json:"nodes"`
	// More counts the folder's entries left out of a long list.
	More int `json:"more,omitempty"`
}

// ChangesQuery asks for a folder of a worktree's changes.
type ChangesQuery struct {
	// Folder is the folder's path in the worktree, "" for the top.
	Folder string `json:"folder"`
	// Refresh measures the changes again; otherwise the folders of one
	// diff come from one measure.
	Refresh bool `json:"refresh,omitempty"`
	// Folders lists folders alone, for a tree without files.
	Folders bool `json:"folders,omitempty"`
	// Flat lists the folder alone, without opening those below it.
	Flat bool `json:"flat,omitempty"`
	// Offset skips the first entries of a long folder, for its next page.
	Offset int `json:"offset,omitempty"`
}

// Changes lists a folder of a worktree's changes, with the folders below
// it opened as far as a small listing allows unless asked for it alone.
func (s *service) Changes(ctx context.Context, name, dir string, q ChangesQuery) (ChangesView, error) {
	wt, def, err := s.worktree(ctx, name, dir)
	if err != nil {
		return ChangesView{}, err
	}
	c := s.measured(wt)
	if c == nil || q.Refresh {
		if c, err = s.changes(ctx, wt, def, q.Refresh); err != nil {
			return ChangesView{}, err
		}
	}
	budget := treeBudget
	if q.Flat {
		budget = 0
	}
	nodes, more, err := c.Tree(q.Folder, pool.TreeOptions{Budget: budget, Folders: q.Folders, Offset: q.Offset})
	if err != nil {
		return ChangesView{}, err
	}
	if nodes == nil {
		nodes = []pool.ChangeNode{}
	}
	return ChangesView{Base: c.Base, MergeBase: c.MergeBase, Ahead: c.Ahead, Behind: c.Behind, Stats: c.ChangeStats, Nodes: nodes, More: more}, nil
}

// Diff returns a changed file's diff, from the changes the tree shows. All
// reads it at up to 16 MB, rather than its first 256 KB.
func (s *service) Diff(ctx context.Context, name, dir, file string, all bool) (pool.FileDiff, error) {
	wt, def, err := s.worktree(ctx, name, dir)
	if err != nil {
		return pool.FileDiff{}, err
	}
	c := s.measured(wt)
	if c == nil {
		if c, err = s.changes(ctx, wt, def, false); err != nil {
			return pool.FileDiff{}, err
		}
	}
	limit := diffLimit
	if all {
		limit = maxDiffLimit
	}
	return c.Diff(ctx, wt.Path, file, limit)
}

// WorktreePull is the pull request of a worktree's branch, or none.
type WorktreePull struct {
	Dir   string              `json:"dir"`
	Pull  *github.PullRequest `json:"pull,omitempty"`
	Error string              `json:"error,omitempty"`
}

// Pulls finds the pull request of each worktree's branch: in its GitHub
// repository, and for a fork in the one it was forked from. Refresh asks
// GitHub again rather than reusing what it said in the last two minutes.
func (s *service) Pulls(ctx context.Context, name string, refresh bool) ([]WorktreePull, error) {
	y, err := s.app.yesmen.Get(name)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.app.pool.Snapshot(ctx)
	if err != nil && len(snapshot.Repos) == 0 {
		return nil, err
	}
	byName := map[string]pool.Repo{}
	for _, r := range snapshot.Repos {
		byName[r.Name] = r
	}
	wts := snapshot.Under(y.Path)
	out := make([]WorktreePull, len(wts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, wt := range wts {
		out[i].Dir = filepath.ToSlash(wt.Dir)
		r, ok := byName[wt.Repo]
		if wt.Detached || wt.Branch == "" || !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			branch := pool.UpstreamBranch(ctx, wt.Path)
			if branch == "" {
				branch = wt.Branch
			}
			pr, err := s.pull(ctx, r, branch, refresh)
			out[i].Pull = pr
			if err != nil {
				out[i].Error = err.Error()
			}
		}()
	}
	wg.Wait()
	return out, nil
}

func (s *service) pull(ctx context.Context, r pool.Repo, branch string, refresh bool) (*github.PullRequest, error) {
	origin, err := github.Parse(r.URL)
	if err != nil {
		return nil, nil // not on GitHub: no pull requests to find
	}
	key := origin.FullName() + "\x00" + branch
	cache := s.app.infoCache()
	cache.mu.Lock()
	e, ok := cache.pulls[key]
	cache.mu.Unlock()
	if ok && !refresh && time.Since(e.at) < pullsFresh {
		return e.pr, nil
	}
	// A fork's pull requests are usually opened in its parent.
	targets := []string{origin.FullName()}
	if up, err := github.Parse(r.Upstream); err == nil && r.Upstream != "" {
		targets = append(targets, up.FullName())
	}
	var pr *github.PullRequest
	for _, target := range targets {
		if pr, err = github.FindPull(ctx, target, origin.Owner, branch); err != nil {
			return nil, err
		} else if pr != nil {
			break
		}
	}
	cache.mu.Lock()
	cache.pulls[key] = pullEntry{at: time.Now(), pr: pr}
	cache.mu.Unlock()
	return pr, nil
}

// UsagePart is a part of a workspace and the disk it takes.
type UsagePart struct {
	// Kind is worktree, knowledge, sessions, yesman (the rest of .yesman)
	// or other.
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Path  string `json:"path"` // relative to the workspace, slashed
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
}

// WorkspaceUsage is the disk a workspace takes, by part, largest first.
type WorkspaceUsage struct {
	Bytes int64       `json:"bytes"`
	Files int         `json:"files"`
	Parts []UsagePart `json:"parts"`
	// Partial says the measure stopped before it visited every file.
	Partial    bool      `json:"partial,omitempty"`
	MeasuredAt time.Time `json:"measuredAt"`
}

// Usage measures the disk a yesman's workspace takes: each worktree, the
// knowledge, the session logs and the rest. Git's objects stay in the
// repo pool, which the yesmen share, and are not counted. Refresh
// measures again rather than reusing a measure of the last minute.
func (s *service) Usage(ctx context.Context, name string, refresh bool) (WorkspaceUsage, error) {
	y, err := s.app.yesmen.Get(name)
	if err != nil {
		return WorkspaceUsage{}, err
	}
	cache := s.app.infoCache()
	cache.mu.Lock()
	e := cache.usage[y.Path]
	if e != nil {
		select {
		case <-e.done:
			if !refresh && time.Since(e.at) < usageFresh && e.err == nil {
				cache.mu.Unlock()
				return e.u, nil
			}
			e = nil
		default:
		}
	}
	if e == nil {
		e = &usageEntry{done: make(chan struct{})}
		cache.usage[y.Path] = e
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			var wts []pool.Worktree
			if snapshot, err := s.app.pool.Snapshot(ctx); err == nil || len(snapshot.Repos) > 0 {
				wts = snapshot.Under(y.Path)
			}
			u, err := measureWorkspace(ctx, y.Path, wts)
			cache.mu.Lock()
			e.u, e.err, e.at = u, err, time.Now()
			cache.mu.Unlock()
			close(e.done)
		}()
	}
	cache.mu.Unlock()
	select {
	case <-e.done:
		return e.u, e.err
	case <-ctx.Done():
		return WorkspaceUsage{}, ctx.Err()
	}
}

// measureWorkspace adds up the sizes of a workspace's files by part. It
// does not follow symbolic links.
func measureWorkspace(ctx context.Context, root string, wts []pool.Worktree) (WorkspaceUsage, error) {
	type part struct {
		prefix string // slashed, relative, with a trailing slash
		UsagePart
	}
	var parts []*part
	for _, wt := range wts {
		rel := filepath.ToSlash(wt.Dir)
		parts = append(parts, &part{prefix: rel + "/", UsagePart: UsagePart{Kind: "worktree", Name: rel, Path: rel}})
	}
	parts = append(parts,
		&part{prefix: ".yesman/knowledge/", UsagePart: UsagePart{Kind: "knowledge", Path: ".yesman/knowledge"}},
		&part{prefix: ".yesman/sessions/", UsagePart: UsagePart{Kind: "sessions", Path: ".yesman/sessions"}},
		&part{prefix: ".yesman/", UsagePart: UsagePart{Kind: "yesman", Path: ".yesman"}},
	)
	// The longest prefix wins: a worktree inside another counts as itself.
	sort.SliceStable(parts, func(i, j int) bool { return len(parts[i].prefix) > len(parts[j].prefix) })
	other := &part{UsagePart: UsagePart{Kind: "other", Path: "."}}
	u := WorkspaceUsage{}
	visits := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip what cannot be read
		}
		if visits++; visits > maxUsageEntries {
			u.Partial = true
			return fs.SkipAll
		}
		if visits%4096 == 0 && ctx.Err() != nil {
			u.Partial = true
			return fs.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		into := other
		for _, pt := range parts {
			if strings.HasPrefix(rel, pt.prefix) {
				into = pt
				break
			}
		}
		size := info.Size()
		if !info.Mode().IsRegular() {
			size = 0 // links and the like take next to nothing
		}
		into.Bytes += size
		into.Files++
		u.Bytes += size
		u.Files++
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return u, err
	}
	if _, statErr := os.Stat(root); statErr != nil {
		return u, statErr
	}
	u.Parts = []UsagePart{}
	for _, pt := range append(parts, other) {
		if pt.Files > 0 || pt.Kind == "worktree" {
			u.Parts = append(u.Parts, pt.UsagePart)
		}
	}
	sort.SliceStable(u.Parts, func(i, j int) bool { return u.Parts[i].Bytes > u.Parts[j].Bytes })
	u.MeasuredAt = time.Now()
	return u, nil
}
