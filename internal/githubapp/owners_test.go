package githubapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDifferentOwnersWithSameRepositoryName(t *testing.T) {
	_, app, key := fixture(t)
	var mints atomic.Int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		owner := "idy"
		id := int64(456)
		if strings.Contains(r.URL.Path, "/other/") || strings.Contains(r.URL.Path, "/789/") {
			owner = "other"
			id = 789
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation"):
			value := installation()
			value["id"] = id
			value["account"] = map[string]string{"login": owner}
			writeJSON(w, value)
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			mints.Add(1)
			value := tokenJSON(fixtureToken+"_"+owner, time.Now().Add(time.Hour))
			value["repositories"] = []map[string]string{{"full_name": owner + "/yesman"}}
			writeJSON(w, value)
		default:
			if r.Header.Get("Authorization") != "Bearer "+fixtureToken+"_"+owner {
				t.Error("token crossed installation owners")
			}
			writeJSON(w, map[string]string{"full_name": owner + "/yesman"})
		}
	}
	upstream := httptest.NewServer(http.HandlerFunc(handler))
	defer upstream.Close()
	c, err := ClientFromKey(app, key, []string{"idy/yesman", "other/yesman"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.base = upstream.URL
	var group sync.WaitGroup
	for _, repo := range []string{"idy/yesman", "other/yesman"} {
		group.Go(func() {
			result, err := c.Test(context.Background(), repo)
			if err != nil {
				t.Error(err)
				return
			}
			want := int64(456)
			if strings.HasPrefix(repo, "other/") {
				want = 789
			}
			if result.InstallationID != want {
				t.Error("wrong installation metadata")
			}
		})
	}
	group.Wait()
	if _, err := c.Test(context.Background(), "other/yesman"); err != nil {
		t.Fatal(err)
	}
	if mints.Load() != 2 {
		t.Fatalf("installation mints: %d", mints.Load())
	}
}
