// Package github names the repositories the repo pool takes: those on
// github.com. It turns what a user or an agent typed, owner/repo or a URL,
// into the URL to clone and, for a fork, the URL of its parent, and lists a
// user's or an organization's repositories to choose from.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Repo is a repository on github.com.
type Repo struct {
	Owner, Name string
	// Protocol is "https" or "ssh" when the input was a URL of that kind,
	// and "" for owner/repo, which names no protocol.
	Protocol string
}

var (
	ownerRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	nameRE  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// Parse reads owner/repo, github.com/owner/repo, an HTTPS or SSH URL of a
// GitHub repository, or the URL of one of its pages, such as …/tree/main.
func Parse(input string) (Repo, error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return Repo{}, errors.New("enter a GitHub repository, such as owner/repo")
	}
	var r Repo
	path, pages := in, false
	lower := strings.ToLower(in)
	switch {
	case strings.HasPrefix(lower, "git@github.com:"):
		r.Protocol, path = "ssh", in[len("git@github.com:"):]
	case strings.Contains(in, "://"):
		u, err := url.Parse(in)
		if err != nil {
			return Repo{}, notGitHub(in)
		}
		if host := strings.ToLower(u.Hostname()); host != "github.com" && host != "www.github.com" {
			return Repo{}, notGitHub(in)
		}
		switch u.Scheme {
		case "https", "http":
			r.Protocol, pages = "https", true
		case "ssh", "git+ssh":
			r.Protocol = "ssh"
		default:
			return Repo{}, notGitHub(in)
		}
		path = u.Path
	case strings.HasPrefix(lower, "github.com/"), strings.HasPrefix(lower, "www.github.com/"):
		path, pages = in[strings.Index(in, "/")+1:], true
	}
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) < 2 || len(segs) > 2 && !pages {
		return Repo{}, notGitHub(in)
	}
	r.Owner, r.Name = segs[0], strings.TrimSuffix(segs[1], ".git")
	if !ownerRE.MatchString(r.Owner) || !nameRE.MatchString(r.Name) || r.Name == "." || r.Name == ".." {
		return Repo{}, notGitHub(in)
	}
	return r, nil
}

func notGitHub(in string) error {
	return fmt.Errorf("%q is not a GitHub repository: enter owner/repo or its github.com URL", in)
}

// FullName is owner/repo.
func (r Repo) FullName() string { return r.Owner + "/" + r.Name }

// URL is the URL to clone it by, over SSH or else HTTPS.
func (r Repo) URL() string {
	if r.Protocol == "ssh" {
		return "git@github.com:" + r.FullName() + ".git"
	}
	return "https://github.com/" + r.FullName() + ".git"
}

// Resolved is what to clone for a repository, and for a fork what to fetch
// as its upstream remote.
type Resolved struct {
	URL      string
	Upstream string
}

// Resolve reads input as Parse does and asks GitHub about the repository.
// A fork's parent becomes its upstream, and GitHub's spelling of the name
// replaces the input's. owner/repo is cloned over the protocol the GitHub
// CLI is set to use, HTTPS by default. When GitHub cannot be asked, the
// repository is still resolved, without an upstream: the clone then
// reports whether it exists.
func Resolve(ctx context.Context, input string) (Resolved, error) {
	r, err := Parse(input)
	if err != nil {
		return Resolved{}, err
	}
	if r.Protocol == "" {
		r.Protocol = protocol(ctx)
	}
	var res Resolved
	if info, err := lookup(ctx, r.FullName()); err == nil {
		if c, err := Parse(info.FullName); err == nil {
			r.Owner, r.Name = c.Owner, c.Name
		}
		if info.Parent != nil {
			if p, err := Parse(info.Parent.FullName); err == nil {
				p.Protocol = r.Protocol
				res.Upstream = p.URL()
			}
		}
	}
	res.URL = r.URL()
	return res, nil
}

// info is the part of GitHub's repository object Resolve reads.
type info struct {
	FullName string `json:"full_name"`
	Parent   *struct {
		FullName string `json:"full_name"`
	} `json:"parent"`
}

// Hooks for tests.
var (
	protocol = ghProtocol
	lookup   = ghLookup
	apiBase  = "https://api.github.com"
)

// ghProtocol is the GitHub CLI's git_protocol, "ssh" or "https".
func ghProtocol(ctx context.Context) string {
	out, err := gh(ctx, "config", "get", "git_protocol", "--host", "github.com")
	if err == nil && strings.TrimSpace(string(out)) == "ssh" {
		return "ssh"
	}
	return "https"
}

