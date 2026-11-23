package yesmen

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func deleteFixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, b, err)
	}
}

func mustDeletion(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustDeletionBytes(t *testing.T, path, want string) {
	t.Helper()
	if b, err := os.ReadFile(path); err != nil || string(b) != want {
		t.Fatalf("%s: bytes=%q err=%v", path, b, err)
	}
}

func TestForceWorkspaceColdApprovedGit(t *testing.T) {
	for _, mode := range []string{"bare", "embedded", "module-pointer", "module-admin"} {
		t.Run(mode, func(t *testing.T) {
			s := waitStore(t)
			y, err := s.Get("alice")
			mustDeletion(t, err)
			target, err := s.PrepareDeletion(y.Name, y.HiredAt)
			mustDeletion(t, err)
			gitDir := "A-repo/.git"
			witness := gitDir + "/objects/pack/owned.pack"
			missing := gitDir + "/config"
			switch mode {
			case "bare":
				gitDir = "A-repo.git"
				witness, missing = gitDir+"/objects/pack/owned.pack", gitDir+"/config"
				deleteFixtureGit(t, y.Path, "init", "--bare", "-q", gitDir)
			default:
				deleteFixtureGit(t, y.Path, "init", "-q", "A-repo")
			}
			if strings.HasPrefix(mode, "module-") {
				admin := gitDir + "/modules/sub"
				module := "A-repo/sub"
				mustDeletion(t, os.MkdirAll(filepath.Join(y.Path, module), 0700))
				mustDeletion(t, os.MkdirAll(filepath.Dir(filepath.Join(y.Path, admin)), 0700))
				deleteFixtureGit(t, y.Path, "init", "-q", "--separate-git-dir", admin, module)
				mustDeletion(t, os.WriteFile(filepath.Join(y.Path, module, ".git"), []byte("gitdir: ../.git/modules/sub\n"), 0600))
				deleteFixtureGit(t, filepath.Join(y.Path, module), "config", "core.worktree", "../../../sub")
				witness = admin + "/objects/pack/owned.pack"
				missing = module + "/.git"
				if mode == "module-admin" {
					missing = admin + "/config"
				}
			}
			// The ordinary files go first, then the exact metadata member above.
			mustDeletion(t, os.WriteFile(filepath.Join(y.Path, witness), []byte("remaining Git bytes"), 0600))
			check := func(context.Context, string) error { return nil }
			checks := LocalGitDeletionChecks{Idle: check}
			_, initial, err := s.PrepareForceDeletion(t.Context(), target, check, nil, checks)
			if initial == nil || err != nil {
				t.Fatal(initial, err)
			}
			stage := managedDeletingPrefix + target.Instance + "." + target.Entry
			stagePath := filepath.Join(s.root, stage)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fault := interruptDeletion{ctx, func() {
				if _, e := os.Lstat(stagePath); e == nil {
					if _, e := os.Lstat(filepath.Join(stagePath, missing)); errors.Is(e, fs.ErrNotExist) {
						cancel()
					}
				}
			}}
			detached, err := s.DeleteWithCheckoutConfirmation(fault, target, initial, check, func(Yesman) error { return nil }, nil)
			if !detached || !errors.Is(err, context.Canceled) {
				t.Fatal(detached, err)
			}
			mustDeletionBytes(t, filepath.Join(stagePath, witness), "remaining Git bytes")
			cold := New(s.root, s.archive)
			fresh, err := cold.Hire(y.Name)
			mustDeletion(t, err)
			mustDeletion(t, os.WriteFile(filepath.Join(fresh.Path, "fresh.txt"), []byte("replacement workspace"), 0600))
			rows, err := cold.StoredWorkspaces()
			if err != nil || len(rows) != 1 || rows[0].Entry != stage {
				t.Fatal(rows, err)
			}
			pending := rows[0]
			if _, err := cold.Delete(t.Context(), pending, check, nil); !errors.Is(err, errDeletionReceipt) {
				t.Fatalf("ordinary delete bypassed review: %v", err)
			}
			if _, err := cold.DeleteWithCheckoutConfirmation(t.Context(), pending, initial, check, nil, nil); !errors.Is(err, errCheckoutPreviewChanged) {
				t.Fatal(err)
			}
			checkReview := func() *ForceDeletion {
				t.Helper()
				scan, plan, err := cold.PrepareForceDeletion(t.Context(), pending, check, nil, checks)
				if err != nil || plan == nil || !slices.Contains(scan.LocalGitStores, gitDir) {
					t.Fatal(scan, plan, err)
				}
				return plan
			}
			assertHeld := func(diagnostic error) {
				t.Helper()
				_, plan, err := cold.PrepareForceDeletion(t.Context(), pending, check, nil, checks)
				if plan != nil || !errors.Is(err, diagnostic) {
					t.Fatal(plan, err)
				}
				mustDeletionBytes(t, filepath.Join(stagePath, witness), "remaining Git bytes")
			}
			record := filepath.Join(s.root, receiptName(stage))
			moved := filepath.Join(t.TempDir(), "receipt")
			mustDeletion(t, os.Rename(record, moved))
			assertHeld(errDeletionReceipt)
			raw, err := os.ReadFile(moved)
			mustDeletion(t, err)
			changed := slices.Clone(raw)
			changed[len(changed)-1] ^= 1
			mustDeletion(t, os.WriteFile(record, changed, 0600))
			assertHeld(errDeletionReceipt)
			mustDeletion(t, os.Remove(record))
			mustDeletion(t, os.Rename(moved, record))
			review := checkReview()
			checkCount := 0
			review.checks.Idle = func(context.Context, string) error {
				checkCount++
				if checkCount == 2 {
					return errors.New("late occupation")
				}
				return nil
			}
			if _, err := cold.DeleteWithCheckoutConfirmation(t.Context(), pending, review, check, nil, nil); err == nil || !strings.Contains(err.Error(), "late occupation") {
				t.Fatal(err)
			}
			mustDeletionBytes(t, filepath.Join(stagePath, witness), "remaining Git bytes")
			// The final pool/registration check must also occur after the
			// staged process probe returns, even on a cold retry.
			lateRegistration := checkReview()
			checkCount = 0
			registered := false
			lateRegistration.checks.Idle = func(context.Context, string) error {
				checkCount++
				registered = checkCount == 2
				return nil
			}
			registrationHold := errors.New("late shared-pool registration")
			checkRegistrations := func(context.Context, string) error {
				if registered {
					return registrationHold
				}
				return nil
			}
			if _, err := cold.DeleteWithCheckoutConfirmation(t.Context(), pending, lateRegistration, checkRegistrations, nil, nil); !errors.Is(err, registrationHold) {
				t.Fatal(err)
			}
			mustDeletionBytes(t, filepath.Join(stagePath, witness), "remaining Git bytes")
			// Admit a new supported external relationship at the last idle
			// probe, after the complete cold scope was already compared.
			// Test both rewriting and recreating a Git configuration.
			late := checkReview()
			checkCount = 0
			outsideCheckout := t.TempDir()
			outsideFile := filepath.Join(outsideCheckout, "keep.txt")
			mustDeletion(t, os.WriteFile(outsideFile, []byte("outside checkout"), 0600))
			admin := strings.TrimSuffix(witness, "/objects/pack/owned.pack")
			config := filepath.Join(stagePath, admin, "config")
			previous, priorErr := os.ReadFile(config)
			if priorErr != nil && !errors.Is(priorErr, fs.ErrNotExist) {
				t.Fatal(priorErr)
			}
			// Keep the original directory entry and time while exercising a
			// newly added supported control, so removing it restores the scope.
			database := filepath.Join(stagePath, admin, "objects", "info", "alternates")
			late.checks.Idle = func(context.Context, string) error {
				checkCount++
				if checkCount != 2 {
					return nil
				}
				if errors.Is(priorErr, fs.ErrNotExist) {
					return os.WriteFile(config, []byte("[core]\n\tworktree = "+filepath.ToSlash(outsideCheckout)+"\n"), 0600)
				}
				return os.WriteFile(database, []byte(filepath.ToSlash(outsideCheckout)+"\n"), 0600)
			}
			if _, err := cold.DeleteWithCheckoutConfirmation(t.Context(), pending, late, check, nil, nil); err == nil || !strings.Contains(err.Error(), "may be used by another checkout") {
				t.Fatal(err)
			}
			mustDeletionBytes(t, outsideFile, "outside checkout")
			mustDeletionBytes(t, filepath.Join(stagePath, witness), "remaining Git bytes")
			if errors.Is(priorErr, fs.ErrNotExist) {
				mustDeletion(t, os.Remove(config))
			} else {
				mustDeletionBytes(t, config, string(previous))
				mustDeletion(t, os.Remove(database))
			}
			// A surviving directory cannot be replaced by an empty same-name one,
			// even if its original markers have already been removed.
			held := filepath.Join(t.TempDir(), "pack")
			pack := filepath.Dir(filepath.Join(stagePath, witness))
			mustDeletion(t, os.Rename(pack, held))
			mustDeletion(t, os.Mkdir(pack, 0700))
			if _, plan, err := cold.PrepareForceDeletion(t.Context(), pending, check, nil, checks); plan != nil || !errors.Is(err, errCheckoutPreviewChanged) {
				t.Fatal(plan, err)
			}
			mustDeletion(t, os.Remove(pack))
			mustDeletion(t, os.Rename(held, pack))
			if detached, err := cold.DeleteWithCheckoutConfirmation(t.Context(), pending, checkReview(), check, nil, nil); detached || err != nil {
				t.Fatal(detached, err)
			}
			for _, p := range []string{stagePath, record} {
				if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("remaining %s: %v", p, err)
				}
			}
			mustDeletionBytes(t, filepath.Join(fresh.Path, "fresh.txt"), "replacement workspace")
		})
	}
}

