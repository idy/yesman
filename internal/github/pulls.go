package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// PullRequest is a pull request, as a worktree shows the one of its branch.
type PullRequest struct {
	Repo   string `json:"repo"` // owner/name it is opened in
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	// State is open, closed or merged.
	State        string    `json:"state"`
	Draft        bool      `json:"draft,omitempty"`
	Additions    int       `json:"additions"`
	Deletions    int       `json:"deletions"`
	ChangedFiles int       `json:"changedFiles"`
	UpdatedAt    time.Time `json:"updatedAt,omitzero"`
	// Checks sums the head's checks, when GitHub was asked through its CLI.
	Checks *Checks `json:"checks,omitempty"`
}

// Checks counts a pull request's checks by how they stand.
type Checks struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
	Total   int `json:"total"`
}

// FindPull returns the pull request from headOwner's branch into repo
// (owner/name): the open one, or else the one updated last. It returns nil
// when there is none. It asks the GitHub CLI, which sees private
// repositories and checks, and without it GitHub's public API.
func FindPull(ctx context.Context, repo, headOwner, branch string) (*PullRequest, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	pulls, err := listPulls(ctx, repo, headOwner, branch)
	if err != nil || len(pulls) == 0 {
		return nil, err
	}
	rankPulls(pulls)
	p := pulls[0]
	return &p, nil
}

// rankPulls puts the open pull requests first, then the ones updated last.
func rankPulls(pulls []PullRequest) {
	sort.SliceStable(pulls, func(i, j int) bool {
		if (pulls[i].State == "open") != (pulls[j].State == "open") {
			return pulls[i].State == "open"
		}
		return pulls[i].UpdatedAt.After(pulls[j].UpdatedAt)
	})
}

var listPulls = ghPulls

func ghPulls(ctx context.Context, repo, headOwner, branch string) ([]PullRequest, error) {
	out, err := gh(ctx, "pr", "list", "--repo", repo, "--head", branch, "--state", "all", "--limit", "20",
		"--json", "number,title,url,state,isDraft,additions,deletions,changedFiles,updatedAt,headRepositoryOwner,statusCheckRollup")
	if err != nil {
		return apiPulls(ctx, repo, headOwner, branch)
	}
	var rows []struct {
		Number              int       `json:"number"`
		Title               string    `json:"title"`
		URL                 string    `json:"url"`
		State               string    `json:"state"`
		IsDraft             bool      `json:"isDraft"`
		Additions           int       `json:"additions"`
		Deletions           int       `json:"deletions"`
		ChangedFiles        int       `json:"changedFiles"`
		UpdatedAt           time.Time `json:"updatedAt"`
		HeadRepositoryOwner struct {
			Login string `json:"login"`
		} `json:"headRepositoryOwner"`
		StatusCheckRollup []rollupItem `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	var pulls []PullRequest
	for _, r := range rows {
		// --head matches the branch's name in every fork.
		if headOwner != "" && !strings.EqualFold(r.HeadRepositoryOwner.Login, headOwner) {
			continue
		}
		pulls = append(pulls, PullRequest{
			Repo: repo, Number: r.Number, Title: r.Title, URL: r.URL, State: strings.ToLower(r.State), Draft: r.IsDraft,
			Additions: r.Additions, Deletions: r.Deletions, ChangedFiles: r.ChangedFiles, UpdatedAt: r.UpdatedAt,
			Checks: sumChecks(r.StatusCheckRollup),
		})
	}
	return pulls, nil
}

// rollupItem is a check run or a commit status of statusCheckRollup.
type rollupItem struct {
	Status     string `json:"status"`     // check runs: QUEUED, IN_PROGRESS, COMPLETED…
	Conclusion string `json:"conclusion"` // check runs, once completed
	State      string `json:"state"`      // statuses: SUCCESS, PENDING, FAILURE, ERROR…
}

func sumChecks(items []rollupItem) *Checks {
	if len(items) == 0 {
		return nil
	}
	c := &Checks{Total: len(items)}
	for _, it := range items {
		v := it.State
		if it.Status != "" {
			v = it.Conclusion
			if it.Status != "COMPLETED" {
				v = "PENDING"
			}
		}
		switch strings.ToUpper(v) {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			c.Passed++
		case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
			c.Failed++
		default:
			c.Pending++
		}
	}
	return c
}

// apiPulls asks GitHub's public API, which sees public repositories and
// no checks.
func apiPulls(ctx context.Context, repo, headOwner, branch string) ([]PullRequest, error) {
	q := url.Values{"state": {"all"}, "per_page": {"20"}, "head": {headOwner + ":" + branch}}
	var rows []struct {
		Number    int       `json:"number"`
		Title     string    `json:"title"`
		HTMLURL   string    `json:"html_url"`
		State     string    `json:"state"`
		Draft     bool      `json:"draft"`
		MergedAt  time.Time `json:"merged_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := getJSON(ctx, fmt.Sprintf("%s/repos/%s/pulls?%s", apiBase, repo, q.Encode()), &rows); err != nil {
		return nil, err
	}
	pulls := make([]PullRequest, 0, len(rows))
	for _, r := range rows {
		p := PullRequest{Repo: repo, Number: r.Number, Title: r.Title, URL: r.HTMLURL, State: r.State, Draft: r.Draft, UpdatedAt: r.UpdatedAt}
		if !r.MergedAt.IsZero() {
			p.State = "merged"
		}
		pulls = append(pulls, p)
	}
	// The list leaves out the size of each; ask about the one shown.
	if len(pulls) > 0 {
		rankPulls(pulls)
		var one struct {
			Additions    int `json:"additions"`
			Deletions    int `json:"deletions"`
			ChangedFiles int `json:"changed_files"`
		}
		if err := getJSON(ctx, fmt.Sprintf("%s/repos/%s/pulls/%d", apiBase, repo, pulls[0].Number), &one); err == nil {
			pulls[0].Additions, pulls[0].Deletions, pulls[0].ChangedFiles = one.Additions, one.Deletions, one.ChangedFiles
		}
	}
	return pulls, nil
}

func getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answered %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}
