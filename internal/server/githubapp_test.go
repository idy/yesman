package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/githubapp"
	"github.com/idy/yesman/internal/yesmen"
)

func TestGitHubAppEndpointsUseAuthenticatedWorkspace(t *testing.T) {
	root := t.TempDir()
	store := yesmen.New(filepath.Join(root, "yesmen"), filepath.Join(root, "archive"))
	y, err := store.Hire("alice")
	if err != nil {
		t.Fatal(err)
	}
	app := githubapp.NewManager(filepath.Join(root, "github-app"), nil, "/installed/gh")
	defer app.Close()
	manager := agent.New(agent.Config{Yesmen: store})
	defer manager.StopAll()
	s := &Server{Yesmen: store, Agents: manager, GitHubApp: app}
	for _, method := range []string{"GET", "POST"} {
		path := "/v1/github-app/mode"
		if method == "POST" {
			path = "/v1/github-app/pulls"
		}
		request := httptest.NewRequest(method, path, bytes.NewReader([]byte(`{}`)))
		request.Header.Set("Authorization", "Bearer forged")
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != 401 {
			t.Fatalf("unauthenticated endpoint %s: %d", path, response.Code)
		}
	}
	for _, role := range []string{agent.RoleLead, agent.RoleWorker} {
		caller := caller{yesman: y, agent: agent.Info{Yesman: "alice", Role: role}}
		mode, err := s.githubAppMode(context.Background(), caller, nil)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(mode)
		if string(data) != `{"mode":"personal"}` {
			t.Fatalf("default mode: %s", data)
		}
		// Caller identity is not an input. Nested workers and leads use the same
		// server-established workspace, and request fields cannot override it.
		request := httptest.NewRequest("POST", "/v1/github-app/pulls", bytes.NewReader([]byte(`{"workspace":"bob","repository":"idy/yesman"}`)))
		if _, err := s.githubAppPull(context.Background(), caller, request); err == nil {
			t.Fatal("accepted client-supplied workspace")
		}
	}
	env := s.Env()
	description := s.Describe()
	if len(env.Env) != 0 || len(env.Hide) != 0 || len(description.Vars) != 0 {
		t.Fatal("App broker leaked credentials into the environment endpoint")
	}
}
