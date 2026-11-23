package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/idy/yesman/internal/safefs/linktest"
	"github.com/idy/yesman/internal/yesmen"
)

// The desktop-only operations below run against the real Store, scheduler
// boundary and agent manager. No pi, user configuration or provider is used.
func TestDeletionBrowserBridge(t *testing.T) {
	if os.Getenv("YESMAN_TEST_DELETE_BROWSER") != "1" {
		t.Skip("run with scripts/delete-workspace-browser.mjs")
	}
	i18n.SetLocaleFunc(func() string { return os.Getenv("YESMAN_TEST_DELETE_LOCALE") })
	s, y, _ := deletionFixture(t)
	for _, stamp := range []string{"20260101-123456", "20260102-123456"} {
		p, err := s.Dismiss(y.Name)
		must(t, err)
		must(t, os.Rename(p, filepath.Join(s.app.home.Archive(), y.Name+"-"+stamp)))
		y, err = s.HireNamed(t.Context(), "Paper Kites")
		must(t, err)
	}
	other, err := s.HireNamed(t.Context(), "Retained")
	must(t, err)
	must(t, os.Mkdir(filepath.Join(other.Path, "unfinished-checkout"), 0700))
	bazel, err := s.HireNamed(t.Context(), "Bazel Builds")
	must(t, err)
	bazelRoot := fixtureLocalCheckout(t, bazel, bazelCheckout)
	fixtureExternalGitfile(t, bazel, "old-root-checkout")
	must(t, os.MkdirAll(filepath.Join(bazel.Path, filepath.FromSlash(deepBuildPath())), 0700))
	var pendingPath, freshPath, externalPath, externalHead string
	var bazelGit, bazelHead string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fixture", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string
			Name   string
			Since  time.Time
			Target yesmen.DeletionTarget
			Token  string
			Review string
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad fixture request", 400)
			return
		}
		var value any
		var err error
		switch req.Method {
		case "State":
			value, err = s.State(r.Context())
		case "Crew":
			value = s.Crew(r.Context())
		case "HireNamed":
			value, err = s.HireNamed(r.Context(), req.Name)
		case "PrepareWorkspaceDeletion":
			value, err = s.PrepareWorkspaceDeletion(req.Name, req.Since)
		case "Info":
			value, err = s.Info(r.Context(), req.Name)
		case "WorkspaceCheckouts":
			value, err = s.WorkspaceCheckouts(r.Context(), req.Name, req.Since)
		case "ReviewWorkspaceForceDeletion":
			value, err = s.ReviewWorkspaceForceDeletion(r.Context(), req.Target, req.Review)
		case "CancelWorkspaceForceReview":
			s.CancelWorkspaceForceReview(req.Review)
		case "CancelWorkspaceForceDeletion":
			s.CancelWorkspaceForceDeletion(req.Token)
		case "ForceDeleteWorkspace":
			err = s.ForceDeleteWorkspace(r.Context(), req.Target, req.Token)
		case "PreserveBazelGitData":
			separateFixtureGitData(t, bazelRoot)
			bazelGit = fixtureGit(t, bazelRoot, "rev-parse", "--absolute-git-dir")
			bazelHead = fixtureGit(t, bazelRoot, "rev-parse", "HEAD")
			marker := filepath.Join(bazelRoot, ".git")
			must(t, os.Remove(marker))
			linktest.Symlink(t, filepath.Join(bazel.Path, "gizos-display-pr", ".git"), marker)
		case "VerifyBazelRemoval":
			if _, e := os.Lstat(bazel.Path); !errors.Is(e, fs.ErrNotExist) {
				t.Error("Bazel workspace remains", e)
			}
			if fixtureGit(t, bazelGit, "rev-parse", "HEAD") != bazelHead {
				t.Error("relocated Bazel Git data changed")
			}
		case "AddLateCheckout":
			fixtureExternalGitfile(t, bazel, deepBuildPath()+"/second-checkout")
		case "RootCheckout":
			root, e := s.HireNamed(r.Context(), "Root Project")
			must(t, e)
			initFixtureCheckout(t, root.Path)
			must(t, os.Mkdir(filepath.Join(root.Path, "src"), 0700))
			must(t, os.WriteFile(filepath.Join(root.Path, "src", "main.txt"), []byte("source"), 0600))
			fixtureGit(t, root.Path, "add", "src")
			externalPath = filepath.Join(t.TempDir(), "kept-linked")
			fixtureGit(t, root.Path, "worktree", "add", "-b", "linked", externalPath)
			externalHead = fixtureGit(t, externalPath, "rev-parse", "HEAD")
		case "PreserveLinked":
			root, e := s.app.yesmen.Get("root-project")
			must(t, e)
			outsideRepo := filepath.Join(t.TempDir(), "kept-repository")
			// The fixture manually moves the repository with its linked Git
			// peer, then lets Git repair the two pointers.
			must(t, os.Mkdir(outsideRepo, 0700))
			must(t, os.Rename(filepath.Join(root.Path, ".git"), filepath.Join(outsideRepo, ".git")))
			fixtureGit(t, outsideRepo, "worktree", "repair", externalPath)
			if fixtureGit(t, externalPath, "rev-parse", "HEAD") != externalHead {
				t.Error("dependent HEAD changed")
			}
			must(t, os.WriteFile(filepath.Join(root.Path, ".git"), []byte("gitdir: "+filepath.Join(outsideRepo, ".git")+"\n"), 0600))
		case "MakeInterruptedWorkspace":
			pendingPath, freshPath = makeInterruptedBrowserWorkspace(t, s)
		case "AddLateStagedFile":
			err = os.WriteFile(filepath.Join(pendingPath, "build-output", "late.txt"), []byte("unapproved"), 0600)
		case "RemoveLateStagedFile":
			late := filepath.Join(pendingPath, "build-output", "late.txt")
			if b := mustReadFile(t, late); string(b) != "unapproved" {
				t.Error("unapproved data changed")
			}
			err = os.Remove(late)
		case "VerifyRecovery":
			if _, err := os.Lstat(pendingPath); !errors.Is(err, fs.ErrNotExist) {
				t.Error("pending workspace remains", err)
			}
			if b := mustReadFile(t, filepath.Join(freshPath, "keep.txt")); string(b) != "fresh workspace" {
				t.Error("new hire changed")
			}
			if fixtureGit(t, externalPath, "rev-parse", "HEAD") != externalHead {
				t.Error("retained checkout changed")
			}
		case "StoredWorkspaces":
			value, err = s.StoredWorkspaces()
		case "DeleteWorkspace":
			err = s.DeleteWorkspace(r.Context(), req.Target)
		case "MoveRetainedFolder": // the user preserves their synthetic loose folder outside the workspace
			err = os.Rename(filepath.Join(other.Path, "unfinished-checkout"), filepath.Join(t.TempDir(), "kept"))
		case "Rehire": // other window, while a confirmation is showing
			old, e := s.PrepareWorkspaceDeletion(y.Name, y.HiredAt)
			err = e
			if err == nil {
				err = s.DeleteWorkspace(r.Context(), old)
			}
			if err == nil {
				value, err = s.HireNamed(r.Context(), "Paper Kites")
			}
		default:
			http.Error(w, "unknown fixture method", 400)
			return
		}
		body := map[string]any{"value": value}
		if err != nil {
			body["error"] = err.Error()
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	web, err := fs.Sub(webFS, "web")
	must(t, err)
	mux.Handle("/", http.FileServerFS(web))
	server := httptest.NewServer(mux)
	defer server.Close()
	fmt.Println("DELETE_BROWSER_URL=" + server.URL)
	_, _ = io.Copy(io.Discard, os.Stdin)
}

