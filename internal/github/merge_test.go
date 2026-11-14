package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadPullMergeFreshAPIEvidence(t *testing.T) {
	// Disable the CLI so this test exercises the real REST fallback and its
	// exact source fields, without GitHub credentials or a network model.
	t.Setenv("PATH", t.TempDir())
	head, squash := strings.Repeat("1", 40), strings.Repeat("2", 40)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/repos/acme/demo/pulls/7" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprintf(w, `{"state":"closed","merged":true,"merged_at":"2026-01-02T00:00:00Z","merge_commit_sha":%q,"head":{"sha":%q,"ref":"feature","repo":{"full_name":"source/demo"}}}`, squash, head)
	}))
	defer srv.Close()
	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = old })
	for range 2 {
		pr, err := ReadPullMerge(context.Background(), "acme/demo", 7)
		if err != nil || !pr.Merged || pr.Head.SHA != head || pr.MergeCommit != squash || pr.Head.Ref != "feature" || pr.Head.Repo.FullName != "source/demo" || pr.MergedAt.IsZero() {
			t.Fatalf("merge evidence: %+v %v", pr, err)
		}
	}
	if calls != 2 {
		t.Fatalf("merge proof reused a stale display cache: %d calls", calls)
	}
}
