package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
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
	fixtureLocalCheckout(t, bazel, bazelCheckout)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fixture", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string
			Name   string
			Since  time.Time
			Target yesmen.DeletionTarget
			Token  string
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
		case "CheckWorkspaceForceDeletion":
			value, err = s.CheckWorkspaceForceDeletion(r.Context(), req.Target)
		case "CancelWorkspaceForceDeletion":
			s.CancelWorkspaceForceDeletion(req.Token)
		case "ForceDeleteWorkspace":
			err = s.ForceDeleteWorkspace(r.Context(), req.Target, req.Token)
		case "AddLateCheckout":
			fixtureLocalCheckout(t, bazel, ".yesman/tmp/second-checkout")
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
