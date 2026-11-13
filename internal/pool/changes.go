package pool

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// FileChange is a file a worktree changed against its base.
type FileChange struct {
	// Path is relative to the worktree, separated by slashes.
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	// Status is added, modified, deleted, renamed, copied, typechange or
	// untracked: a new file git does not know yet.
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
	// Large is an untracked file too big to count its lines.
	Large bool `json:"large,omitempty"`
	// Uncommitted says the file has changes not committed yet.
	Uncommitted bool `json:"uncommitted,omitempty"`
}

// ChangeStats sums changed files.
type ChangeStats struct {
	Files     int `json:"files"`
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	// Uncommitted counts the files with changes not committed yet.
	Uncommitted int `json:"uncommitted"`
}

func (s *ChangeStats) add(f *FileChange) {
	s.Files++
	s.Additions += f.Additions
	s.Deletions += f.Deletions
	if f.Uncommitted {
		s.Uncommitted++
	}
}

// Changes is what a worktree changed since its work started: the commits
// of its branch since it left the remote's default branch, and the edits
// not committed yet, new files too.
type Changes struct {
	// Base is what the worktree is compared with: origin/<default branch>,
	// or HEAD for a detached worktree or a repository without one.
	Base string `json:"base"`
	// MergeBase is the commit the diff starts from.
	MergeBase string `json:"mergeBase"`
	// Ahead and Behind count the commits of HEAD the base lacks, and the
	// other way round.
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
	ChangeStats
	// List is the changed files, sorted by path.
	List []FileChange `json:"-"`

	once sync.Once
	root *changeDir
}

const (
	// untrackedCountLimit is the largest new file whose lines are counted.
	untrackedCountLimit = 4 << 20
	// maxCountedDiff is how far past its limit a diff is measured.
	maxCountedDiff = 64 << 20
)

// WorktreeChanges compares a worktree with the default branch of the
// pool repository it came from. A detached worktree, checked out to read a
// ref, is compared with its HEAD, so only edits count.
func WorktreeChanges(ctx context.Context, dir, defaultBranch string, detached bool) (*Changes, error) {
	c := &Changes{Base: "HEAD"}
	head, err := git(ctx, dir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	c.MergeBase = head
	if !detached && defaultBranch != "" {
		ref := "origin/" + defaultBranch
		if mb, err := git(ctx, dir, "merge-base", "HEAD", ref); err == nil && mb != "" {
			c.Base, c.MergeBase = ref, mb
			if counts, err := git(ctx, dir, "rev-list", "--left-right", "--count", ref+"...HEAD"); err == nil {
				if behind, ahead, ok := strings.Cut(counts, "\t"); ok {
					c.Behind, _ = strconv.Atoi(strings.TrimSpace(behind))
					c.Ahead, _ = strconv.Atoi(strings.TrimSpace(ahead))
				}
			}
		}
	}

	// git diff <commit> compares the commit with the working tree, which
	// takes in staged and unstaged edits alike.
	stat, err := gitBytes(ctx, dir, "diff", "-z", "-M", "--numstat", "--no-ext-diff", "--no-textconv", c.MergeBase)
	if err != nil {
		return nil, err
	}
	names, err := gitBytes(ctx, dir, "diff", "-z", "-M", "--name-status", "--no-ext-diff", c.MergeBase)
	if err != nil {
		return nil, err
	}
	status, err := gitBytes(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return nil, err
	}
	files := parseNameStatus(names)
	applyNumstat(files, stat)
	dirty, untracked := parseStatus(status)
	for i := range files {
		f := &files[i]
		f.Uncommitted = dirty[f.Path] || f.OldPath != "" && dirty[f.OldPath]
	}
	for _, p := range untracked {
		f := FileChange{Path: p, Status: "untracked", Uncommitted: true}
		countLines(filepath.Join(dir, filepath.FromSlash(p)), &f)
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	c.List = files
	for i := range files {
		c.add(&files[i])
	}
	return c, nil
}

var statusNames = map[byte]string{'A': "added", 'M': "modified", 'D': "deleted", 'R': "renamed", 'C': "copied", 'T': "typechange"}

// parseNameStatus reads git diff -z --name-status: a status, then one
// path, or two for a rename or copy.
func parseNameStatus(out []byte) []FileChange {
	fields := splitNUL(out)
	var files []FileChange
	for i := 0; i < len(fields); i++ {
		code := fields[i]
		if code == "" {
			continue
		}
		name, ok := statusNames[code[0]]
		if !ok {
			name = "modified"
		}
		if (code[0] == 'R' || code[0] == 'C') && i+2 < len(fields) {
			files = append(files, FileChange{OldPath: fields[i+1], Path: fields[i+2], Status: name})
			i += 2
		} else if i+1 < len(fields) {
			files = append(files, FileChange{Path: fields[i+1], Status: name})
			i++
		}
	}
	return files
}

// applyNumstat reads git diff -z --numstat, "added\tdeleted\tpath" or for
// a rename "added\tdeleted\t" then both paths, into the files it names.
// Binary files count "-" lines.
func applyNumstat(files []FileChange, out []byte) {
	at := make(map[string]int, len(files))
	for i, f := range files {
		at[f.Path] = i
	}
	fields := splitNUL(out)
	for i := 0; i < len(fields); i++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) < 3 {
			continue
		}
		p := parts[2]
		if p == "" && i+2 < len(fields) {
			p = fields[i+2]
			i += 2
		}
		j, ok := at[p]
		if !ok {
			continue
		}
		if parts[0] == "-" {
			files[j].Binary = true
			continue
		}
		files[j].Additions, _ = strconv.Atoi(parts[0])
		files[j].Deletions, _ = strconv.Atoi(parts[1])
	}
}

