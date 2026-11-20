package pool

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/idy/yesman/internal/safefs/linktest"
)

func TestWorktreeLeaseThroughConfiguredAlias(t *testing.T) {
	p, _, wt, _ := newTestCheckout(t)
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
		if err == nil {
			t.Fatal("removed through a configured alias while leased")
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
}
