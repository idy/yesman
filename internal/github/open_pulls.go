package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// HasOpenPullFrom asks only about open pulls from this source in the target
// repository. A historical merged PR can share a head ref with a new one.
// Neither the display cache nor the latest N historical results answers this.
func HasOpenPullFrom(ctx context.Context, target, owner, branch string) (bool, error) {
	q := url.Values{"state": {"open"}, "head": {owner + ":" + branch}, "per_page": {"1"}}
	path := fmt.Sprintf("repos/%s/pulls?%s", target, q.Encode())
	var rows []json.RawMessage
	if err := readLifecycleJSON(ctx, path, &rows); err != nil {
		return false, err
	}
	if rows == nil {
		return false, fmt.Errorf("missing open pull request list")
	}
	return len(rows) != 0, nil
}

// ReadPublishedTag resolves the published release's tag (annotated or
// lightweight) to its commit. A draft, missing release or unrelated tag
// cannot certify a release checkout's publication.
func ReadPublishedTag(ctx context.Context, repo, tag string) (string, error) {
	path := "repos/" + repo
	var release struct {
		Tag       string    `json:"tag_name"`
		Draft     bool      `json:"draft"`
		Published time.Time `json:"published_at"`
	}
	if err := readLifecycleJSON(ctx, path+"/releases/tags/"+url.PathEscape(tag), &release); err != nil {
		return "", err
	}
	if release.Tag != tag || release.Draft || release.Published.IsZero() {
		return "", fmt.Errorf("release tag is not published")
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := readLifecycleJSON(ctx, path+"/commits/"+url.PathEscape("refs/tags/"+tag), &commit); err != nil {
		return "", err
	}
	return commit.SHA, nil
}

func readLifecycleJSON(ctx context.Context, path string, dst any) error {
	if out, err := gh(ctx, "api", path); err == nil {
		return json.Unmarshal(out, dst)
	}
	return getJSON(ctx, apiBase+"/"+path, dst)
}
