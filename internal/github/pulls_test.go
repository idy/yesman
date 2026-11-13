package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSumChecks(t *testing.T) {
	got := sumChecks([]rollupItem{
		{Status: "COMPLETED", Conclusion: "SUCCESS"},
		{Status: "COMPLETED", Conclusion: "SKIPPED"},
		{Status: "COMPLETED", Conclusion: "FAILURE"},
		{Status: "IN_PROGRESS"},
		{State: "SUCCESS"},
		{State: "PENDING"},
	})
	if *got != (Checks{Passed: 3, Failed: 1, Pending: 2, Total: 6}) {
		t.Errorf("checks %+v", got)
	}
	if sumChecks(nil) != nil {
		t.Error("no checks summed to some")
	}
}

func TestFindPullPrefersOpen(t *testing.T) {
	old := listPulls
	t.Cleanup(func() { listPulls = old })
	now := time.Now()
	listPulls = func(context.Context, string, string, string) ([]PullRequest, error) {
		return []PullRequest{
			{Number: 1, State: "merged", UpdatedAt: now},
			{Number: 2, State: "open", UpdatedAt: now.Add(-time.Hour)},
			{Number: 3, State: "closed", UpdatedAt: now.Add(time.Hour)},
		}, nil
	}
	pr, err := FindPull(context.Background(), "idy/yesman", "idy", "work")
	if err != nil || pr == nil || pr.Number != 2 {
		t.Errorf("FindPull = %+v, %v", pr, err)
	}
	listPulls = func(context.Context, string, string, string) ([]PullRequest, error) { return nil, nil }
	if pr, err := FindPull(context.Background(), "idy/yesman", "idy", "none"); pr != nil || err != nil {
		t.Errorf("FindPull without one = %+v, %v", pr, err)
	}
}

func TestAPIPulls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/idy/yesman/pulls":
			if r.URL.Query().Get("head") != "fork:work" {
				t.Errorf("head = %q", r.URL.Query().Get("head"))
			}
			w.Write([]byte(`[{"number":7,"title":"t","html_url":"https://github.com/idy/yesman/pull/7","state":"closed","merged_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}]`))
		case "/repos/idy/yesman/pulls/7":
			w.Write([]byte(`{"additions":5,"deletions":2,"changed_files":3}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = old })
	pulls, err := apiPulls(context.Background(), "idy/yesman", "fork", "work")
	if err != nil || len(pulls) != 1 {
		t.Fatalf("apiPulls = %+v, %v", pulls, err)
	}
	if p := pulls[0]; p.State != "merged" || p.Additions != 5 || p.Deletions != 2 || p.ChangedFiles != 3 || p.Checks != nil {
		t.Errorf("pull %+v", p)
	}
}
