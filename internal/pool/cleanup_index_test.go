package pool

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeIndexConflictRecovery(t *testing.T) {
	p, repo, wt, workspace := newTestCheckout(t)
	if err := p.CheckTemporaryWorktree(t.Context(), workspace, wt); err != nil {
		t.Fatalf("initial inspection of a clean checkout: %v", err)
	}
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wt.Path, "README.md"), []byte(text+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Resolve a conflict to the original source so both HEAD and working
	// files are clean. Git keeps the local version only in its saved stages.
	run(t, wt.Path, "switch", "-qc", "other")
	write("other version")
	run(t, wt.Path, "commit", "-qam", "other version")
	write("handwritten version")
	local := run(t, wt.Path, "hash-object", "README.md")
	run(t, wt.Path, "switch", "--merge", wt.Branch)
	run(t, wt.Path, "checkout", "--ours", "--", "README.md")
	run(t, wt.Path, "add", "README.md")
	before := run(t, wt.Path, "ls-files", "--resolve-undo", "README.md")
	if run(t, wt.Path, "status", "--porcelain=v1", "--ignored") != "" || !strings.Contains(before, local+" 3\tREADME.md") {
		t.Fatal("expected a clean checkout with recoverable conflict stages", before)
	}
	if err := p.CheckTemporaryWorktree(t.Context(), workspace, wt); !errors.Is(err, ErrWorktreeResolveUndo) {
		t.Fatalf("inspection: %v", err)
	}
	if err := p.RemoveTemporaryWorktree(t.Context(), workspace, wt); !errors.Is(err, ErrWorktreeResolveUndo) {
		t.Fatalf("manual removal: %v", err)
	}
	if err := p.TryReclaimTemporary(t.Context(), workspace, wt, func() error { t.Fatal("certified after ignoring the index hold"); return nil }); !errors.Is(err, ErrWorktreeResolveUndo) {
		t.Fatalf("automatic removal: %v", err)
	}
	if got := run(t, wt.Path, "ls-files", "--resolve-undo", "README.md"); got != before {
		t.Fatal("retention changed the saved modes, object IDs or stage numbers")
	}
	run(t, repo.Path, "reflog", "expire", "--expire=now", "--all")
	run(t, repo.Path, "gc", "--prune=now")
	run(t, wt.Path, "checkout", "-m", "--", "README.md")
	if text := run(t, wt.Path, "show", ":3:README.md"); text != "handwritten version" {
		t.Fatal("could not reconstruct the handwritten version after pool GC", text)
	}
}

func TestWorktreeIndexReadFailuresAndSkipHash(t *testing.T) {
	p, repo, wt, workspace := newTestCheckout(t)
	pd, err := privateWorktreeDir(repo.Path, wt.Path)
	if err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(pd, "index")
	original, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	width := len(wt.Head) / 2
	save := func(raw []byte) {
		t.Helper()
		if err := os.WriteFile(index, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	hold := func() {
		t.Helper()
		if err := p.RemoveTemporaryWorktree(t.Context(), workspace, wt); !errors.Is(err, ErrWorktreeIndex) {
			t.Fatalf("unverifiable index: %v", err)
		}
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	hold()
	save([]byte("partial"))
	hold()
	bad := bytes.Clone(original)
	bad[len(bad)-1] ^= 0xff
	save(bad)
	hold()
	if width == sha1.Size {
		// Git skips an unknown optional extension with a warning and exit 0.
		body := append(bytes.Clone(original[:len(original)-width]), "WXYZ\x00\x00\x00\x03new"...)
		sum := sha1.Sum(body)
		save(append(body, sum[:]...))
		hold()
	}
	save(original)
	run(t, wt.Path, "-c", "index.skipHash=true", "update-index", "--force-write-index")
	if err := p.RemoveTemporaryWorktree(t.Context(), workspace, wt); err != nil {
		t.Fatal("ordinary Git index with skipHash enabled", err)
	}
}
