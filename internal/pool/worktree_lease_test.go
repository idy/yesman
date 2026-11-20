package pool

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/safefs/linktest"
)

func TestWorktreeLeaseThroughConfiguredAlias(t *testing.T) {
	p, repo, wt, _ := newTestCheckout(t)
	parent := filepath.Dir(wt.Path)
	alias := filepath.Join(t.TempDir(), "configured-workspace")
	if runtime.GOOS == "windows" {
		linktest.Junction(t, parent, alias)
	} else {
		linktest.Symlink(t, parent, alias)
	}
	name := filepath.Base(wt.Path)
	aliasPath := filepath.Join(alias, name)
	actual, err := os.Stat(wt.Path)
	if err != nil {
		t.Fatal(err)
	}
	other, err := os.Stat(aliasPath)
	if err != nil || !os.SameFile(actual, other) {
		t.Fatal("alias did not reach checkout", err)
	}
	if registered, err := p.HasWorktreeRegistration(t.Context(), wt.Repo, aliasPath); err != nil || !registered {
		t.Fatalf("registration through workspace alias %q = %v, %v (Git path %q)", alias, registered, err, wt.Path)
	}
	if visible, err := p.Worktrees(t.Context(), alias); err != nil || len(visible) != 1 || visible[0].CleanupID != wt.CleanupID {
		t.Fatalf("worktrees under workspace alias = %+v, %v", visible, err)
	}
	before, err := p.treeLock(filepath.Join(parent, "next"))
	if err != nil {
		t.Fatal(err)
	}
	fromAlias, err := p.treeLock(filepath.Join(alias, "next"))
	if err != nil || before != fromAlias {
		t.Fatal("missing checkout received two leases", err)
	}
	if err := os.Mkdir(filepath.Join(parent, "next"), 0700); err != nil {
		t.Fatal(err)
	}
	after, err := p.treeLock(filepath.Join(alias, "next"))
	if err != nil || before != after {
		t.Fatal("creating the directory changed its lease", err)
	}
	if err := p.WithWorktree(wt.Path, func() error {
		err := p.TryReclaimTemporary(t.Context(), alias, wt, func() error { t.Fatal("certified while the Git path was leased"); return nil })
		if err == nil || !strings.Contains(err.Error(), "checkout is in use") {
			t.Fatalf("removal through a configured alias while leased: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatal(err)
	}
	if err := p.TryReclaimTemporary(t.Context(), alias, wt, func() error { return nil }); err != nil {
		t.Fatalf("after lease release: %v", err)
	}
	if registered, err := p.HasWorktreeRegistration(t.Context(), wt.Repo, aliasPath); err != nil || registered {
		t.Fatalf("removed checkout still registered: %v, %v", registered, err)
	}
	// The checkout might also be created using the configured spelling and
	// inspected or removed from the physical workspace path.
	otherWT, err := p.AddWorktree(t.Context(), repo, WorktreeOptions{Dest: aliasPath, Ref: wt.Head})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveTemporaryWorktree(t.Context(), parent, wt); err == nil {
		t.Fatal("old instance removed its replacement")
	}
	if err := p.RemoveTemporaryWorktree(t.Context(), parent, otherWT); err != nil {
		t.Fatalf("blocking removal of checkout created through alias: %v", err)
	}
}