// The opt-in fixture is deliberately just over the old cap. Empty files use
// modest disk space, and one fixture covers genuine interruption, late scope
// drift beyond the old cap and completing a cold retry.
func TestForceWorkspaceColdMillionEntries(t *testing.T) {
	mode := os.Getenv("YESMAN_TEST_LARGE_DELETION")
	if mode != "1" && mode != "local" {
		t.Skip("set YESMAN_TEST_LARGE_DELETION=1 or local to create a million-entry fixture")
	}
	s := waitStore(t)
	y, err := s.Get("alice")
	mustDeletion(t, err)
	start := time.Now()
	const groups = 936
	const entries = 1000
	const wideEntries = deletionDirectoryPage + 1
	const output = ".yesman/tmp/generated"
	for group := range groups {
		dir := filepath.Join(y.Path, output, fmt.Sprintf("%04d", group))
		mustDeletion(t, os.MkdirAll(dir, 0700))
		for i := range entries {
			mustDeletion(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d", i)), nil, 0600))
		}
	}
	wide := filepath.Join(y.Path, output, "zz-wide")
	mustDeletion(t, os.Mkdir(wide, 0700))
	for i := range wideEntries {
		mustDeletion(t, os.WriteFile(filepath.Join(wide, fmt.Sprintf("%05d", i)), nil, 0600))
	}
	t.Logf("created %d generated files including %d in one directory in %v", groups*entries+wideEntries, wideEntries, time.Since(start))
	// Give the large workspace an admitted checkout and a first ordinary file
	// whose real unlink deterministically interrupts before the generated tree.
	missing := "!first"
	prefix := deletingPrefix
	var localChecks []LocalGitDeletionChecks
	check := func(context.Context, string) error { return nil }
	if mode == "local" {
		// Interrupt within the bare store before reaching the generated tree.
		deleteFixtureGit(t, y.Path, "init", "--bare", "-q", "!repo.git")
		missing, prefix = "!repo.git/config", managedDeletingPrefix
		localChecks = []LocalGitDeletionChecks{{Idle: check}}
	} else {
		outside := filepath.Join(t.TempDir(), "outside.git")
		deleteFixtureGit(t, filepath.Dir(outside), "init", "--bare", "-q", outside)
		mustDeletion(t, writeExternalTestGitfile(filepath.Join(y.Path, "checkout"), outside))
	}
	mustDeletion(t, os.WriteFile(filepath.Join(y.Path, "!first"), nil, 0600))
	target, err := s.PrepareDeletion(y.Name, y.HiredAt)
	mustDeletion(t, err)
	_, initial, err := s.PrepareForceDeletion(t.Context(), target, check, nil, localChecks...)
	if err != nil || initial == nil {
		t.Fatal(initial, err)
	}
	stage := filepath.Join(s.root, prefix+target.Instance+"."+target.Entry)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	armed := false
	registrations := func(_ context.Context, workspace string) error {
		if workspace == stage {
			armed = true
		}
		return nil
	}
	fault := interruptDeletion{ctx, func() {
		// The last staged registration check follows the read-only walk.
		// Poll only during the final admission and purge, when an unlink
		// can actually happen, so the fixture stays within its budget.
		if armed {
			if _, err := os.Stat(filepath.Join(stage, missing)); errors.Is(err, fs.ErrNotExist) {
				cancel()
			}
		}
	}}
	if detached, err := s.DeleteWithCheckoutConfirmation(fault, target, initial, registrations, func(Yesman) error { return nil }, nil); !detached || !errors.Is(err, context.Canceled) {
		t.Fatal(detached, err)
	}
	cold := New(s.root, s.archive)
	rows, err := cold.StoredWorkspaces()
	if err != nil || len(rows) != 1 || !rows[0].Pending {
		t.Fatal(rows, err)
	}
	far := filepath.Join(stage, output, "zz-wide", fmt.Sprintf("%05d", wideEntries-1))
	mustDeletionBytes(t, far, "")
	if mode == "local" {
		if _, err := os.Stat(filepath.Join(stage, "!repo.git", "HEAD")); err != nil {
			t.Fatal(err)
		}
	}
	review := func() *ForceDeletion {
		t.Helper()
		started := time.Now()
		rctx, done := context.WithTimeout(t.Context(), 2*time.Minute)
		defer done()
		scan, plan, err := cold.PrepareForceDeletion(rctx, rows[0], check, nil, localChecks...)
		if err != nil || plan == nil || mode == "local" && !slices.Contains(scan.LocalGitStores, "!repo.git") {
			t.Fatal(scan, plan, err)
		}
		t.Logf("cold review took %v", time.Since(started))
		return plan
	}
	stale := review()
	original := filepath.Join(t.TempDir(), "original")
	mustDeletion(t, os.Rename(far, original))
	mustDeletion(t, os.WriteFile(far, []byte("late replacement"), 0600))
	if _, err := cold.DeleteWithCheckoutConfirmation(t.Context(), rows[0], stale, check, nil, nil); !errors.Is(err, errCheckoutPreviewChanged) {
		t.Fatal(err)
	}
	mustDeletionBytes(t, far, "late replacement")
	mustDeletion(t, os.Remove(far))
	mustDeletion(t, os.Rename(original, far))
	// The invalidated confirmation cannot be reused through the App bridge.
	// The Store receives only the newly reviewed scope for this retry.
	latest := review()
	started := time.Now()
	if detached, err := cold.DeleteWithCheckoutConfirmation(t.Context(), rows[0], latest, check, nil, nil); detached || err != nil {
		t.Fatal(detached, err)
	}
	t.Logf("cold commit took %v", time.Since(started))
	if _, err := os.Stat(stage); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}
