package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/idy/yesman/internal/githubapp"
)

func TestGitHubAppScopeBesideInvalidMetadata(t *testing.T) {
	s, alice, bob, original := metadataServiceFixture(t)
	s.app.githubApp = githubapp.NewManager(filepath.Join(s.app.home.Root, "github-app"), nil, "/fixture/gh")
	defer s.app.githubApp.Close()
	path := filepath.Join(alice.MetaDir(), "yesman.json")
	oversize := append(append([]byte{}, original...), bytes.Repeat([]byte(" "), (1<<20)+1-len(original))...)
	if err := os.WriteFile(path, oversize, 0600); err != nil {
		t.Fatal(err)
	}
	v, err := s.ConfigureGitHubApp(githubapp.Config{Repositories: []string{"idy/yesman"}, Workspaces: []string{bob.Name}})
	if err != nil || len(v.Workspaces) != 1 || v.Workspaces[0].Name != bob.Name || v.Error == "" {
		t.Fatal("invalid neighbor prevented valid App scope or lost metadata diagnostic", err)
	}
	before, _ := json.Marshal(v.Config)
	if _, err := s.ConfigureGitHubApp(githubapp.Config{Repositories: []string{"idy/yesman"}, Workspaces: []string{alice.Name}}); err == nil {
		t.Fatal("unavailable workspace admitted")
	}
	after, _ := json.Marshal(s.app.githubApp.View().Config)
	if !bytes.Equal(before, after) {
		t.Fatal("failed scope replaced original generation")
	}
	state, err := s.State(context.Background())
	if err != nil || len(state.WorkspaceErrors) != 1 {
		t.Fatal("State partial diagnostics changed", err)
	}
	data, _ := json.Marshal([]any{v, state})
	if bytes.Contains(data, []byte("fixture-only-do-not-report")) {
		t.Fatal("metadata content leaked")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, oversize) {
		t.Fatal("oversized metadata was rewritten")
	}
}
