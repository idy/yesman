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
	// Revalidate after decoding, then bind token mint/gh execution to this
	// actor's retirement fence. Archive/worker removal cancels and drains the
	// operation without holding the workspace lock during network or gh I/O.
	ctx, finish, err := s.Agents.BeginOperation(ctx, c.credential, c.agent, c.yesman.HiredAt)
	if err != nil {
		return nil, err
	}
	defer finish()
	return s.GitHubApp.Create(ctx, c.yesman.Name, c.yesman.HiredAt.UTC().Format(time.RFC3339Nano), in)
}
