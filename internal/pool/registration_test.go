package pool

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/safefs"
	"github.com/idy/yesman/internal/safefs/linktest"
)

func newTestCheckout(t *testing.T) (*Pool, Repo, Worktree, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "origin")
	newOrigin(t, origin)
	p := New(filepath.Join(tmp, "repos"))
	r, err := p.Add(context.Background(), origin, "")
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(tmp, "workspace")
	wt, err := p.AddWorktree(context.Background(), r, WorktreeOptions{Dest: filepath.Join(workspace, "mine"), Branch: "work", Base: "origin/feature"})
	if err != nil {
		t.Fatal(err)
	}
	return p, r, wt, workspace
}

// Deletion never calls worktree remove/prune, even for a clean or missing
// checkout. A missing registration can still be recovered by its owner.
func TestCheckWorkspaceDeletionRetainsGitState(t *testing.T) {
	p, r, wt, workspace := newTestCheckout(t)
	ctx := t.Context()
	refs := run(t, r.Path, "for-each-ref", "--format=%(refname) %(objectname)")
	blocked := func() {
		t.Helper()
		if err := p.CheckWorkspaceDeletion(ctx, workspace); err == nil || !strings.Contains(err.Error(), wt.Dir) {
			t.Fatalf("expected blocker naming %q, got %v", wt.Dir, err)
		}
		if got := run(t, r.Path, "for-each-ref", "--format=%(refname) %(objectname)"); got != refs {
			t.Fatal("refs changed")
		}
	}
	blocked()
	for _, name := range []string{"README.md", "untracked", ".gitignore", "ignored"} {
		if err := os.WriteFile(filepath.Join(wt.Path, name), []byte("ignored\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	blocked()
	if err := os.Rename(wt.Path, filepath.Join(t.TempDir(), "kept")); err != nil {
		t.Fatal(err)
	}
	blocked()
	if err := p.CheckWorkspaceDeletion(ctx, workspace+"-another"); err != nil {
		t.Fatal(err)
	}
}

// A new checkout records the identity of the directory it was made in.
func TestWorktreeRegistrationRecordsRootIdentity(t *testing.T) {
	_, _, wt, _ := newTestCheckout(t)
	if wt.Root == nil {
		t.Fatalf("no root identity recorded: %s", wt.Warning)
	}
	d, err := safefs.Open(wt.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now, err := d.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if now != *wt.Root || !now.HasBirth {
		t.Fatalf("recorded %+v, directory %+v", *wt.Root, now)
	}
}

// Registration files are read through their parents' handles: ordinary ones
// read, oversized, non-regular and linked ones are refused, and nothing is
// changed by a refusal.
func TestWorktreeRegistrationRefusesUnusualMetadata(t *testing.T) {
	_, r, wt, _ := newTestCheckout(t)
	if id, err := readWorktreeInstance(r.Path, wt.Path); err != nil || id != wt.CleanupID {
		t.Fatalf("ordinary instance: %q %v", id, err)
	}
	private, err := privateWorktreeDir(r.Path, wt.Path)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(private, worktreeInstanceFile)
	original, _ := os.ReadFile(marker)
	// The marker replaced by a link to a well-formed marker elsewhere.
	elsewhere := filepath.Join(t.TempDir(), "marker")
	if err := os.WriteFile(elsewhere, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(marker, marker+".kept"); err != nil {
		t.Fatal(err)
	}
	linktest.Symlink(t, elsewhere, marker)
	if _, err := readWorktreeInstance(r.Path, wt.Path); err == nil {
		t.Fatal("read the instance through a linked marker")
	}
	os.Remove(marker)
	if err := os.Rename(marker+".kept", marker); err != nil {
		t.Fatal(err)
	}
	// An oversized backpointer, and a directory where the marker is.
	back := filepath.Join(private, "gitdir")
	backBytes, _ := os.ReadFile(back)
	big := append(bytes.Repeat([]byte(" "), maxGitFile), backBytes...)
	if err := os.WriteFile(back, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorktreeInstance(r.Path, wt.Path); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("oversized backpointer: %v", err)
	}
	if err := os.WriteFile(back, backBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(marker, marker+".kept"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(marker, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorktreeInstance(r.Path, wt.Path); err == nil {
		t.Fatal("read a directory as the marker")
	}
	os.Remove(marker)
	if err := os.Rename(marker+".kept", marker); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(marker); !bytes.Equal(got, original) {
		t.Fatal("marker changed")
	}
	if id, err := readWorktreeInstance(r.Path, wt.Path); err != nil || id != wt.CleanupID {
		t.Fatalf("restored instance: %q %v", id, err)
	}
	if got, _ := os.ReadFile(elsewhere); !bytes.Equal(got, original) {
		t.Fatal("the link's target changed")
	}
}
