package yesmen

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestForceWorkspaceGitReceiptRetainsUnresolvedRegistration(t *testing.T) {
	for _, mode := range []string{"existing", "removed-before-intent", "new-external", "replaced-private"} {
		t.Run(mode, func(t *testing.T) {
			s := waitStore(t)
			y, err := s.Get("alice")
			if err != nil {
				t.Fatal(err)
			}
			git := func(dir string, args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
				cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %s: %v", args, out, err)
				}
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			repo := filepath.Join(y.MetaDir(), "tmp", "Z-repo")
			linked := filepath.Join(y.MetaDir(), "tmp", "A-linked")
			must(os.MkdirAll(repo, 0700))
			git(repo, "init", "-b", "main")
			git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "-c", "core.hooksPath="+os.DevNull, "commit", "--allow-empty", "-m", "fixture")
			git(repo, "worktree", "add", "-b", "linked", linked)
			want, err := s.PrepareDeletion(y.Name, y.HiredAt)
			must(err)
			regs := func(context.Context, string) error { return nil }
			_, plan, err := s.PrepareForceDeletion(t.Context(), want, regs, nil)
			must(err)
			if plan == nil {
				t.Fatal("initial preview held")
			}
			stage := filepath.Join(s.root, deletingPrefix+want.Instance+"."+want.Entry)
			stageGit := filepath.Join(stage, ".yesman", "tmp", "Z-repo", ".git")
			stageGitfile := filepath.Join(stage, ".yesman", "tmp", "A-linked", ".git")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fault := interruptDeletion{ctx, func() {
				if _, err := os.Stat(filepath.Join(stage, stagedGitReceipt)); err != nil {
					return
				}
				_, err := os.Lstat(stageGitfile)
				if mode == "existing" || mode == "removed-before-intent" || errors.Is(err, fs.ErrNotExist) {
					cancel()
				}
			}}
			if detached, err := s.DeleteWithCheckoutConfirmation(fault, want, plan, regs, func(Yesman) error { return nil }, nil); !detached || !errors.Is(err, context.Canceled) {
				t.Fatalf("did not interrupt selected phase: %t %v", detached, err)
			}
			private := filepath.Join(stageGit, "worktrees", "A-linked")
			var outside string
			switch mode {
			case "removed-before-intent":
				must(os.Rename(filepath.Dir(stageGitfile), filepath.Join(t.TempDir(), "retained-checkout")))
			case "new-external":
				outside = filepath.Join(t.TempDir(), "external-linked")
				git(stageGit, "--git-dir=.", "worktree", "add", "-b", "new-external", outside)
				git(outside, "rev-parse", "HEAD")
			case "replaced-private":
				keep := filepath.Join(t.TempDir(), "old-private")
				must(os.Rename(private, keep))
				must(os.CopyFS(private, os.DirFS(keep)))
			}
			cold := New(s.root, s.archive)
			rows, err := cold.StoredWorkspaces()
			must(err)
			if len(rows) != 1 || !rows[0].Pending {
				t.Fatal(rows)
			}
			if _, got, err := cold.PrepareForceDeletion(t.Context(), rows[0], regs, nil); got != nil || err == nil || !strings.Contains(err.Error(), "Git") {
				t.Fatalf("admitted unsafe staged Git: %v %v", got, err)
			}
			if mode == "existing" {
				_, err := os.Lstat(stageGitfile)
				must(err)
			}
			if outside != "" {
				git(outside, "rev-parse", "HEAD")
			}
			_, err = os.ReadFile(filepath.Join(private, "gitdir"))
			must(err)
		})
	}
}
