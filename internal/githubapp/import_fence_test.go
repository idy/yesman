package githubapp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type importTransport func(*http.Request) (*http.Response, error)

func (f importTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type inspectedBody struct {
	io.ReadCloser
	closed chan struct{}
}

func (b inspectedBody) Close() error { close(b.closed); return b.ReadCloser.Close() }

func TestCanceledImportCannotCommitAfterInspection(t *testing.T) {
	m, app, key, _ := managerFixture(t, nil)
	configureManager(t, m, app, key)
	inspected := make(chan struct{})
	m.newClient = func(a Registration, key []byte, repos []string) (*Client, error) {
		c, err := ClientFromKey(a, key, repos)
		if err != nil {
			return nil, err
		}
		c.http = &http.Client{Transport: importTransport(func(r *http.Request) (*http.Response, error) {
			body := `{"id":123,"slug":"late-import","html_url":"https://github.com/apps/late-import","owner":{"login":"idy"},"permissions":{"contents":"read","pull_requests":"write","metadata":"read"}}`
			return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r, Body: inspectedBody{io.NopCloser(strings.NewReader(body)), inspected}}, nil
		})}
		return c, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.operations.Lock()
	done := make(chan error, 1)
	go func() { _, err := m.ImportKey(ctx, 123, key); done <- err }()
	<-inspected // successful response fully decoded, its body closed
	cancel()
	m.operations.Unlock()
	if err := <-done; err == nil {
		t.Fatal("canceled import committed after successful inspection")
	}
	if v := m.View(); v.App == nil || *v.App != app {
		t.Fatal("canceled import replaced the existing key")
	}
}

func TestImportedKeyCannotOverwriteSupersedingIdentity(t *testing.T) {
	for _, action := range []string{"registration", "newer-import", "scope", "lock-unlock"} {
		t.Run(action, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			var inspections atomic.Int32
			m, app, key, _ := managerFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/app" {
					connectionHandler(w, r)
					return
				}
				n := inspections.Add(1)
				if n == 1 {
					close(entered)
					<-release
				}
				slug := "old-import"
				if n > 1 {
					slug = "newer-import"
				}
				writeJSON(w, map[string]any{"id": 123, "slug": slug, "html_url": "https://github.com/apps/" + slug, "owner": map[string]string{"login": "idy"}, "permissions": map[string]string{"contents": "read", "pull_requests": "write", "metadata": "read"}})
			})
			defer once.Do(func() { close(release) })
			configureManager(t, m, app, key)
			done := make(chan error, 1)
			go func() { _, err := m.ImportKey(context.Background(), 123, key); done <- err }()
			<-entered
			switch action {
			case "registration":
				newer := app
				newer.Slug = "new-registration"
				newer.URL = "https://github.com/apps/" + newer.Slug
				if err := m.SaveRegistration(newer, key); err != nil {
					t.Fatal(err)
				}
			case "newer-import":
				if _, err := m.ImportKey(context.Background(), 123, key); err != nil {
					t.Fatal(err)
				}
			case "scope":
				if err := m.Configure(Config{Repositories: []string{"other/project"}}); err != nil {
					t.Fatal(err)
				}
			case "lock-unlock":
				m.Lock()
				if err := m.Prepare(""); err != nil {
					t.Fatal(err)
				}
			}
			before := m.View()
			once.Do(func() { close(release) })
			if err := <-done; err == nil {
				t.Error("superseded inspection committed an old key")
			}
			after := m.View()
			if after.App == nil || before.App == nil || *after.App != *before.App {
				t.Error("late import replaced current App")
			}
			if len(after.Config.Repositories) != len(before.Config.Repositories) || after.Config.Repositories[0] != before.Config.Repositories[0] {
				t.Error("late import changed saved scope")
			}
		})
	}
}