// ghLookup reads a repository through the GitHub CLI, which sees what the
// user's GitHub login sees, private repositories too. Without the CLI, or
// signed out of it, it asks GitHub's public API, which sees public ones.
func ghLookup(ctx context.Context, fullName string) (info, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var v info
	if out, err := gh(ctx, "api", "--hostname", "github.com", "repos/"+fullName); err == nil {
		err = json.Unmarshal(out, &v)
		return v, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/repos/"+fullName, nil)
	if err != nil {
		return v, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return v, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return v, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v)
	return v, err
}

// Listed is one repository of an owner's list.
type Listed struct {
	FullName    string    `json:"fullName"`
	Description string    `json:"description,omitempty"`
	Private     bool      `json:"private,omitempty"`
	Fork        bool      `json:"fork,omitempty"`
	Archived    bool      `json:"archived,omitempty"`
	PushedAt    time.Time `json:"pushedAt,omitzero"`
}

// listLimit caps how many repositories List returns.
const listLimit = 1000

// ParseOwner reads a GitHub user or organization: its login, or its
// github.com URL.
func ParseOwner(input string) (string, error) {
	in := strings.TrimSpace(input)
	owner := in
	for _, prefix := range []string{"https://", "http://", "www.", "github.com/"} {
		if len(owner) >= len(prefix) && strings.EqualFold(owner[:len(prefix)], prefix) {
			owner = owner[len(prefix):]
		}
	}
	owner = strings.Trim(owner, "/")
	if !ownerRE.MatchString(owner) {
		return "", fmt.Errorf("%q is not a GitHub user or organization", in)
	}
	return owner, nil
}

// List returns the repositories of a GitHub user or organization, most
// recently pushed first: through the GitHub CLI, the private ones the
// user's login sees too, and else the public ones.
func List(ctx context.Context, owner string) ([]Listed, error) {
	owner, err := ParseOwner(owner)
	if err != nil {
		return nil, err
	}
	repos, err := list(ctx, owner)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(repos, func(i, j int) bool { return repos[i].PushedAt.After(repos[j].PushedAt) })
	return repos, nil
}

var list = ghList

func ghList(ctx context.Context, owner string) ([]Listed, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := gh(ctx, "repo", "list", owner, "--limit", strconv.Itoa(listLimit),
		"--json", "nameWithOwner,description,isPrivate,isFork,isArchived,pushedAt")
	if err == nil {
		var rows []struct {
			NameWithOwner string    `json:"nameWithOwner"`
			Description   string    `json:"description"`
			IsPrivate     bool      `json:"isPrivate"`
			IsFork        bool      `json:"isFork"`
			IsArchived    bool      `json:"isArchived"`
			PushedAt      time.Time `json:"pushedAt"`
		}
		if err := json.Unmarshal(out, &rows); err != nil {
			return nil, err
		}
		repos := make([]Listed, 0, len(rows))
		for _, r := range rows {
			repos = append(repos, Listed{FullName: r.NameWithOwner, Description: r.Description, Private: r.IsPrivate, Fork: r.IsFork, Archived: r.IsArchived, PushedAt: r.PushedAt})
		}
		return repos, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && strings.Contains(string(exit.Stderr), "not recognized") {
		return nil, noOwner(owner)
	}
	// Without the CLI, or signed out of it, the public API lists public
	// repositories.
	var repos []Listed
	for page := 1; len(repos) < listLimit; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("%s/users/%s/repos?per_page=100&type=owner&sort=pushed&page=%d", apiBase, owner, page), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		var rows []struct {
			FullName    string    `json:"full_name"`
			Description string    `json:"description"`
			Private     bool      `json:"private"`
			Fork        bool      `json:"fork"`
			Archived    bool      `json:"archived"`
			PushedAt    time.Time `json:"pushed_at"`
		}
		switch {
		case resp.StatusCode == http.StatusNotFound:
			err = noOwner(owner)
		case resp.StatusCode != http.StatusOK:
			err = fmt.Errorf("GitHub answered %s", resp.Status)
		default:
			err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rows)
		}
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			repos = append(repos, Listed(r))
		}
		if len(rows) < 100 {
			break
		}
	}
	return repos, nil
}

func noOwner(owner string) error {
	return fmt.Errorf("GitHub has no user or organization named %q", owner)
}

func gh(ctx context.Context, args ...string) ([]byte, error) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "NO_COLOR=1", "GH_NO_UPDATE_NOTIFIER=1")
	return cmd.Output()
}
