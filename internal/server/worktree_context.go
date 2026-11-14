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
	b.WriteString("Use yesman_checkout with an explicit dir, purpose (review, author or release) and your board task ID for agent temporary worktrees. Use purpose primary for a checkout that must stay. Existing/user worktrees without lifecycle ownership are protected; do not adopt or scan-delete them.\n")
	b.WriteString("When this round of a read-only review is complete, save its conclusion, PR/ref, exact full HEAD, evidence outside the tree, and reconstruction instructions before cleanup. PASS, findings handed to the lead/author, and waiting only for release all finish a review round, even with an open PR or blocked board task. Stop any tests/processes using the tree and confirm no other agent/task uses it. Then call yesman_worktree_remove yourself with checkout set to this checkout's cleanupId and completion {head, outcome: pass|findings-handed-off|waiting-release|audit-complete, ref, evidence, unused: true}, before your final report. Only clean, registered, reconstructable detached trees you created or were explicitly handed may be reclaimed. Report retained trees and the reason; never force.\n")
	b.WriteString("For author/release branches use completion outcome done and the merged GitHub PR URL as ref. The tool checks the PR source repository, branch and exact HEAD, including squash merges; git branch --merged alone is insufficient. Finish the associated task first. A release branch stays until publication and verification finish, even if its PR has merged. Preserve primary/main, active work, open/unmerged PR branches, post-merge commits, dirty/untracked/ignored files and unknown ownership. Removal keeps branches, refs, tags, commits, sessions and completion evidence; a management-owned refs/yesman/worktrees/<checkout-id> checkpoint preserves exact source HEAD through remote branch pruning and garbage collection.\n")
	b.WriteString("On a resumed review, first check whether the old path exists. If absent, read .yesman/worktrees.json and the saved report, then use yesman_checkout with repo, ref set to the recorded exact HEAD, dir, purpose review and your reopened task ID; verify HEAD before continuing. Never assume an old path still exists, reuse a removed tree's evidence for a new HEAD, or restore from a moving branch. yesman_repos also returns lifecycle records. Repeated cleanup is safe; retain a reused directory.\n")
	if me.Role == agent.RoleLead {
		b.WriteString("When a worker reports completion or after restart, check the board, saved completion evidence and report/session. If cleanup was missed, start one batch cleanup worker for all verified completed temporary resources; explicitly delegate each with yesman_worktree_remove handoff set to that worker's name (supply verified completion if it was not saved). Handoff records permission without removing the tree. Stopped, failed, FinishedAt, detached status and a directory containing review do not prove task/review completion. Resume unfinished reviews first, respecting a user stop; do not clean them. Keep agent history/evidence after retiring resources.\n")
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
