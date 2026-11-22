package yesmen

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
	scan, plan, err := s.PrepareForceDeletion(t.Context(), want, func(context.Context, string) error { return nil }, nil)
	if err != nil || plan != nil || scan.Problem == "" || len(scan.Entries) != 1 || scan.Entries[0].Path != ".yesman/tmp/visible" {
		t.Fatal(scan, plan, err)
	}
}