// parseStatus reads git status --porcelain=v1 -z: the paths with changes
// not committed, and the untracked ones among them.
func parseStatus(out []byte) (dirty map[string]bool, untracked []string) {
	dirty = map[string]bool{}
	for _, entry := range splitNUL(out) {
		if len(entry) < 4 {
			continue
		}
		p := entry[3:]
		dirty[p] = true
		if entry[:2] == "??" {
			untracked = append(untracked, p)
		}
	}
	return dirty, untracked
}

func splitNUL(out []byte) []string {
	s := strings.TrimSuffix(string(out), "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

// countLines counts a new file's lines as additions, unless it is binary
// or too large to read.
func countLines(path string, f *FileChange) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return
	} else if info.Size() > untrackedCountLimit {
		f.Large = true
		return
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		f.Binary = true
		return
	}
	f.Additions = bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		f.Additions++
	}
}

// File returns the changed file at a path.
func (c *Changes) File(path string) (FileChange, bool) {
	i := sort.Search(len(c.List), func(i int) bool { return c.List[i].Path >= path })
	if i < len(c.List) && c.List[i].Path == path {
		return c.List[i], true
	}
	return FileChange{}, false
}

// ChangeNode is a file or directory of a worktree's changes, as a tree
// shows them. A directory holding only one directory is shown joined with
// it, as "a/b", so that long chains take one row.
type ChangeNode struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir,omitempty"`
	// Files counts a directory's changed files, at any depth; Direct
	// counts those right in it, and Dirs its directories with changes.
	Files  int `json:"files,omitempty"`
	Direct int `json:"direct,omitempty"`
	Dirs   int `json:"dirs,omitempty"`
	FileChange
	// Children are filled for a directory listed within the budget, and
	// nil for one the page asks about when it opens.
	Children []ChangeNode `json:"children,omitempty"`
	// More counts entries of a long directory left out.
	More int `json:"more,omitempty"`
}

type changeDir struct {
	name, path string
	dirs       map[string]*changeDir
	order      []string // dirs' names, sorted once the tree is built
	files      []int    // indexes into Changes.List, sorted by name
	stats      ChangeStats
}

// maxNodeChildren caps how many entries of one directory a listing holds.
const maxNodeChildren = 1000

