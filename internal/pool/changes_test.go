package pool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeChanges(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	ctx := context.Background()
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "origin")
	newOrigin(t, origin)
	write(t, filepath.Join(origin, "src", "deep", "er", "old.go"), "package er\n\nfunc A() {}\n")
	write(t, filepath.Join(origin, "gone.txt"), "bye\n")
	run(t, origin, "add", ".")
	run(t, origin, "commit", "--quiet", "-m", "more")

	p := New(filepath.Join(tmp, "repos"))
	r, err := p.Add(ctx, origin, "")
	if err != nil {
		t.Fatal(err)
	}
	wt, err := p.AddWorktree(ctx, r, WorktreeOptions{Dest: filepath.Join(tmp, "ws", "origin--work"), Branch: "work"})
	if err != nil {
		t.Fatal(err)
	}

	// Nothing changed yet.
	c, err := WorktreeChanges(ctx, wt.Path, r.DefaultBranch, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Base != "origin/main" || c.ChangeStats.Files != 0 || len(c.List) != 0 {
		t.Fatalf("fresh worktree changes = %+v", c)
	}

	// A commit, a rename, a staged deletion, an unstaged edit, new files.
	write(t, filepath.Join(wt.Path, "committed.txt"), "one\ntwo\n")
	run(t, wt.Path, "add", ".")
	run(t, wt.Path, "commit", "--quiet", "-m", "work")
	run(t, wt.Path, "mv", "src/deep/er/old.go", "src/deep/er/new.go")
	run(t, wt.Path, "rm", "--quiet", "gone.txt")
	write(t, filepath.Join(wt.Path, "README.md"), "hello\nworld\n")
	write(t, filepath.Join(wt.Path, "notes", "todo.md"), "a\nb\nc")
	write(t, filepath.Join(wt.Path, "image.bin"), "\x00\x01\x02")

	c, err = WorktreeChanges(ctx, wt.Path, r.DefaultBranch, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Ahead != 1 || c.Behind != 0 {
		t.Errorf("ahead %d, behind %d; want 1, 0", c.Ahead, c.Behind)
	}
	want := map[string]FileChange{
		"README.md":          {Status: "modified", Additions: 1, Uncommitted: true},
		"committed.txt":      {Status: "added", Additions: 2},
		"gone.txt":           {Status: "deleted", Deletions: 1, Uncommitted: true},
		"src/deep/er/new.go": {Status: "renamed", OldPath: "src/deep/er/old.go", Uncommitted: true},
		"notes/todo.md":      {Status: "untracked", Additions: 3, Uncommitted: true},
		"image.bin":          {Status: "untracked", Binary: true, Uncommitted: true},
	}
	if len(c.List) != len(want) {
		t.Fatalf("changed files %+v", c.List)
	}
	for _, f := range c.List {
		w, ok := want[f.Path]
		w.Path = f.Path
		if !ok || f != w {
			t.Errorf("change %+v, want %+v", f, w)
		}
	}
	if c.Additions != 6 || c.Deletions != 1 || c.Uncommitted != 5 {
		t.Errorf("stats %+v", c.ChangeStats)
	}

	// The tree joins a chain of single folders, and opens what fits.
	nodes, more, err := c.Tree("", TreeOptions{Budget: 100})
	if err != nil || more != 0 {
		t.Fatal(err, more)
	}
	var names []string
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	if got := strings.Join(names, ","); got != "notes,src/deep/er,committed.txt,gone.txt,image.bin,README.md" {
		t.Errorf("top of the tree = %s", got)
	}
	if nodes[1].Path != "src/deep/er" || nodes[1].Files != 1 || len(nodes[1].Children) != 1 || nodes[1].Children[0].Path != "src/deep/er/new.go" {
		t.Errorf("joined folder %+v", nodes[1])
	}
	// With no budget to spare, folders arrive closed.
	nodes, _, _ = c.Tree("", TreeOptions{Budget: 1})
	if nodes[0].Children != nil {
		t.Errorf("folder opened past the budget: %+v", nodes[0])
	}
	if sub, _, err := c.Tree("notes", TreeOptions{Budget: 1}); err != nil || len(sub) != 1 || sub[0].Path != "notes/todo.md" {
		t.Errorf("Tree(notes) = %+v, %v", sub, err)
	}
	if _, _, err := c.Tree("nowhere", TreeOptions{Budget: 10}); err == nil {
		t.Error("a folder without changes listed")
	}

	// Diffs: tracked, new, and cut at the limit.
	d, err := c.Diff(ctx, wt.Path, "README.md", diffTestLimit)
	if err != nil || !strings.HasPrefix(d.Patch, "@@ ") || !strings.Contains(d.Patch, "+world\n") || d.Truncated {
		t.Errorf("README diff = %+v, %v", d, err)
	}
	d, err = c.Diff(ctx, wt.Path, "notes/todo.md", diffTestLimit)
	if err != nil || d.Patch != "@@ -0,0 +1,3 @@\n+a\n+b\n+c\n" {
		t.Errorf("new file diff = %q, %v", d.Patch, err)
	}
	if _, err := c.Diff(ctx, wt.Path, "unchanged.txt", diffTestLimit); err != ErrNotChanged {
		t.Errorf("diff of an unchanged file: %v", err)
	}
	var big strings.Builder
	for i := range 5000 {
		fmt.Fprintf(&big, "line %d\n", i)
	}
	write(t, filepath.Join(wt.Path, "committed.txt"), big.String())
	c, err = WorktreeChanges(ctx, wt.Path, r.DefaultBranch, false)
	if err != nil {
		t.Fatal(err)
	}
	d, err = c.Diff(ctx, wt.Path, "committed.txt", 1000)
	if err != nil || !d.Truncated || len(d.Patch) > 1000 || !strings.HasSuffix(d.Patch, "\n") || d.Size <= 1000 {
		t.Errorf("cut diff: truncated %v, %d bytes of %d, %v", d.Truncated, len(d.Patch), d.Size, err)
	}

	// A detached worktree counts only its edits.
	wt3, err := p.AddWorktree(ctx, r, WorktreeOptions{Dest: filepath.Join(tmp, "ws", "origin@v1"), Ref: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	c, err = WorktreeChanges(ctx, wt3.Path, r.DefaultBranch, true)
	if err != nil || c.Base != "HEAD" || len(c.List) != 0 {
		t.Errorf("detached changes = %+v, %v", c, err)
	}
}

const diffTestLimit = 1 << 20

func TestChangesTreeLongFolder(t *testing.T) {
	c := &Changes{}
	for i := range maxNodeChildren + 5 {
		c.List = append(c.List, FileChange{Path: fmt.Sprintf("many/f%05d.txt", i), Status: "added", Additions: 1})
	}
	nodes, more, err := c.Tree("many", TreeOptions{Budget: 10})
	if err != nil || len(nodes) != maxNodeChildren || more != 5 {
		t.Errorf("long folder: %d nodes, %d more, %v", len(nodes), more, err)
	}
	top, _, _ := c.Tree("", TreeOptions{Budget: 10})
	if len(top) != 1 || top[0].Files != maxNodeChildren+5 || top[0].Direct != maxNodeChildren+5 || top[0].Children != nil {
		t.Errorf("top of a long folder: %+v", top)
	}
	// The rest arrives a page at a time.
	page, more, _ := c.Tree("many", TreeOptions{Offset: maxNodeChildren})
	if len(page) != 5 || more != 0 || page[0].Path != fmt.Sprintf("many/f%05d.txt", maxNodeChildren) {
		t.Errorf("second page: %d nodes, %d more, first %+v", len(page), more, page)
	}
}

func TestChangesTreeFolders(t *testing.T) {
	c := &Changes{List: []FileChange{
		{Path: "README.md"},
		{Path: "a/one.go"},
		{Path: "a/b/two.go"},
		{Path: "a/b/three.go"},
		{Path: "x/y/z/deep.go"},
	}}
	nodes, more, err := c.Tree("", TreeOptions{Budget: 100, Folders: true})
	if err != nil || more != 0 || len(nodes) != 2 {
		t.Fatalf("folders = %+v, %d, %v", nodes, more, err)
	}
	if a := nodes[0]; a.Path != "a" || a.Files != 3 || a.Direct != 1 || a.Dirs != 1 || len(a.Children) != 1 || a.Children[0].Path != "a/b" || a.Children[0].Direct != 2 {
		t.Errorf("folder a = %+v", a)
	}
	if x := nodes[1]; x.Name != "x/y/z" || x.Dirs != 0 || x.Children != nil {
		t.Errorf("joined folder = %+v", x)
	}
	// A folder's own listing still has its files.
	if files, _, _ := c.Tree("a/b", TreeOptions{}); len(files) != 2 || files[0].Path != "a/b/three.go" {
		t.Errorf("files of a/b = %+v", files)
	}
}
