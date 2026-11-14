package githubapp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const githubAPI = "https://api.github.com"

// RegistrationServer handles one short-lived GitHub manifest exchange. Neither
// the callback URL nor GitHub's response is logged or rendered to the browser.
type RegistrationServer struct {
	Owner, Name, Dir      string
	client                *http.Client
	base                  string
	mu                    sync.Mutex
	state, host, callback string
	expires               time.Time
	consumed              bool
	result                chan RegistrationResult
}

type RegistrationResult struct {
	App Registration
	Err error
}

func NewRegistrationServer(owner, name, dir string) *RegistrationServer {
	return &RegistrationServer{Owner: owner, Name: name, Dir: dir, base: githubAPI,
		client: safeHTTPClient(), result: make(chan RegistrationResult, 1)}
}

func safeHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Start binds exclusively to loopback. Stop must be called by the owner. The
// caller prints only the entry URL, which contains no code or state value.
func (s *RegistrationServer) Start(ctx context.Context) (entry string, stop func(), err error) {
	if !ownerPattern.MatchString(s.Owner) || strings.TrimSpace(s.Name) == "" {
		return "", nil, errors.New("a GitHub owner and App name are required")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		ln.Close()
		return "", nil, errors.New("create registration state failed")
	}
	s.state = base64.RawURLEncoding.EncodeToString(nonce)
	s.host = ln.Addr().String()
	s.callback = "http://" + s.host + "/callback"
	s.expires = time.Now().Add(30 * time.Minute)
	server := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 16384}
	go func() { _ = server.Serve(ln) }()
	stop = func() { _ = server.Close() }
	go func() {
		select {
		case <-ctx.Done():
			stop()
		case <-time.After(30 * time.Minute):
			stop()
		}
	}()
	return "http://" + s.host + "/", stop, nil
}

func (s *RegistrationServer) Results() <-chan RegistrationResult { return s.result }

func (s *RegistrationServer) manifest() map[string]any {
	return map[string]any{"name": s.Name, "url": "https://github.com/idy/yesman", "description": "Create pull requests from yesman agents using a dedicated GitHub App identity.",
		"redirect_url": s.callback, "public": false, "hook_attributes": map[string]any{"active": false},
		"default_permissions": map[string]string{"contents": "read", "pull_requests": "write"},
		"default_events":      []string{}, "request_oauth_on_install": false}
}

var registerPage = template.Must(template.New("register").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><title>Register your yesman GitHub App</title><h1>Register your yesman GitHub App</h1><p>Sign in to GitHub as <strong>{{.Owner}}</strong>. The App belongs to your personal account and is private.</p><p>Permissions: pull requests (read and write), contents (read only), and GitHub's default metadata read. Webhooks and user OAuth are disabled.</p><p>After creating it, install on <strong>Only select repositories → idy/yesman</strong>. This page saves the private key directly to a private directory on this computer; do not paste it into chat or Settings → Secrets.</p><form method="post" action="https://github.com/settings/apps/new?state={{.State}}"><input type="hidden" name="manifest" value="{{.Manifest}}"><button type="submit">Continue to GitHub</button></form></html>`))

func (s *RegistrationServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action https://github.com; frame-ancestors 'none'; base-uri 'none'")
	if r.Host != s.host || r.Method != http.MethodGet || len(r.URL.RawQuery) > 2048 {
		http.Error(w, "Invalid registration request", http.StatusBadRequest)
		return
	}
	if time.Now().After(s.expires) {
		http.Error(w, "Registration expired. Restart setup.", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	switch r.URL.Path {
	case "/":
		s.mu.Lock()
		consumed := s.consumed
		s.mu.Unlock()
		if consumed {
			http.Error(w, "Registration already handled. Follow the installation link from the setup command.", http.StatusConflict)
			return
		}
		if r.URL.RawQuery != "" {
			http.Error(w, "Invalid registration request", 400)
			return
		}
		// Cross-site navigations may open the entry page; only same-origin
		// scripts could read the unguessable state from its form.
		data, _ := json.Marshal(s.manifest())
		_ = registerPage.Execute(w, map[string]string{"Owner": s.Owner, "State": s.state, "Manifest": string(data)})
	case "/callback":
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q["state"]) != 1 || len(q["code"]) != 1 || q.Get("state") != s.state || !codePattern.MatchString(q.Get("code")) {
			http.Error(w, "Invalid registration callback", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		if s.consumed {
			s.mu.Unlock()
			http.Error(w, "Registration was already handled", http.StatusConflict)
			return
		}
		s.consumed = true
		s.mu.Unlock()
		// Detached from browser navigation: losing the page must not lose a
		// one-time response containing the only copy of the private key.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		app, err := s.exchange(ctx, q.Get("code"))
		s.result <- RegistrationResult{App: app, Err: err}
		if err != nil {
			http.Error(w, "Registration exchange failed. See the local setup command; no credentials are shown here.", http.StatusBadGateway)
			return
		}
		_, _ = fmt.Fprintf(w, "<!doctype html><title>yesman App saved</title><h1>GitHub App saved securely</h1><p>Install the App on Only select repositories → idy/yesman.</p><p><a href=\"https://github.com/apps/%s/installations/new\">Install your App</a></p><p>You can close this page.</p>", template.HTMLEscapeString(app.Slug))
	default:
		http.NotFound(w, r)
	}
}

var codePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{10,200}$`)
var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)

func (s *RegistrationServer) exchange(ctx context.Context, code string) (Registration, error) {
	var app Registration
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/app-manifests/"+code+"/conversions", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := s.client.Do(req)
	if err != nil {
		return app, errors.New("GitHub manifest exchange did not complete; inspect the App registration before restarting")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return app, fmt.Errorf("GitHub manifest exchange returned HTTP %d", resp.StatusCode)
	}
	var wire struct {
		ID       int64  `json:"id"`
		ClientID string `json:"client_id"`
		Slug     string `json:"slug"`
		Name     string `json:"name"`
		URL      string `json:"html_url"`
		PEM      string `json:"pem"`
		Owner    struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"owner"`
		Permissions map[string]string `json:"permissions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&wire); err != nil {
		return app, errors.New("GitHub returned invalid manifest data")
	}
	if !strings.EqualFold(wire.Owner.Login, s.Owner) || wire.Owner.Type != "User" {
		return app, errors.New("GitHub created the App under an unexpected owner; no key was saved, inspect the registration")
	}
	if wire.ID <= 0 || !slugPattern.MatchString(wire.Slug) || wire.URL != "https://github.com/apps/"+wire.Slug {
		return app, errors.New("GitHub returned invalid App identity metadata")
	}
	if !permissionsMatch(wire.Permissions) {
		return app, errors.New("GitHub App permissions differ from pull requests write and contents read; inspect the registration")
	}
	app = Registration{ID: wire.ID, ClientID: wire.ClientID, Slug: wire.Slug, Name: wire.Name, Owner: wire.Owner.Login, URL: wire.URL}
	return app, StoreRegistration(s.Dir, app, []byte(wire.PEM))
}

func permissionsMatch(p map[string]string) bool {
	if p["contents"] != "read" || p["pull_requests"] != "write" {
		return false
	}
	for name, value := range p {
		if name == "contents" || name == "pull_requests" || name == "metadata" && value == "read" {
			continue
		}
		return false
	}
	return true
}
