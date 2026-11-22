package yesmen

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestForceWorkspaceRechecksAfterDrain(t *testing.T) {
	s := waitStore(t)
	y, err := s.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.PrepareDeletion(y.Name, y.HiredAt)
	if err != nil {
		t.Fatal(err)
	}
	makeMarker := func(name string) error {
		return os.MkdirAll(filepath.Join(y.MetaDir(), "tmp", name, ".git"), 0700)
	}
	if err := makeMarker("first"); err != nil {
		t.Fatal(err)
	}
	check := func(context.Context, string) error { return nil }
	_, plan, err := s.PrepareForceDeletion(t.Context(), want, check, nil)
	if err != nil || plan == nil {
		t.Fatal(plan, err)
	}
	detached, err := s.DeleteWithCheckoutConfirmation(t.Context(), want, plan, check, func(Yesman) error {
		return makeMarker("added-during-drain")
	}, nil)
	if detached || !errors.Is(err, errCheckoutPreviewChanged) {
		t.Fatalf("detached %v, error %v", detached, err)
	}
	for _, name := range []string{"first", "added-during-drain"} {
		if _, err := os.Stat(filepath.Join(y.MetaDir(), "tmp", name, ".git")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestForceWorkspaceInterruptedRetry(t *testing.T) {
	s := waitStore(t)
	y, err := s.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.PrepareDeletion(y.Name, y.HiredAt)
	if err != nil {
		t.Fatal(err)
	}
	// Earlier bytes disappear after staging, but this top-level checkout is
	// intact when the walk is interrupted, so the retry must name it again.
	checkout := filepath.Join(y.Path, "checkout")
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(y.Path, "A-first"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	check := func(context.Context, string) error { return nil }
	_, plan, err := s.PrepareForceDeletion(t.Context(), want, check, nil)
	if err != nil || plan == nil {
		t.Fatal(plan, err)
	}
	pending := filepath.Join(s.root, deletingPrefix+want.Instance+"."+want.Entry)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fault := interruptDeletion{ctx, func() {
		if _, err := os.Stat(pending); err == nil {
			if _, err := os.Stat(filepath.Join(pending, "A-first")); errors.Is(err, fs.ErrNotExist) {
				cancel()
			}
		}
	}}
	detached, err := s.DeleteWithCheckoutConfirmation(fault, want, plan, check, func(Yesman) error { return nil }, nil)
	if !detached || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "Stored workspaces") {
		t.Fatalf("detached %v, error %v", detached, err)
	}
	cold := New(s.root, s.archive)
	fresh, err := cold.Hire(y.Name)
	if err != nil {
		t.Fatal(err)
	}
	list, err := cold.StoredWorkspaces()
	if err != nil || len(list) != 1 || !list[0].Pending {
		t.Fatal(list, err)
	}
	if _, err := cold.DeleteWithCheckoutConfirmation(t.Context(), list[0], plan, check, nil, nil); err == nil {
		t.Fatal("live confirmation applied to interrupted bytes")
	}
	scan, retry, err := cold.PrepareForceDeletion(t.Context(), list[0], check, nil)
	if err != nil || retry == nil || len(scan.Entries) != 1 || scan.Entries[0].Path != "checkout" {
		t.Fatal(scan, retry, err)
	}
	if detached, err := cold.DeleteWithCheckoutConfirmation(t.Context(), list[0], retry, check, nil, nil); detached || err != nil {
		t.Fatal(detached, err)
	}
	if got, err := cold.Get(y.Name); err != nil || got != fresh {
		t.Fatal(got, err)
	}
}

func TestForceWorkspaceResumesMarkerlessStageWithFreshScope(t *testing.T) {
	s := waitStore(t)
	y, err := s.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.PrepareDeletion(y.Name, y.HiredAt)
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(y.Path, "build-output", "A-checkout")
	if err := os.MkdirAll(checkout, 0700); err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "-C", checkout, "init", "-q")
	git.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %q %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(y.Path, "build-output", "Z-cache"), []byte("still here"), 0600); err != nil {
		t.Fatal(err)
	}
	check := func(context.Context, string) error { return nil }
	_, plan, err := s.PrepareForceDeletion(t.Context(), target, check, nil)
	if err != nil || plan == nil {
		t.Fatal(plan, err)
	}
	pending := filepath.Join(s.root, deletingPrefix+target.Instance+"."+target.Entry)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fault := interruptDeletion{ctx, func() {
		if _, err := os.Stat(filepath.Join(pending, "build-output", "Z-cache")); err == nil {
			if _, err := os.Stat(filepath.Join(pending, "build-output", "A-checkout", ".git")); errors.Is(err, fs.ErrNotExist) {
				cancel()
			}
		}
	}}
	if detached, err := s.DeleteWithCheckoutConfirmation(fault, target, plan, check, func(Yesman) error { return nil }, nil); !detached || !errors.Is(err, context.Canceled) {
		t.Fatal(detached, err)
	}
	cold := New(s.root, s.archive)
	fresh, err := cold.Hire(y.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fresh.Path, "new-hire.txt"), []byte("new hire"), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := cold.StoredWorkspaces()
	if err != nil || len(rows) != 1 || !rows[0].Pending {
		t.Fatal(rows, err)
	}
	review := func() *ForceDeletion {
		t.Helper()
		scan, plan, err := cold.PrepareForceDeletion(t.Context(), rows[0], check, nil)
		if err != nil || plan == nil || len(scan.Entries) != 0 || !slices.Contains(scan.Paths, "build-output") {
			t.Fatal(scan, plan, err)
		}
		return plan
	}
	stale := review()
	late := filepath.Join(pending, "build-output", "new-after-review")
	if err := os.WriteFile(late, []byte("unapproved"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := cold.DeleteWithCheckoutConfirmation(t.Context(), rows[0], stale, check, nil, nil); !errors.Is(err, errCheckoutPreviewChanged) {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(late); err != nil || string(b) != "unapproved" {
		t.Fatal(string(b), err)
	}
	if b, err := os.ReadFile(filepath.Join(pending, "build-output", "Z-cache")); err != nil || string(b) != "still here" {
		t.Fatal(string(b), err)
	}
	if err := os.Remove(late); err != nil {
		t.Fatal(err)
	}
	if detached, err := cold.DeleteWithCheckoutConfirmation(t.Context(), rows[0], review(), check, nil, nil); detached || err != nil {
		t.Fatal(detached, err)
	}
	if _, err := os.Stat(pending); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(fresh.Path, "new-hire.txt")); err != nil || string(b) != "new hire" {
		t.Fatal(string(b), err)
	}
}

func TestCheckoutScanPartial(t *testing.T) {
	s := waitStore(t)
	y, err := s.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.PrepareDeletion(y.Name, y.HiredAt)
	if err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(y.MetaDir(), "tmp", "too-deep")
	for range checkoutDepthLimit {
		deep = filepath.Join(deep, "d")
	}
	for _, dir := range []string{deep, filepath.Join(y.MetaDir(), "tmp", "visible", ".git")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	scan, err := s.InspectCheckouts(t.Context(), want)
	if err != nil || scan.Problem == "" || len(scan.Entries) != 1 || scan.Entries[0].Path != ".yesman/tmp/visible" {
		t.Fatal(scan, err)
	}
}
