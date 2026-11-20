package pool

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/safefs/linktest"
)

func TestWorktreePrivateHistorySurvivesGC(t *testing.T) {
	p, repo, wt, workspace := newTestCheckout(t)
	pd := run(t, wt.Path, "rev-parse", "--absolute-git-dir")
	log := filepath.Join(pd, "logs", "HEAD")
	// Produce a commit that appears only in the first *old* column of
	// the reflog. Git's `reflog show --format=%H` omits it.
	run(t, repo.Path, "config", "core.logAllRefUpdates", "false")
	if err := os.Remove(log); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	run(t, wt.Path, "switch", "--detach", wt.Head)
	commit := func(message string) string {
		run(t, wt.Path, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", message)
		return run(t, wt.Path, "rev-parse", "HEAD")
	}
	old := commit("private work")
	run(t, repo.Path, "config", "core.logAllRefUpdates", "always")
	detached := commit("later private work")
	run(t, wt.Path, "switch", wt.Branch)
	if got := run(t, wt.Path, "reflog", "show", "--format=%H", "HEAD"); strings.Contains(got, old) {
		t.Fatal("fixture still exposes the old-only commit", got)
	}
	orig := run(t, wt.Path, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit-tree", wt.Head+"^{tree}", "-m", "only in ORIG_HEAD")
	run(t, wt.Path, "update-ref", "ORIG_HEAD", orig)
	before, err := readPrivateHistory(t.Context(), repo, wt.Path, wt)
	if err != nil || !strings.HasPrefix(before["logs/HEAD"], old+" ") {
		t.Fatal("unexpected private history", err)
	}
	// Recover across an interrupted removal. A retry must verify existing
	// pins and leave each earlier snapshot intact.
	if _, err := preserveWorktreeHistory(t.Context(), repo, wt.Path, wt); err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveTemporaryWorktree(t.Context(), workspace, wt); err != nil {
		t.Fatal(err)
	}
	run(t, repo.Path, "reflog", "expire", "--expire=now", "--all")
	run(t, repo.Path, "gc", "--prune=now")
	if err := p.CheckWorktreeCheckpoint(t.Context(), wt); err != nil {
		t.Fatal(err)
	}
	for _, oid := range []string{old, detached, orig} {
		ref := "refs/yesman/worktree-history/" + wt.CleanupID + "/" + oid
		if got := run(t, repo.Path, "rev-parse", ref+"^{commit}"); got != oid {
			t.Fatalf("%s held %s", ref, got)
		}
	}
	for name, raw := range before {
		prefix := "refs/yesman/worktree-metadata/" + wt.CleanupID + "/" + name + "/"
		ref := run(t, repo.Path, "for-each-ref", "--format=%(refname)", prefix)
		data, err := gitBytes(t.Context(), repo.Path, "cat-file", "blob", ref)
		if err != nil || !bytes.Equal(data, []byte(raw)) {
			t.Fatalf("%s metadata recovery failed: %v", name, err)
		}
	}
}

func TestWorktreeReflogRequiresCompleteRecords(t *testing.T) {
	head, prior := strings.Repeat("a", 40), strings.Repeat("b", 40)
	prefix := prior + " " + head + " "
	identity := "A. Contributor <a@example.invalid> 1795204800 +0530"
	for _, name := range []string{"logs/HEAD", "logs/ORIG_HEAD"} {
		for _, tail := range []string{"", "missing identity\n", "name <mail>\n", "name <mail> 100\n", "name <mail> 100 +05\n", identity, identity + "\tpartial message", identity + "\x00\n"} {
			if _, err := (privateHistory{name: prefix + tail}).targets(head); err == nil {
				t.Errorf("%s accepted an incomplete record ending in %q", name, tail)
			}
		}
		for _, tail := range []string{identity + "\n", identity + "\t\n", identity + "\trecord with a tab\tand spaces\n", "作者 <a@example.invalid> 0 -0400\n"} {
			ids, err := (privateHistory{name: prefix + tail}).targets(head)
			if err != nil || ids[prior] != "commit" || ids[head] != "commit" {
				t.Errorf("%s valid record: %v %v", name, ids, err)
			}
		}
		if _, err := (privateHistory{name: ""}).targets(head); err != nil {
			t.Fatalf("empty reflog after expiration: %v", err)
		}
	}
}

func TestWorktreePrivateHistoryHolds(t *testing.T) {
	for _, kind := range []string{"named-ref", "custom-file", "oversized", "malformed", "unreadable-object", "wrong-object-type", "symbolic-pin", "changed-pin", "linked-log"} {
		t.Run(kind, func(t *testing.T) {
			p, repo, wt, workspace := newTestCheckout(t)
			pd := run(t, wt.Path, "rev-parse", "--absolute-git-dir")
			orig := run(t, wt.Path, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit-tree", wt.Head+"^{tree}", "-m", "private work")
			ref := "refs/yesman/worktree-history/" + wt.CleanupID + "/" + orig
			write := func(file, data string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(pd, file), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write("ORIG_HEAD", orig+"\n")
			switch kind {
			case "named-ref":
				run(t, wt.Path, "update-ref", "refs/worktree/local-result", orig)
			case "custom-file":
				write("private-notes", "keep this")
			case "oversized":
				write("ORIG_HEAD", strings.Repeat("a", maxHistoryFile+1))
			case "malformed":
				write("ORIG_HEAD", "not an OID")
			case "unreadable-object":
				write("ORIG_HEAD", strings.Repeat("1", len(orig)))
			case "wrong-object-type":
				write("ORIG_HEAD", run(t, wt.Path, "rev-parse", "HEAD^{tree}"))
			case "symbolic-pin":
				run(t, repo.Path, "update-ref", "refs/heads/local-result", orig)
				run(t, repo.Path, "symbolic-ref", ref, "refs/heads/local-result")
			case "changed-pin":
				run(t, repo.Path, "update-ref", ref, wt.Head)
			case "linked-log":
				target := filepath.Join(t.TempDir(), "ref")
				if err := os.WriteFile(target, []byte(orig+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(pd, "ORIG_HEAD")); err != nil {
					t.Fatal(err)
				}
				linktest.Symlink(t, target, filepath.Join(pd, "ORIG_HEAD"))
			}
			if err := p.TryReclaimTemporary(t.Context(), workspace, wt, func() error { return nil }); err == nil {
				t.Fatal("collected a checkout with unpreserved private state")
			}
			if id, err := readWorktreeInstance(repo.Path, wt.Path); err != nil || id != wt.CleanupID {
				t.Fatal("lost the original checkout", err)
			}
			if kind == "named-ref" {
				// Git resolves this namespace relative to the worktree running
				// maintenance. Keep that exact registration and its ref intact.
				run(t, wt.Path, "gc", "--prune=now")
				if got := run(t, wt.Path, "rev-parse", "refs/worktree/local-result^{commit}"); got != orig {
					t.Fatalf("lost private ref %s", orig)
				}
			}
		})
	}
}
