package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorktreePRAndPublicationEvidence(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	open := `[]`
	draft := true
	sha := strings.Repeat("c", 40)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/demo/pulls":
			q := r.URL.Query()
			if q.Get("head") != "acme:published/feature" || q.Get("state") != "open" || q.Get("per_page") != "1" {
				t.Errorf("lookup was not scoped to the published branch: %s", r.URL)
			}
			fmt.Fprint(w, open)
		case "/repos/acme/demo/releases/tags/v1":
			fmt.Fprintf(w, `{"tag_name":"v1","draft":%t,"published_at":"2026-01-15T12:00:00Z"}`, draft)
		case "/repos/acme/demo/commits/refs/tags/v1":
			fmt.Fprintf(w, `{"sha":%q}`, sha)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = old })
	for _, body := range []string{`[]`, `[{"number":8}]`, `null`} {
		open = body
		found, err := HasOpenPullFrom(context.Background(), "acme/demo", "acme", "published/feature")
		if found != strings.Contains(open, "number") || (err != nil) != (open == "null") {
			t.Fatalf("%s: open=%t error=%v", open, found, err)
		}
	}
	if _, err := ReadPublishedTag(context.Background(), "acme/demo", "v1"); err == nil {
		t.Fatal("certified a draft")
	}
	draft = false
	if got, err := ReadPublishedTag(context.Background(), "acme/demo", "v1"); err != nil || got != sha {
		t.Fatal(got, err)
	}
}
