package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/yesmen"
)

// This is part of the actual per-run prompt for both roles, including resumed
// sessions. The task log/session hold evidence; this section is only policy and
// a bounded recovery index, never a substitute for the saved report.
func writeWorktreeCleanupContext(b *strings.Builder, y yesmen.Yesman, me agent.Info) {
	b.WriteString("\n## Finishing and recovering worktrees\n\n")
	b.WriteString("Use yesman_checkout with an explicit dir, purpose (review, author or release) and your board task ID for agent temporary worktrees. Use purpose primary for a checkout that must stay. Existing/user worktrees without lifecycle ownership are protected; do not adopt or scan-delete them. Each new Git instance has a private metadata nonce, including UI/user checkouts; a nonce alone grants no ownership. Old/missing/mismatched markers stay protected. Only an explicit same-owner checkout may retire a history record whose directory AND Git registration are already absent; that does not complete its unfinished task.\n")
	b.WriteString("Return unused temporary worktrees with the normal yesman_board update that finishes their task, before your final report. In update[].note save the result and tree-external evidence. In update[].worktrees name each {checkout: cleanupId, head: exact full HEAD, ref, outcome, unused: true}. Stop its processes and confirm no other person, agent or task needs its directory first. Use worktrees: [] to keep checkouts in use. The service waits for workers and file operations to drain and retries saved returns after restart. It never starts a model to clean a tree.\n")
	b.WriteString("A review return can have outcome pass, findings-handed-off, waiting-release or audit-complete, even with an open PR. To finish a detached review round before its board task ends, use yesman_worktree_remove with its exact checkout and saved conclusion, including findings handed to the author. Report retained trees and their reasons; never force.\n")
	b.WriteString("For an author or release return use outcome done and the source PR URL as ref. If your local branch was pushed to a different PR head branch, set sourceBranch to the published ref and cite the push in the evidence. The service checks the merged source repository, published branch and exact HEAD (including squash merges), and looks for other open PRs. A release stays until publication and verification finish: provide the published GitHub release URL in publication, with verification evidence in the note. The exact source survives in the checkpoint refs/yesman/worktrees/<cleanupId>.\n")
	b.WriteString("On a resumed review, first check whether the old path exists. If absent, read .yesman/worktrees.json and the saved report, then use yesman_checkout with repo, ref set to the recorded exact HEAD, dir, purpose review and your reopened task ID; verify HEAD before continuing. Never assume an old path still exists, reuse a removed tree's evidence for a new HEAD, or restore from a moving branch. yesman_repos also returns lifecycle records. Repeated cleanup is safe; retain a reused directory.\n")
	b.WriteString("A user can archive a finished author worktree in Info → Worktrees, keeping all local files. An archive record reserves its original directory. Use the panel's Worktree archives to finish a pending move or restore it.\n")
	if me.Role == agent.RoleLead {
		b.WriteString("For older managed checkouts that lack a task return, verify the original evidence and current non-use. The manual yesman_worktree_remove handoff remains available. Resume unfinished reviews first and preserve user pauses. Stopped, failed, FinishedAt and a directory name do not certify completion.\n")
	} else {
		b.WriteString("For a batch cleanup handoff, use the existing saved completion and management tool; do not invent an outcome or remove another owner's resources without the lead's recorded handoff. If you exit before cleanup, leave precise completion/recovery evidence so the lead can verify it and arrange the batch fallback.\n")
	}
	data, err := os.ReadFile(filepath.Join(y.MetaDir(), "worktrees.json"))
	if err != nil {
		return
	}
	var records []yesmen.WorktreeRecord
	if json.Unmarshal(data, &records) != nil {
		return
	}
	seen := map[string]bool{}
	shown := 0
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if seen[r.Dir] {
			continue
		}
		seen[r.Dir] = true
		if me.Role != agent.RoleLead && r.Owner != me.Name && r.CleanupOwner != me.Name {
			continue
		}
		if r.Completion == nil {
			continue
		}
		if shown == 20 {
			b.WriteString("More records in .yesman/worktrees.json.\n")
			break
		}
		if shown == 0 {
			b.WriteString("\nSaved completion/recovery index (verify the report before acting):\n")
		}
		shown++
		fmt.Fprintf(b, "- %s: checkout %s, repo %s, owner %s, purpose %s, task %s, HEAD %s, outcome %s, removed=%t; ref %s\n", oneLine(r.Dir, 80), oneLine(r.ID, 32), oneLine(r.Repo, 200), oneLine(r.Owner, 32), oneLine(r.Purpose, 20), oneLine(r.Task, 20), oneLine(r.Completion.Head, 64), oneLine(r.Completion.Outcome, 32), r.Removed, oneLine(r.Completion.Ref, 200))
	}
}
