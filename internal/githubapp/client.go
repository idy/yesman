package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client holds the private key and short-lived tokens only in the Go process.
// Each token is narrowed to one explicitly configured repository. No personal
// credential fallback exists. A new client is needed when configuration changes.
type Client struct {
	app      Registration
	key      *rsa.PrivateKey
	allowed  map[string]bool
	http     *http.Client
	base     string
	now      func() time.Time
	mu       sync.Mutex
	tokens   map[string]installationToken
	flights  map[string]*tokenFlight
	lifetime context.Context
	stop     context.CancelFunc
	// Tests can adapt gh's transport without changing any OS trust store. The
	// production constructor always uses the strict github.com environment.
	ghEnv func(string, string) []string
}

type installationToken struct {
	value        string
	expires      time.Time
	installation int64
}
type tokenFlight struct {
	done  chan struct{}
	token installationToken
	err   error
}

func NewClient(dir string, repositories []string) (*Client, error) {
	app, key, err := Load(dir)
	if err != nil {
		return nil, err
	}
	if len(repositories) == 0 {
		return nil, errors.New("select at least one repository for the GitHub App")
	}
	return newClient(app, key, repositories)
}

func newClient(app Registration, key *rsa.PrivateKey, repositories []string) (*Client, error) {
	lifetime, stop := context.WithCancel(context.Background())
	c := &Client{app: app, key: key, allowed: map[string]bool{}, http: safeHTTPClient(), base: githubAPI, now: time.Now, tokens: map[string]installationToken{}, flights: map[string]*tokenFlight{}, lifetime: lifetime, stop: stop, ghEnv: ghEnvironment}
	for _, repo := range repositories {
		if !validRepository(repo) {
			return nil, errors.New("App repositories must be explicit owner/repo names")
		}
		c.allowed[strings.ToLower(repo)] = true
	}
	return c, nil
}

func (c *Client) Close() {
	c.stop()
	c.mu.Lock()
	c.tokens = map[string]installationToken{}
	c.mu.Unlock()
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)

func validRepository(repo string) bool {
	if !repoPattern.MatchString(repo) {
		return false
	}
	_, name, _ := strings.Cut(repo, "/")
	return name != "." && name != ".."
}

func (c *Client) jwt() (string, error) {
	now := c.now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(c.app.ID, 10)})
	payload := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	hash := sha256.Sum256([]byte(payload))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, hash[:])
	if err != nil {
		return "", errors.New("could not sign GitHub App authentication")
	}
	return payload + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// apiError intentionally omits response bodies, requests and URLs: GitHub or a
// proxy may reflect Authorization, code, or submitted content in an error.
type apiError struct{ status int }

func (e *apiError) Error() string {
	return fmt.Sprintf("GitHub App request returned HTTP %d", e.status)
}