func (c *Changes) tree() *changeDir {
	c.once.Do(func() {
		c.root = &changeDir{path: "", dirs: map[string]*changeDir{}}
		for i := range c.List {
			f := &c.List[i]
			d := c.root
			d.stats.add(f)
			parts := strings.Split(f.Path, "/")
			for _, part := range parts[:len(parts)-1] {
				next := d.dirs[part]
				if next == nil {
					next = &changeDir{name: part, path: joinRel(d.path, part), dirs: map[string]*changeDir{}}
					d.dirs[part] = next
				}
				d = next
				d.stats.add(f)
			}
			d.files = append(d.files, i)
		}
		// Sort each directory once, so that listing a page of a long one
		// costs only that page.
		var sortDir func(d *changeDir)
		sortDir = func(d *changeDir) {
			d.order = make([]string, 0, len(d.dirs))
			for name, sub := range d.dirs {
				d.order = append(d.order, name)
				sortDir(sub)
			}
			sort.Slice(d.order, func(i, j int) bool { return lessName(d.order[i], d.order[j]) })
			sort.Slice(d.files, func(i, j int) bool {
				return lessName(baseName(c.List[d.files[i]].Path), baseName(c.List[d.files[j]].Path))
			})
		}
		sortDir(c.root)
	})
	return c.root
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// TreeOptions says how much of the changes Tree lists.
type TreeOptions struct {
	// Budget is how many rows a listing may hold: folders below the one
	// asked about are opened too, breadth first, while their entries fit.
	Budget int
	// Folders lists folders alone, leaving their files out.
	Folders bool
	// Offset skips the first entries of the folder asked about, to list a
	// long one a page at a time.
	Offset int
}

// Tree lists a directory of the changes, "" for the top, and how many of
// its entries it left out after the page it lists. Directories below it
// open while the listing stays within its budget, so that a small diff
// arrives whole and a large one a level at a time.
func (c *Changes) Tree(dir string, o TreeOptions) ([]ChangeNode, int, error) {
	d := c.find(dir)
	if d == nil {
		return nil, 0, fmt.Errorf("%s has no changes", dir)
	}
	nodes, more := c.list(d, o.Folders, o.Offset)
	used := len(nodes)
	queue := []*[]ChangeNode{&nodes}
	for len(queue) > 0 && used < o.Budget {
		level := queue[0]
		queue = queue[1:]
		for i := range *level {
			n := &(*level)[i]
			if !n.Dir {
				continue
			}
			sub := c.find(n.Path)
			if sub == nil {
				continue
			}
			size := len(sub.dirs)
			if !o.Folders {
				size += len(sub.files)
			}
			if size == 0 || used+size > o.Budget {
				continue
			}
			n.Children, n.More = c.list(sub, o.Folders, 0)
			used += len(n.Children)
			queue = append(queue, &n.Children)
		}
	}
	return nodes, more, nil
}

// find returns the directory at a path of the changes, "" for the top.
func (c *Changes) find(p string) *changeDir {
	d := c.tree()
	if p = strings.Trim(p, "/"); p == "" || p == "." {
		return d
	}
	for _, part := range strings.Split(p, "/") {
		if d = d.dirs[part]; d == nil {
			return nil
		}
	}
	return d
}

// list returns a page of a directory's entries, directories first, and how
// many come after it.
func (c *Changes) list(d *changeDir, folders bool, offset int) ([]ChangeNode, int) {
	total := len(d.order)
	if !folders {
		total += len(d.files)
	}
	start := min(max(offset, 0), total)
	end := min(start+maxNodeChildren, total)
	nodes := make([]ChangeNode, 0, end-start)
	for i := start; i < end; i++ {
		if i >= len(d.order) {
			f := c.List[d.files[i-len(d.order)]]
			nodes = append(nodes, ChangeNode{Name: baseName(f.Path), Path: f.Path, FileChange: f})
			continue
		}
		sub := d.dirs[d.order[i]]
		label := sub.name
		// A directory holding one directory and nothing else joins it.
		for len(sub.files) == 0 && len(sub.dirs) == 1 {
			sub = sub.dirs[sub.order[0]]
			label += "/" + sub.name
		}
		n := ChangeNode{Name: label, Path: sub.path, Dir: true, Files: sub.stats.Files, Direct: len(sub.files), Dirs: len(sub.dirs)}
		n.Additions, n.Deletions = sub.stats.Additions, sub.stats.Deletions
		n.Uncommitted = sub.stats.Uncommitted > 0
		nodes = append(nodes, n)
	}
	return nodes, total - end
}

func baseName(p string) string { return p[strings.LastIndexByte(p, '/')+1:] }

func lessName(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return la < lb
	}
	return a < b
}

