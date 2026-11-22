package yesmen

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForceWorkspaceHoldsStagedGitData(t *testing.T) {
	for _, mode := range []string{"linked-common", "prior-git-receipt"} {
		t.Run(mode, func(t *testing.T) {
			s := waitStore(t)
			y, err := s.Get("alice")
			if err != nil {
				t.Fatal(err)
			}
			want, err := s.PrepareDeletion(y.Name, y.HiredAt)
			if err != nil {
				t.Fatal(err)
			}
			stage := deletingPrefix + want.Instance + "." + want.Entry
			rel := filepath.Join(".yesman", "tmp", "Z-common", ".git", "worktrees", "A-linked", "gitdir")
			contents := []byte(filepath.Join(y.MetaDir(), "tmp", "A-linked", ".git") + "\n")
			diagnostic := filepath.Dir(filepath.Dir(filepath.Dir(rel)))
			if mode == "prior-git-receipt" {
				// A prior purge could already have removed every Git marker.
				// The old record alone must preserve that stage for recovery.
				rel = stagedGitReceipt
				diagnostic = rel
				contents = []byte(`{"stage":"` + stage + `","registrations":[]}`)
			}
			file := filepath.Join(y.Path, rel)
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, contents, 0600); err != nil {
				t.Fatal(err)
			}
			// Model a stage left by an older deletion implementation. R4
			// would refuse to detach an active workspace in either case.
			stagePath := filepath.Join(s.root, stage)
			if err := os.Rename(y.Path, stagePath); err != nil {
				t.Fatal(err)
			}
			cold := New(s.root, s.archive)
			fresh, err := cold.Hire(y.Name)
			if err != nil {
				t.Fatal(err)
			}
			freshFile := filepath.Join(fresh.Path, "keep.txt")
			if err := os.WriteFile(freshFile, []byte("replacement workspace"), 0600); err != nil {
				t.Fatal(err)
			}
			rows, err := cold.StoredWorkspaces()
			if err != nil || len(rows) != 1 || !rows[0].Pending {
				t.Fatal(rows, err)
			}
			check := func(context.Context, string) error { return nil }
			if _, plan, err := cold.PrepareForceDeletion(t.Context(), rows[0], check, nil); plan != nil || err == nil || !strings.Contains(err.Error(), filepath.Join(stagePath, diagnostic)) {
				t.Fatalf("cold Storage admitted unresolved Git data: %v %v", plan, err)
			}
			if detached, err := cold.Delete(t.Context(), rows[0], check, nil); detached || err == nil || !strings.Contains(err.Error(), filepath.Join(stagePath, diagnostic)) {
				t.Fatalf("ordinary retry admitted unresolved Git data: %t %v", detached, err)
			}
			if b, err := os.ReadFile(filepath.Join(stagePath, rel)); err != nil || !bytes.Equal(b, contents) {
				t.Fatalf("Git recovery bytes changed: %q %v", b, err)
			}
			if b, err := os.ReadFile(freshFile); err != nil || string(b) != "replacement workspace" {
				t.Fatalf("new hire changed: %q %v", b, err)
			}
		})
	}
}
