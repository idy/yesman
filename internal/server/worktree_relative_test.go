package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idy/yesman/internal/pool"
)

// Older Git versions silently ignore this setting. Only the relative-path
// feature tests skip there; with a supporting Git the real checkout must first
// succeed, so an instance registration failure cannot be hidden by the check.
func relativeWorktreeAdmin(t *testing.T, wt pool.Worktree) string {
	t.Helper()
	admin := cleanupGit(t, wt.Path, "rev-parse", "--absolute-git-dir")
	back, err := os.ReadFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(strings.TrimSpace(string(back))) {
		t.Skip("installed Git does not create relative worktree pointers for worktree.useRelativePaths")
	}
	return admin
}

func TestRelativeWorktreeLifecycle(t *testing.T) {
	for _, scope := range []string{"repository", "global"} {
		t.Run(scope, func(t *testing.T) {
			f := newCleanupFixture(t)
			if scope == "repository" {
				cleanupGit(t, f.repo.Path, "config", "worktree.useRelativePaths", "true")
			} else {
				config := filepath.Join(t.TempDir(), "gitconfig")
				if err := os.WriteFile(config, []byte("[worktree]\n\tuseRelativePaths = true\n"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GIT_CONFIG_GLOBAL", config)
			}
			wt := f.checkout(t, "review", "relative-review")
			relativeWorktreeAdmin(t, wt)
			if wt.CleanupID == "" || wt.Head != f.head {
				t.Fatalf("invalid managed checkout: %+v", wt)
			}
			// The actual UI/user entry point must also create a marked instance,
			// without gaining agent ownership from the marker.
			user, err := Checkout(context.Background(), f.s.Pool, f.y, CheckoutRequest{Repo: f.repo.Name, Ref: f.head, Dir: "relative-user"})
			if err != nil {
				t.Fatal("UI relative checkout failed", err)
			}
			relativeWorktreeAdmin(t, user)
			if user.CleanupID == "" || user.CleanupID == wt.CleanupID {
				t.Fatal("UI instance missing/reused nonce")
			}
			if out := f.remove(t, f.lead, removeWorktreeRequest{Dir: user.Dir, Checkout: user.CleanupID, Completion: f.completion("pass")}); out.Removed {
				t.Fatal("UI marker granted ownership")
			}
			if out := f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID, Completion: f.completion("pass")}); !out.Removed {
				t.Fatal("relative cleanup failed", out.Reason)
			}
			if registered, err := f.s.Pool.HasWorktreeRegistration(context.Background(), wt.Repo, wt.Path); err != nil || registered {
				t.Fatalf("relative registration remains: %t %v", registered, err)
			}
			if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
				t.Fatal("relative tree remains", err)
			}
			rebuilt := f.checkout(t, "review", wt.Dir)
			relativeWorktreeAdmin(t, rebuilt)
			if rebuilt.Head != f.head || rebuilt.CleanupID == wt.CleanupID {
				t.Fatalf("incorrect relative reconstruction: %+v", rebuilt)
			}
			if out := f.remove(t, f.owner, removeWorktreeRequest{Dir: rebuilt.Dir, Checkout: rebuilt.CleanupID, Completion: f.completion("waiting-release")}); !out.Removed {
				t.Fatal("rebuilt relative cleanup failed", out.Reason)
			}
			if _, err := os.Stat(filepath.Join(user.Path, "README")); err != nil {
				t.Fatal("UI tree affected by managed cleanup", err)
			}
		})
	}
}

func TestWorktreePointerRepresentations(t *testing.T) {
	for _, forwardRelative := range []bool{false, true} {
		for _, backRelative := range []bool{false, true} {
			name := "forward-absolute"
			if forwardRelative {
				name = "forward-relative"
			}
			name += "/back-absolute"
			if backRelative {
				name = strings.TrimSuffix(name, "back-absolute") + "back-relative"
			}
			t.Run(name, func(t *testing.T) {
				f := newCleanupFixture(t)
				cleanupGit(t, f.repo.Path, "config", "worktree.useRelativePaths", "true")
				wt := f.checkout(t, "review", "review")
				admin := relativeWorktreeAdmin(t, wt)
				forward, back := admin, filepath.Join(wt.Path, ".git")
				var err error
				if forwardRelative {
					forward, err = filepath.Rel(wt.Path, admin)
					if err != nil {
						t.Fatal(err)
					}
				}
				if backRelative {
					back, err = filepath.Rel(admin, back)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(wt.Path, ".git"), []byte("gitdir: "+filepath.ToSlash(forward)+"\n"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.ToSlash(back)+"\n"), 0644); err != nil {
					t.Fatal(err)
				}
				if out := f.remove(t, f.owner, removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID, Completion: f.completion("pass")}); !out.Removed {
					t.Fatalf("valid pointer combination retained: %s", out.Reason)
				}
				if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
					t.Fatal("valid mixed-pointer checkout still exists", err)
				}
			})
		}
	}
}

func TestWrongWorktreeBackpointerRetains(t *testing.T) {
	for _, representation := range []string{"absolute", "relative"} {
		t.Run(representation, func(t *testing.T) {
			f := newCleanupFixture(t)
			cleanupGit(t, f.repo.Path, "config", "worktree.useRelativePaths", "true")
			wt := f.checkout(t, "review", "review")
			admin := relativeWorktreeAdmin(t, wt)
			other := f.checkout(t, "review", "other")
			wrong := filepath.Join(other.Path, ".git")
			if representation == "relative" {
				var err error
				wrong, err = filepath.Rel(admin, wrong)
				if err != nil {
					t.Fatal(err)
				}
			}
			wrongBytes := []byte(filepath.ToSlash(wrong) + "\n")
			if err := os.WriteFile(filepath.Join(admin, "gitdir"), wrongBytes, 0644); err != nil {
				t.Fatal(err)
			}
			out := f.remove(t, f.lead, removeWorktreeRequest{Dir: wt.Dir, Checkout: wt.CleanupID, Completion: f.completion("pass")})
			if out.Removed || out.Reason == "" {
				t.Fatal("wrong backpointer accepted", out)
			}
			for _, path := range []string{wt.Path, other.Path} {
				if _, err := os.Stat(filepath.Join(path, "README")); err != nil {
					t.Fatal("wrong pointer cleanup deleted a tree", err)
				}
			}
			if got, err := os.ReadFile(filepath.Join(admin, "gitdir")); err != nil || string(got) != string(wrongBytes) {
				t.Fatal("wrong pointer was repaired", err)
			}
			if got, err := os.ReadFile(filepath.Join(admin, "yesman-instance")); err != nil || string(got) != wt.CleanupID {
				t.Fatal("instance marker was changed", err)
			}
		})
	}
}