type browserDeletionFault struct {
	context.Context
	check func()
}

func (f browserDeletionFault) Err() error {
	f.check()
	return f.Context.Err()
}

func makeInterruptedBrowserWorkspace(t *testing.T, s *service) (string, string) {
	t.Helper()
	y, err := s.HireNamed(t.Context(), "Interrupted Build")
	must(t, err)
	fixtureExternalGitfile(t, y, "build-output/A-checkout")
	cache := filepath.Join("build-output", "Z-cache")
	must(t, os.WriteFile(filepath.Join(y.Path, cache), []byte("staged build bytes"), 0600))
	target, err := s.PrepareWorkspaceDeletion(y.Name, y.HiredAt)
	must(t, err)
	plan, err := s.CheckWorkspaceForceDeletion(t.Context(), target)
	must(t, err)
	if plan.Token == "" {
		t.Fatal(plan)
	}
	pending := filepath.Join(s.app.home.Yesmen(), ".deleting-"+target.Instance+"."+target.Entry)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fault := browserDeletionFault{ctx, func() {
		if _, err := os.Stat(filepath.Join(pending, cache)); err == nil {
			if _, err := os.Stat(filepath.Join(pending, "build-output", "A-checkout", ".git")); errors.Is(err, fs.ErrNotExist) {
				cancel()
			}
		}
	}}
	if err := s.ForceDeleteWorkspace(fault, target, plan.Token); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatal("interruption did not leave a verified stage", err)
	}
	mustReadFile(t, filepath.Join(pending, cache))
	fresh, err := s.HireNamed(t.Context(), "Interrupted Build")
	must(t, err)
	must(t, os.WriteFile(filepath.Join(fresh.Path, "keep.txt"), []byte("fresh workspace"), 0600))
	return pending, fresh.Path
}
