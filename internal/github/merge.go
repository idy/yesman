package github

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// PullMerge identifies a merged PR by its source repository, branch and exact
// source HEAD. The merge commit may differ (squash/rebase merges).
type PullMerge struct {
	State       string    `json:"state"`
	Merged      bool      `json:"merged"`
	MergedAt    time.Time `json:"merged_at"`
	MergeCommit string    `json:"merge_commit_sha"`
	Head        struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
}

// ReadPullMerge reads fresh merge evidence by PR number, without display caches.
func ReadPullMerge(ctx context.Context, repo string, number int) (PullMerge, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var merge PullMerge
	path := fmt.Sprintf("repos/%s/pulls/%d", repo, number)
	out, err := gh(ctx, "api", path)
	if err == nil {
		err = json.Unmarshal(out, &merge)
	} else {
		err = getJSON(ctx, apiBase+"/"+path, &merge)
	}
	return merge, err
}
