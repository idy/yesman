package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/server"
	"github.com/idy/yesman/internal/yesmen"
)

func TestNativeWorktreeMenuItems(t *testing.T) {
	item := func(id, label string, _ ...func(*mygo.MenuItem)) *mygo.MenuItem {
		return &mygo.MenuItem{ID: id, Label: label}
	}
	header, row := worktreeMenuItems(false, item), worktreeMenuItems(true, item)
	if len(header) != 1 || header[0].ID != "cleanup" || header[0].Label != "Clean up worktrees…" {
		t.Fatalf("heading menu: %+v", header)
	}
	if len(row) != 3 || row[0].ID != "reveal" || row[0].Label != revealLabel() || row[1].Type != mygo.MenuItemSeparator || row[2].ID != "remove" || row[2].Label != "Remove Worktree…" {
		t.Fatalf("row menu: %+v", row)
	}
	for _, label := range []string{header[0].Label, row[0].Label, row[2].Label} {
		if trIn("zh-Hans", label) == label {
			t.Fatalf("untranslated menu item %q", label)
		}
	}
}

// The browser uses the real embedded page, desktop service, Store and Git.
// Only the native menu selection and unrelated services use stand-ins.
func TestWorktreeCleanupBrowser(t *testing.T) {
	if os.Getenv("YESMAN_TEST_WORKTREE_BROWSER") != "1" {
		t.Skip("run through scripts/worktree-cleanup-browser.mjs")
	}
	s := hireService(t)
	a := s.app
	a.desktopWorktrees.Server = &server.Server{Pool: a.pool, Yesmen: a.yesmen, Agents: a.agents,
		HasOpenPull: func(context.Context, string, string, string) (bool, error) { return false, nil }}
	y, err := a.yesmen.Hire("studio")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	origin := t.TempDir()
	git := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	git(origin, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, "README"), []byte("Fixture\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git(origin, "add", "README")
	git(origin, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "Fixture")
	git(origin, "switch", "-qc", "replacement")
	git(origin, "rm", "-q", "README")
	if err := os.WriteFile(filepath.Join(origin, "replacement.txt"), []byte("New checkout\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git(origin, "add", "replacement.txt")
	git(origin, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "Replacement fixture")
	git(origin, "switch", "-q", "main")
	repo, err := a.pool.Add(t.Context(), origin, "")
	if err != nil {
		t.Fatal(err)
	}
	var records []yesmen.WorktreeRecord
	str := func(v string) *string { return &v }
	var gizmoTask string
	for _, dir := range []string{"gizmoth-artifact-rows", "review-a", "review-b", "locked", "legacy", "saved-conflict", "unverified-index"} {
		since := time.Now().UTC()
		if err := a.yesmen.AddWorker(y.Name, yesmen.Worker{Name: dir, StartedAt: since}); err != nil {
			t.Fatal(err)
		}
		b, err := a.yesmen.ChangeBoard(y.Name, yesmen.BoardChange{Add: []yesmen.TaskEdit{{Title: str(dir), Owner: str(dir), Status: str(yesmen.TaskDropped)}}})
		if err != nil {
			t.Fatal(err)
		}
		opts := pool.WorktreeOptions{Dest: filepath.Join(y.Path, dir), Ref: "origin/main"}
		if dir == "gizmoth-artifact-rows" {
			opts.Ref, opts.Branch = "", "fix/artifact-row-actions"
		}
		w, err := a.pool.AddWorktree(t.Context(), repo, opts)
		if err != nil {
			t.Fatal(err)
		}
		if dir == "legacy" {
			continue
		}
		r := yesmen.WorktreeRecord{ID: w.CleanupID, Dir: w.Dir, Repo: w.Repo, Owner: dir, OwnerSince: since, Purpose: "review", Task: b.Tasks[len(b.Tasks)-1].ID, Branch: w.Branch, Head: w.Head}
		if w.Detached {
			r.Completion = &yesmen.WorktreeCompletion{Head: w.Head, Ref: w.Head, Outcome: "pass", Unused: true, Evidence: "Fixture review on board", SavedAt: since}
		} else {
			r.Purpose = "author"
			gizmoTask = r.Task
		}
		switch dir {
		case "saved-conflict":
			git(w.Path, "switch", "-qc", "fixture-conflict")
			readme := filepath.Join(w.Path, "README")
			if err := os.WriteFile(readme, []byte("Other version\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git(w.Path, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qam", "Other version")
			if err := os.WriteFile(readme, []byte("Local version\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git(w.Path, "switch", "--merge", "--detach", w.Head)
			git(w.Path, "checkout", "--ours", "--", "README")
			git(w.Path, "add", "README")
		case "unverified-index":
			private, err := exec.Command("git", "-C", w.Path, "rev-parse", "--absolute-git-dir").Output()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(strings.TrimSpace(string(private)), "index"), []byte("partial"), 0600); err != nil {
				t.Fatal(err)
			}
		case "locked":
			git(repo.Path, "worktree", "lock", w.Path)
		}
		records = append(records, r)
	}
	if err := a.yesmen.SaveWorktreeRecords(y.Name, records); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fixture", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "http://"+r.Host || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "origin", 403)
			return
		}
		var req struct {
			Method string
			Args   []json.RawMessage
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req) != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		var value any
		var err error
		switch req.Method {
		case "Recreate", "ReplaceAgain":
			dest := filepath.Join(y.Path, "gizmoth-artifact-rows")
			if req.Method == "ReplaceAgain" {
				err = a.pool.RemoveWorktree(r.Context(), dest, false)
				if err != nil {
					break
				}
			}
			value, err = a.pool.AddWorktree(r.Context(), repo, pool.WorktreeOptions{Dest: dest, Ref: "origin/replacement"})
		case "KeepGizmo":
			keep := []yesmen.WorktreeReturn{}
			_, err = a.yesmen.ChangeBoard(y.Name, yesmen.BoardChange{Update: []yesmen.TaskEdit{{ID: gizmoTask, Status: str(yesmen.TaskDone), Worktrees: &keep}}})
		case "FaultAfterGit":
			// After success Git removes the checkout and its private directory.
			// A reference hook injects an independent registry failure into this fixture.
			err = os.Remove(filepath.Join(y.Path, "review-b", "keep"))
			if err == nil {
				err = installFixtureRegistryFault(repo.Path, y.MetaDir())
			}
		case "DirtyReview":
			err = os.WriteFile(filepath.Join(y.Path, "review-b", "keep"), []byte("Still in use"), 0600)
		case "Verify":
			wts, e := a.pool.Worktrees(r.Context(), y.Path)
			var refs []string
			for _, rec := range records {
				if out, e := exec.Command("git", "-C", repo.Path, "rev-parse", "--verify", "refs/yesman/worktrees/"+rec.ID).Output(); e == nil && string(out) == rec.Head+"\n" {
					refs = append(refs, rec.Dir)
				}
			}
			value, err = map[string]any{"trees": wts, "checkpoints": refs}, e
		default:
			if !slices.Contains([]string{"State", "Info", "Usage", "ListFiles", "ReadFile", "ReadWorktreeFile", "WriteWorktreeFile", "PreviewWorktreeCleanup", "ConfirmWorktreeCleanup", "CancelWorktreeCleanup"}, req.Method) {
				http.Error(w, "method", 400)
				return
			}
			m := reflect.ValueOf(s).MethodByName(req.Method)
			var in []reflect.Value
			if m.Type().In(0) == reflect.TypeFor[context.Context]() {
				in = append(in, reflect.ValueOf(r.Context()))
			}
			if len(req.Args)+len(in) != m.Type().NumIn() {
				http.Error(w, "argument count", 400)
				return
			}
			for _, raw := range req.Args {
				v := reflect.New(m.Type().In(len(in)))
				if json.Unmarshal(raw, v.Interface()) != nil {
					http.Error(w, "argument", 400)
					return
				}
				in = append(in, v.Elem())
			}
			out := m.Call(in)
			if len(out) > 0 {
				value = out[0].Interface()
			}
			if len(out) == 2 && !out[1].IsNil() {
				err = out[1].Interface().(error)
			}
		}
		result := map[string]any{"value": value}
		if err != nil {
			result["error"] = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
	web, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", http.FileServerFS(web))
	fixture := httptest.NewServer(mux)
	defer fixture.Close()
	fmt.Println("WORKTREE_BROWSER_URL=" + fixture.URL)
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// Redirecting this synthetic registry only when recovery refs commit lets the
// browser exercise a successful Git removal followed by a bookkeeping error.
func installFixtureRegistryFault(repo, meta string) error {
	cmd := exec.Command("git", "-C", repo, "config", "fixture.registry", filepath.ToSlash(filepath.Join(meta, "worktrees.json")))
	if err := cmd.Run(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(repo, "hooks", "reference-transaction"), []byte(`#!/bin/sh
if test "$1" = committed && grep -q ' refs/yesman/worktrees/'; then
  mv "$(git config --get fixture.registry)" "$(git config --get fixture.registry).saved"
fi
`), 0700)
}