func (c *Client) request(ctx context.Context, method, path, credential string, body, out any) error {
	ctx, cancel := joinedContext(ctx, c.lifetime)
	defer cancel()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return errors.New("invalid GitHub App request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(data))
	if err != nil {
		return errors.New("invalid GitHub App request")
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("GitHub App request did not complete; verify remote state before retrying a write")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiError{status: resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out); err != nil {
		return errors.New("GitHub App response was invalid; verify remote state before retrying a write")
	}
	return nil
}

func (c *Client) token(ctx context.Context, repo string) (installationToken, error) {
	repo = strings.ToLower(repo)
	if !validRepository(repo) || !c.allowed[repo] {
		return installationToken{}, errors.New("repository is not enabled for this GitHub App; personal credentials will not be used")
	}
	if err := ctx.Err(); err != nil {
		return installationToken{}, err
	}
	if err := c.lifetime.Err(); err != nil {
		return installationToken{}, err
	}
	c.mu.Lock()
	if t, ok := c.tokens[repo]; ok && t.expires.After(c.now().Add(time.Minute)) {
		c.mu.Unlock()
		return t, nil
	}
	f := c.flights[repo]
	if f == nil {
		f = &tokenFlight{done: make(chan struct{})}
		c.flights[repo] = f
		// A canceled waiter must not abort another agent's concurrent mint.
		// The shared request still has its own fixed 30-second deadline.
		go func() {
			ctx, cancel := context.WithTimeout(c.lifetime, 30*time.Second)
			defer cancel()
			t, err := c.mint(ctx, repo)
			c.mu.Lock()
			if err == nil && c.lifetime.Err() == nil {
				c.tokens[repo] = t
			}
			if c.lifetime.Err() != nil {
				t, err = installationToken{}, c.lifetime.Err()
			}
			f.token, f.err = t, err
			delete(c.flights, repo)
			close(f.done)
			c.mu.Unlock()
		}()
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return installationToken{}, ctx.Err()
	case <-f.done:
		return f.token, f.err
	}
}

func (c *Client) mint(ctx context.Context, repo string) (installationToken, error) {
	var empty installationToken
	jwt, err := c.jwt()
	if err != nil {
		return empty, err
	}
	var install struct {
		ID          int64   `json:"id"`
		AppID       int64   `json:"app_id"`
		SuspendedAt *string `json:"suspended_at"`
		Account     struct {
			Login string `json:"login"`
		} `json:"account"`
		Permissions map[string]string `json:"permissions"`
	}
	if err := c.request(ctx, "GET", "/repos/"+repo+"/installation", jwt, nil, &install); err != nil {
		return empty, fmt.Errorf("find App installation for %s: %w", repo, err)
	}
	owner, name, _ := strings.Cut(repo, "/")
	if install.ID <= 0 || install.AppID != c.app.ID || !strings.EqualFold(install.Account.Login, owner) || install.SuspendedAt != nil {
		return empty, errors.New("GitHub App installation is suspended or belongs to a different App or owner")
	}
	if install.Permissions["pull_requests"] != "write" || install.Permissions["contents"] != "read" {
		return empty, errors.New("GitHub App installation needs pull requests write and contents read")
	}
	var token struct {
		Token        string            `json:"token"`
		Expires      time.Time         `json:"expires_at"`
		Permissions  map[string]string `json:"permissions"`
		Repositories []struct {
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	body := map[string]any{"repositories": []string{name}, "permissions": map[string]string{"contents": "read", "pull_requests": "write"}}
	if err := c.request(ctx, "POST", "/app/installations/"+strconv.FormatInt(install.ID, 10)+"/access_tokens", jwt, body, &token); err != nil {
		return empty, fmt.Errorf("create repository-scoped App token: %w", err)
	}
	if len(token.Token) < 20 || !token.Expires.After(c.now().Add(time.Minute)) || token.Expires.After(c.now().Add(65*time.Minute)) || !permissionsMatch(token.Permissions) || len(token.Repositories) != 1 || !strings.EqualFold(token.Repositories[0].FullName, repo) {
		return empty, errors.New("GitHub returned an invalid or insufficiently scoped App token")
	}
	return installationToken{value: token.Token, expires: token.Expires, installation: install.ID}, nil
}

func (c *Client) invalidate(repo string, token installationToken) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// A late 401 from a concurrent request must not evict a fresh token.
	if current := c.tokens[strings.ToLower(repo)]; current.value == token.value {
		delete(c.tokens, strings.ToLower(repo))
	}
}

func (c *Client) installationRequest(ctx context.Context, repo, method, path string, body, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.token(ctx, repo)
		if err != nil {
			return err
		}
		err = c.request(ctx, method, path, token.value, body, out)
		var api *apiError
		if errors.As(err, &api) && api.status == http.StatusUnauthorized {
			c.invalidate(repo, token)
			if attempt == 0 {
				continue
			}
		}
		return err
	}
	panic("unreachable")
}

// Connection is safe for UI and model context. It never contains credentials.
type Connection struct {
	App            Registration `json:"app"`
	Repository     string       `json:"repository"`
	InstallationID int64        `json:"installation_id"`
	Expires        time.Time    `json:"expires_at"`
}

func (c *Client) Test(ctx context.Context, repo string) (Connection, error) {
	_, err := c.token(ctx, repo)
	if err != nil {
		return Connection{}, err
	}
	// Exercise the actual installation credential, rather than accepting a
	// successful mint as proof the selected repository is usable.
	var repository struct {
		FullName string `json:"full_name"`
	}
	if err := c.installationRequest(ctx, repo, "GET", "/repos/"+repo, nil, &repository); err != nil {
		return Connection{}, err
	}
	if !strings.EqualFold(repo, repository.FullName) {
		return Connection{}, errors.New("GitHub returned a different repository")
	}
	token, err := c.token(ctx, repo)
	if err != nil {
		return Connection{}, err
	}
	return Connection{App: c.app, Repository: repository.FullName, InstallationID: token.installation, Expires: token.expires}, nil
}

// PullInput deliberately contains no merge, reviewer, assignee, label, upload,
// or authentication options. The caller must push its branch before creating it.
type PullInput struct {
	Repository string `json:"repository"`
	Head       string `json:"head"`
	Base       string `json:"base"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
}

type Pull struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	Draft  bool   `json:"draft"`
	User   struct {
		Login     string `json:"login"`
		Type      string `json:"type"`
		AvatarURL string `json:"avatar_url"`
		URL       string `json:"html_url"`
	} `json:"user"`
	Head struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
}

func (c *Client) CreatePull(ctx context.Context, in PullInput) (Pull, error) {
	var pull Pull
	if !validRepository(in.Repository) || strings.TrimSpace(in.Title) == "" || len(in.Title) > 256 || len(in.Body) > 65536 || !validRef(in.Base) || !validRef(in.Head) {
		return pull, errors.New("provide an explicit repository, branch names, a title and a bounded PR body")
	}
	body := map[string]any{"head": in.Head, "base": in.Base, "title": in.Title, "body": in.Body, "draft": in.Draft}
	if err := c.installationRequest(ctx, in.Repository, "POST", "/repos/"+in.Repository+"/pulls", body, &pull); err != nil {
		return pull, err
	}
	if pull.Number <= 0 || pull.User.Type != "Bot" || pull.User.Login != c.app.Slug+"[bot]" {
		return pull, errors.New("PR was created but GitHub returned an unexpected author; inspect the repository before retrying")
	}
	return pull, nil
}

func validRef(s string) bool {
	return s != "" && len(s) < 256 && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, "\x00\r\n ") && !strings.Contains(s, "..")
}
