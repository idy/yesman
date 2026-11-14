package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/idy/yesman/internal/githubapp"
)

func (s *Server) githubAppMode(_ context.Context, c caller, _ *http.Request) (any, error) {
	mode := "personal"
	if s.GitHubApp != nil {
		var err error
		mode, err = s.GitHubApp.Mode(c.yesman.Name, c.yesman.HiredAt.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
	}
	return map[string]string{"mode": mode}, nil
}
func (s *Server) githubAppPull(ctx context.Context, c caller, r *http.Request) (any, error) {
	if s.GitHubApp == nil {
		return nil, errors.New("GitHub App PR creation is unavailable")
	}
	var in githubapp.PullInput
	decoder := json.NewDecoder(io.LimitReader(r.Body, 70001))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return nil, errors.New("invalid GitHub App PR request")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("invalid GitHub App PR request")
	}
	// Workspace/actor come from Authenticate, never request fields or cwd.
	return s.GitHubApp.Create(ctx, c.yesman.Name, c.yesman.HiredAt.UTC().Format(time.RFC3339Nano), in)
}