// FileDiff is one file's diff, as git prints it.
type FileDiff struct {
	FileChange
	// Patch is the unified diff, without git's header lines, cut at the
	// limit asked for: Truncated says so, and Size is how long it is.
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated,omitempty"`
	Size      int    `json:"size"`
}

// ErrNotChanged says a file is not among a worktree's changes.
var ErrNotChanged = errors.New("the file has no changes")

// Diff returns a changed file's diff, reading at most limit bytes of it.
func (c *Changes) Diff(ctx context.Context, dir, path string, limit int) (FileDiff, error) {
	f, ok := c.File(path)
	if !ok {
		return FileDiff{}, ErrNotChanged
	}
	d := FileDiff{FileChange: f}
	if f.Binary || f.Large {
		return d, nil
	}
	if f.Status == "untracked" {
		return untrackedDiff(filepath.Join(dir, filepath.FromSlash(path)), d, limit)
	}
	paths := []string{f.Path}
	if f.OldPath != "" {
		paths = append(paths, f.OldPath)
	}
	args := append([]string{"diff", "-M", "--no-color", "--no-ext-diff", "--no-textconv", "-U3", c.MergeBase, "--"}, paths...)
	out, truncated, size, err := gitHead(ctx, dir, limit, args...)
	if err != nil {
		return d, err
	}
	d.Patch, d.Truncated, d.Size = stripHeader(out), truncated, size
	return d, nil
}

// stripHeader drops the lines before a diff's first hunk: diff --git,
// index, ---, +++ and the like, which the page shows otherwise.
func stripHeader(patch string) string {
	if i := strings.Index(patch, "\n@@"); i >= 0 {
		return patch[i+1:]
	}
	if strings.HasPrefix(patch, "@@") {
		return patch
	}
	return ""
}

// untrackedDiff shows a new file as one hunk of added lines.
func untrackedDiff(path string, d FileDiff, limit int) (FileDiff, error) {
	file, err := os.Open(path)
	if err != nil {
		return d, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return d, err
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return d, err
	}
	d.Size = int(info.Size())
	if len(data) > limit {
		data, d.Truncated = data[:limit], true
		if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
			data = data[:i+1]
		}
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return d, nil
	}
	lines := strings.Split(text, "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", d.Additions)
	for _, line := range lines {
		b.WriteByte('+')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	d.Patch = b.String()
	return d, nil
}

// gitHead runs git and keeps the first limit bytes it prints, stopping it
// there. It reports whether it cut the output, and how long that was, or
// at least how much it read.
func gitHead(ctx context.Context, dir string, limit int, args ...string) (string, bool, int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := gitCommand(ctx, dir, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", false, 0, err
	}
	if err := cmd.Start(); err != nil {
		return "", false, 0, err
	}
	r := bufio.NewReaderSize(pipe, 64<<10)
	var out bytes.Buffer
	out.Grow(min(limit, 1<<20))
	n, copyErr := io.CopyN(&out, r, int64(limit)+1)
	truncated := n > int64(limit)
	size := int(n)
	if truncated {
		// Count the rest without keeping it, up to a point, then stop git.
		rest, _ := io.CopyN(io.Discard, r, maxCountedDiff)
		size += int(rest)
		cancel()
	}
	waitErr := cmd.Wait()
	if !truncated {
		if waitErr != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = waitErr.Error()
			}
			return "", false, 0, fmt.Errorf("git %s: %s", args[0], msg)
		}
		if copyErr != nil && !errors.Is(copyErr, io.EOF) {
			return "", false, 0, copyErr
		}
	}
	text := out.Bytes()
	if truncated {
		text = text[:limit]
		if i := bytes.LastIndexByte(text, '\n'); i >= 0 {
			text = text[:i+1]
		}
	}
	return string(text), truncated, size, nil
}

func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0")
	return cmd
}

// gitBytes runs git and returns what it prints untrimmed, for -z output.
func gitBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := gitCommand(ctx, dir, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], msg)
	}
	return stdout.Bytes(), nil
}

// UpstreamBranch returns the name of the remote branch a worktree's branch
// tracks, or "" when it tracks none.
func UpstreamBranch(ctx context.Context, dir string) string {
	ref, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		return ""
	}
	_, branch, ok := strings.Cut(ref, "/")
	if !ok {
		return ""
	}
	return branch
}
