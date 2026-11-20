# Worktree completion and recovery

Agent runs finish independently of PRs and releases. A completed review round can retire its detached checkout while a PR is open, has findings handed off, or waits for a release. Author branches require an exact merged PR source HEAD and a finished task. Release branches also require their release task to be **done**, after publication and verification.

`BuildContext` supplies this policy to both roles before every run. Workers save their conclusion and reclaim their own temporary resources before reporting. The lead checks missed completion evidence and delegates one batch cleanup task. Stopped/failed processes, `FinishedAt`, restart, detached status and directory names do not establish review completion. Unfinished reviews resume first.

## Cleanup from the Worktrees panel

Open the context menu on **Worktrees** in the Info panel and choose **Clean up worktrees…**. The preview lists the local directories eligible for removal. Expand **Keep** to see why other directories will stay. Bulk cleanup uses saved completion evidence and verifies the merged pull request again for an author or release branch.

Open the context menu on a directory or pull-request row and choose **Remove Worktree…** to review just that checkout. This also works for a clean, unused checkout of a completed or dropped task whose branch was never published. The dialog shows its full directory and current commit. Close programs using the directory before confirming. Escape or Cancel keeps it. The menu key or Shift+F10 opens either context menu from the keyboard.

Each confirmation checks the workspace hire, original checkout and directory identity, current commit, board and owners again. The Files pane keeps directories with unsaved edits. Tracked changes, untracked or ignored files, hidden index flags, unknown directory entries, locks, submodules, protected branches and unfinished or paused tasks keep a checkout in place. Historical registrations with matching private markers and both Git links can be verified even if they predate the recorded directory inode. A missing registration or ambiguous link stays available for inspection.

Git saves the source in `refs/yesman/worktrees/<cleanupId>` before removing the directory. Local branches, tags, the shared repository and lifecycle records stay available. Commits that would lose their last lasting reference with the checkout’s private HEAD reflog get references under `refs/yesman/worktree-history/<cleanupId>/`. Use the recorded repository and full commit to check out that revision again. The desktop records a user removal separately from the task’s completion. Each directory in a batch has its own result.

## Management tools

Create temporary work with an explicit `dir`, `purpose` (`review`, `author`, `release`) and an open board `task` owned by the caller. A review also needs `ref` and must be read-only/detached. `purpose: primary`, omitted/unknown lifecycle purposes, UI/user checkouts and legacy trees without lifecycle records stay protected.

The checkout result includes a `cleanupId` for this actual Git instance. Every pool creation path, including UI/user checkout, writes a fresh nonce to the worktree's private Git metadata; it does not dirty the working tree or confer agent ownership. Cleanup checks the nonce against its ownership record under the pool lock and rechecks immediately before deletion. Missing, unknown or mismatched markers are retained and never backfilled. For example:

```json
{"repo":"github.com/owner/repo","ref":"<full-reviewed-HEAD>","dir":"repo-review","purpose":"review","task":"t3"}
```

After saving the conclusion and test/handoff evidence outside the worktree, stopping its tests/processes and confirming no other task/agent uses it, call `yesman_worktree_remove`:

```json
{
  "dir":"repo-review",
  "checkout":"<cleanupId-from-this-checkout>",
  "completion":{
    "head":"<full-reviewed-HEAD>",
    "outcome":"findings-handed-off",
    "ref":"https://github.com/owner/repo/pull/7",
    "evidence":"Findings and test results saved in board t3/session reviewer. Rebuild repo-review from this exact HEAD with yesman_checkout.",
    "unused":true
  }
}
```

Review outcomes are `pass`, `findings-handed-off`, `waiting-release` and `audit-complete`. Author/release completion uses `done` and the merged GitHub PR URL. The server reads fresh PR merge evidence, comparing its source repository, branch and SHA; the squash merge commit can differ. New work after merge stays protected.

Verified completion is written to `.yesman/worktrees.json` **before** removal is attempted. Author/release proposals first verify the task, merged PR source, actual Git instance and HEAD. A rejected proposal is saved as failed evidence without granting removal authority; the owner or lead can correct its HEAD/ref and retry with the same checkout ID. Repeated identical failed attempts do not duplicate history. Retained results include a reason. A lead can supply verified missed completion evidence and `handoff: "cleanup-worker"` with the directory/checkout ID; that only records permission, without removing the tree. The delegate retries removal using the saved evidence. Workers cannot certify another owner's completion or grant themselves permission.

Checkout and removal serialize with managed file I/O on the server's workspace mutex. After waiting, the server rechecks the request's exact workspace hire, agent generation and live process credential using `BeginOperation`. Its short workspace admission lease ends before Git or verification; the process change lease remains through the operation and final lifecycle bookkeeping. Stop cancels the operation before draining it, and Archive stops and drains these operations before moving the workspace. The operation never reacquires a workspace lease while Archive may be draining it. Request cancellation reaches Git/verification, and a successful checkout or removal still records its result before releasing the lease. Cancellation is not a rollback of completed work.

The API checks the owner identity and active state. Its final removal reservation excludes owner startup/input without holding the manager lock during git operations. Git rechecks the registered workspace, repository, branch and exact HEAD under the pool lock. Cleanup rejects force, primary/main, tracked changes, untracked/ignored files, hidden index flags and unfinished git operations. External process/task use is the agent's explicit `unused` attestation; this is not an OS process scanner.

## Recovery and retained history

Each eligible cleanup preserves the exact source in `refs/yesman/worktrees/<cleanupId>` before removing the worktree and Git registration. Existing branches, refs and tags are never deleted or moved. The checkpoint keeps a squash-merged source recoverable after remote branch pruning and GC. A failed removal can leave its checkpoint in place safely.

On resuming, check whether the old path exists. If absent, read the report/lifecycle record, reopen the task when necessary, and call `yesman_checkout` with the recorded repository and **exact full HEAD** as `ref`. Verify HEAD before continuing. A fresh checkout gets a fresh ID and no completion, so an old cleanup event cannot remove a new review round at the same path. Removed records and worker sessions remain available as evidence; they are also indexed in role context and `yesman_repos`.

There is no startup sweep, PR watcher, background janitor, legacy ownership inference or directory-name heuristic. Repeated cleanup after successful removal is harmless. An absent path with a remaining Git registration is retained for recovery instead of pruning unrelated registrations. If an explicit checkout request finds an earlier same-owner record but both its directory and all Git registrations are already gone, it retires only that history record and creates a fresh instance, including for older calls that omitted purpose. This never completes an unfinished task, adopts an existing unknown tree, or transfers another owner's resource. A replacement directory is never deleted. Legacy resources need separate provenance review; they are not automatically adopted by this policy.

Tests use isolated local Git repositories/workspaces and mocked GitHub merge responses. They exercise the real checkout/remove handlers, durable records, role contexts, exact reconstruction, squash source preservation through fetch/prune/GC, ownership handoff, active-owner reservations and retention cases without invoking external models.
