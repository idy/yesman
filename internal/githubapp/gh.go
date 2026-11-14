package githubapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ClientFromKey is for trusted Go backends only. Neither the key nor Client is
// exposed by a desktop binding or the agent's HTTP API.
func ClientFromKey(app Registration, data []byte, repositories []string) (*Client, error) {
	if app.ID <= 0 {
		return nil, errors.New("enter the numeric GitHub App ID")
	}
	key, err := parseKey(data)
	if err != nil {
		return nil, err
	}
	return newClient(app, key, repositories)
}

func (c *Client) Inspect(ctx context.Context) (Registration, error) {
	jwt, err := c.jwt()
	if err != nil {
		return Registration{}, err
	}
	var wire struct {
		ID       int64  `json:"id"`
		ClientID string `json:"client_id"`
		Slug     string `json:"slug"`
		Name     string `json:"name"`
		URL      string `json:"html_url"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
		Permissions map[string]string `json:"permissions"`
	}
	if err := c.request(ctx, "GET", "/app", jwt, nil, &wire); err != nil {
		return Registration{}, err
	}
	if wire.ID != c.app.ID || !slugPattern.MatchString(wire.Slug) || !ownerPattern.MatchString(wire.Owner.Login) || wire.URL != "https://github.com/apps/"+wire.Slug {
		return Registration{}, errors.New("GitHub returned an unexpected App identity")
	}
	if !permissionsMatch(wire.Permissions) {
		return Registration{}, errors.New("the App must have only pull requests write, contents read and metadata read permissions")
	}
	return Registration{ID: wire.ID, ClientID: wire.ClientID, Slug: wire.Slug, Name: wire.Name, Owner: wire.Owner.Login, URL: wire.URL}, nil
}

// ReadKeyFile imports a user-selected file without ever sending it through the
// webview. Native file dialogs authorize the path; symlinks/nonregular/large
// files are rejected. The durable copy is encrypted on every desktop platform.
func ReadKeyFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("select an absolute private key path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("could not open the selected private key file")
	}
	if !info.Mode().IsRegular() || info.Size() > 16384 {
		return nil, errors.New("select one regular PEM key file smaller than 16 KiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("could not open the selected private key file")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("selected key file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(data) > 16384 {
		return nil, errors.New("could not read the selected private key file")
	}
	if _, err := parseKey(data); err != nil {
		return nil, err
	}
	return data, nil
}

type limitedOutput struct {
	bytes.Buffer
	limit int
}

func (b *limitedOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		_, _ = b.Buffer.Write(data[:min(remaining, n)])
	}
	return n, nil
}

// ghEnvironment is an allowlist, not os.Environ plus overrides: the child does
// not inherit unrelated API keys, GH_DEBUG, gh aliases, personal authentication,
// git hooks/config, or arbitrary command-loader environment variables.
func ghEnvironment(dir, token string) []string {
	allowed := []string{"PATH", "SystemRoot", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "TMP", "TEMP", "TMPDIR", "LANG", "LC_ALL", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"}
	env := make([]string, 0, len(allowed)+12)
	for _, name := range allowed {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return append(env, "HOME="+dir, "USERPROFILE="+dir, "XDG_CONFIG_HOME="+dir, "GH_CONFIG_DIR="+dir, "GH_HOST=github.com", "GH_TOKEN="+token, "GH_PROMPT_DISABLED=1", "GH_TELEMETRY=0", "GH_NO_UPDATE_NOTIFIER=1", "GH_NO_EXTENSION_UPDATE_NOTIFIER=1", "NO_COLOR=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
}

// CreateWithGH runs the installed, app-resolved gh with an installation token in
// that child only. The branch must already be pushed; --head prevents gh from
// offering to push. The real gh API call determines the author. No ambiguous
// subprocess failure is retried, since gh may already have created the PR.
func (c *Client) CreateWithGH(ctx context.Context, gh string, in PullInput) (Pull, error) {
	var pull Pull
	ctx, stop := joinedContext(ctx, c.lifetime)
	defer stop()
	in.Repository = strings.ToLower(in.Repository)
	if !filepath.IsAbs(gh) {
		return pull, errors.New("GitHub CLI is unavailable; install it and restart yesman")
	}
	if !repoPattern.MatchString(in.Repository) || strings.TrimSpace(in.Title) == "" || len(in.Title) > 256 || len(in.Body) > 65536 || !validRef(in.Head) || !validRef(in.Base) || strings.ContainsRune(in.Title, 0) || strings.ContainsRune(in.Body, 0) {
		return pull, errors.New("App PR creation requires an explicit repository, head, base, title and bounded body")
	}
	// Exercise/refresh authentication before the write, then take the actual
	// current cache entry. A cached revoked token is not sent blindly to gh.
	if _, err := c.Test(ctx, in.Repository); err != nil {
		return pull, err
	}
	token, err := c.token(ctx, in.Repository)
	if err != nil {
		return pull, err
	}
	dir, err := os.MkdirTemp("", "yesman-app-gh-*")
	if err != nil {
		return pull, errors.New("could not create an isolated gh directory")
	}
	defer os.RemoveAll(dir)
	args := []string{"pr", "create", "--repo", in.Repository, "--head", in.Head, "--base", in.Base, "--title", in.Title, "--body-file", "-"}
	if in.Draft {
		args = append(args, "--draft")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, gh, args...)
	cmd.Dir = dir
	cmd.Env = ghEnvironment(dir, token.value)
	cmd.Stdin = strings.NewReader(in.Body)
	var stdout, stderr limitedOutput
	stdout.limit = 8192
	stderr.limit = 8192
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return pull, errors.New("App PR creation was interrupted; inspect the repository before retrying")
		}
		// Do not expose raw gh errors: credential helpers/proxies may print secrets.
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return pull, fmt.Errorf("gh pr create failed (exit %d); inspect the repository before retrying; no personal fallback was used", exit.ExitCode())
		}
		return pull, errors.New("could not start GitHub CLI for App PR creation")
	}
	output := strings.TrimSpace(stdout.String())
	number, err := pullNumber(output, in.Repository)
	if err != nil {
		return pull, errors.New("GitHub CLI completed without a recognizable PR URL; inspect the repository before retrying")
	}
	if err := c.installationRequest(ctx, in.Repository, "GET", "/repos/"+in.Repository+"/pulls/"+strconv.Itoa(number), nil, &pull); err != nil {
		return Pull{Number: number, URL: output}, fmt.Errorf("PR %d was created but identity verification failed: %w", number, err)
	}
	if pull.Number != number || pull.User.Type != "Bot" || pull.User.Login != c.app.Slug+"[bot]" {
		return Pull{Number: number, URL: output}, errors.New("PR was created with an unexpected author; inspect it before retrying")
	}
	pull.URL = output // the validated canonical URL, not an unchecked response field
	return pull, nil
}

func pullNumber(raw, repo string) (int, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return 0, errors.New("invalid PR URL")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 4 || !strings.EqualFold(parts[0]+"/"+parts[1], repo) || parts[2] != "pull" {
		return 0, errors.New("invalid PR URL")
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n < 1 {
		return 0, errors.New("invalid PR URL")
	}
	return n, nil
}
