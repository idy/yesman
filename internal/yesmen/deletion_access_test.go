//go:build darwin || linux

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

func TestDeleteReadOnlyModuleDirectories(t *testing.T) {
	for _, mode := range []string{"active", "pending", "outside-cache", "nested-repository"} {
		t.Run(mode, func(t *testing.T) {
			s := waitStore(t)
			y, err := s.Get("alice")
			if err != nil {
				t.Fatal(err)
			}
			target, err := s.PrepareDeletion(y.Name, y.HiredAt)
			if err != nil {
				t.Fatal(err)
			}
			rel := filepath.Join(".yesman", "tmp", "_cache", "go-mod", "example.test", "kit@v1.0.0")
			if mode == "outside-cache" {
				rel = filepath.Join(".yesman", "evidence", "keep")
			}
			dir := filepath.Join(y.Path, rel)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			external := filepath.Join(t.TempDir(), "kept")
			if err := os.WriteFile(external, []byte("unchanged"), 0400); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(external, filepath.Join(dir, "go.mod")); err != nil {
				t.Fatal(err)
			}
			if mode == "nested-repository" {
				if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(dir, 0500); err != nil {
				t.Fatal(err)
			}
			staged := filepath.Join(s.root, deletingPrefix+target.Instance+"."+y.Name)
			t.Cleanup(func() { _ = os.Chmod(dir, 0700); _ = os.Chmod(filepath.Join(staged, rel), 0700) })
			if mode == "pending" {
				if err := os.Rename(y.Path, staged); err != nil {
					t.Fatal(err)
				}
				s = New(s.root, s.archive)
				list, err := s.StoredWorkspaces()
				if err != nil || len(list) != 1 || !list[0].Pending {
					t.Fatal(list, err)
				}
				target = list[0]
			}
			_, err = s.Delete(t.Context(), target, func(context.Context, string) error { return nil }, func(Yesman) error { return nil })
			if mode == "nested-repository" || mode == "outside-cache" && os.Geteuid() != 0 {
				if err == nil || !strings.Contains(err.Error(), filepath.Base(dir)) {
					t.Fatalf("expected retained %s, got %v", dir, err)
				}
				if st, e := os.Stat(dir); e != nil || st.Mode().Perm() != 0500 {
					t.Fatalf("preflight changed directory: %v %v", st, e)
				}
			} else if err != nil {
				t.Fatal(err)
			} else if _, err := os.Stat(staged); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
			if st, err := os.Stat(external); err != nil || st.Mode().Perm() != 0400 {
				t.Fatal("external file permissions", st, err)
			}
			if data, err := os.ReadFile(external); err != nil || string(data) != "unchanged" {
				t.Fatalf("external bytes %q %v", data, err)
			}
		})
	}
}
