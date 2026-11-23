# Worktree ownership and recovery

A managed temporary checkout belongs to the Board task that created it. Marking the task **done** makes the checkout eligible for automatic cleanup. Save the result and evidence outside the directory before finishing the task. Keep release tasks open through publication and verification. Automatic cleanup keeps directories of open, paused and dropped tasks. A failed or completed agent run leaves the Board task's status unchanged.

Yes Man binds the task and workspace hire when the authenticated owner checks out a temporary tree. Reassigning the task leaves this binding intact. An older record can qualify if its creation-time directory identity, original owner generation, and the matching task in that workspace establish the association. Earlier records without that evidence stay on the explicit management paths. Existing UI/user and primary checkouts stay protected.

## Keeping or explicitly returning a checkout

An ordinary `yesman_board` status update to `done` is sufficient. Use `worktrees: []` to keep all the checkouts of a task. Listing specific returns keeps the omitted checkouts. The keep choice survives reopening. An owner or lead can replace it with a new explicit disposition. Use `purpose: primary` when you create a checkout that should stay long term.

An optional explicit return documents a particular source and introduces additional verification for that return. Stop its processes and check whether another person, agent or task needs the directory before returning it. Save the evidence in the task note or a report it references.

```json
{
  "update": [{
    "id": "t3",
    "status": "done",
    "note": "Tests passed. Saved the review handoff and push receipt in .yesman/evidence/t3/author/report.md.",
    "worktrees": [{
      "checkout": "<cleanupId>",
      "head": "<full-source-commit>",
      "ref": "https://github.com/owner/repo/pull/7",
      "outcome": "done",
      "unused": true
    }]
  }]
}
```

The task and its returns are saved together. An unmerged PR can be returned for later collection. `sourceBranch` names the actual published head ref if it differs from the registered local branch. Cite the push or PR creation in the evidence. The service preserves both names and checks the source repository, published ref and full commit against the merged PR. It checks the local and published refs for another open PR in the repository or its upstream. A published alias stays attached to the checkout after a return is withdrawn. Cleanup checks that alias against GitHub again and uses the current Board decision for authorization. A release return additionally supplies `publication`, the published GitHub release URL. The service resolves its tag to the verified PR source or merge commit. The publisher verifies the delivery and records those results in the note.

The service discovers eligible tasks on startup and after relevant Board changes. Owner retirement wakes waiting jobs, and pending checks retry with backoff from one minute to one hour. Immediately before removal Yes Man rechecks the workspace hire, current task close, recorded users, directory and Git state under the same locks. Ordinary cleanup checks for open PRs on known source branches. A branch can qualify without ever having a pull request.

A saved Board certification belongs to one task close and one authenticated return. The first manual request captures that close, owner and checkout disposition before remote verification. The Board stores a generation for each task that changes when its status, owner, owner incarnation or pause changes. Reopening and dropping a task again therefore starts a distinct round even when both closes have an empty `DoneAt`. The service checks the captured decision again before saving completion, granting a handoff or removing the directory. A note edit can proceed during the lookup. An explicit keep requires the owner or lead to return the checkout on the Board before agent or background collection. A manual proposal matching an existing Board return uses its certification and round. Reopening the task, choosing to keep the checkout, or replacing the return withdraws that certification. Manual retries, delegated retries and the service read the current disposition again under the final local lock. An owner or lead can return the same source in a new round with a new result note. After verification the registry preserves the prior certification and its round in `priorReturns`. Crash recovery can record a previously completed removal after withdrawal by checking the absent directory, every Git registration and the exact recovery ref.

Closing the last open task of an owner or delegate wakes a checkout that was waiting on that local work. Pending remote checks keep their backoff. The single worker orders discovery and collection requests by when they became due. One slow remote check can delay other requests for up to that attempt's timeout. Configured workspace aliases and paths reported by Git share a pool lease using the physical parent directory and checkout name.

A detached review round that hands off findings before its board task finishes can still use the explicit tool. Both roles receive the return and recovery instructions in their run context.

## Cleanup from the Worktrees panel

Open the context menu on **Worktrees** and choose **Clean up worktrees…**. The preview lists the exact directories that can be permanently removed, including eligible temporary checkouts from completed or dropped tasks. Older recorded checkouts can qualify through their original Git registration and instance marker. A local checkout with a stale Git pointer needs its recorded directory identity and a complete check for Git registrations. Expand **Keep** to see holds. The batch respects explicit Board retention, protects open PRs and keeps active, primary and unknown checkouts. Close programs and Files editors using the worktrees before confirming. The confirmation discards tracked edits, untracked and ignored files, and build output. Existing branches and tags are preserved. The batch creates no backup or recovery reference and writes no task completion.

Open the context menu on a directory or pull-request row and choose **Remove Worktree…** to review just that checkout. This also works for a clean, unused checkout of a completed or dropped task whose branch was never published. The dialog shows its full directory and current commit. A branch with an open pull request stays in place. If the Board explicitly kept an otherwise eligible checkout, the dialog explains that confirming this single removal overrides that decision. A changed task round, decision or checkout requires a fresh preview. Close programs using the directory before confirming. Escape or Cancel keeps it. The menu key or Shift+F10 opens either context menu from the keyboard.

Each confirmation rechecks the workspace hire, the checkout and directory identity, the current commit when registered, the Board and the recorded users. Files holds a directory while one of its writable editors is open, including editors under All files. Single removal additionally requires clean, verifiable Git state. Its checks cover tracked or untracked changes, ignored files, hidden index flags, unknown directory entries, locks and submodules. A preview on a file system without directory birth times holds the verified checkout directory open through confirmation and compares its device and inode. A checkout with a recorded creation identity still requires that identity. Standalone Git data, ambiguous filesystem entries and unrelated registrations stay available for inspection.

For single removal, Git saves the exact source, recoverable private history and metadata as described in [Recovery and retained history](#recovery-and-retained-history). A checkout with saved conflict stages or an unverifiable index stays in place. The desktop records removal independently of task completion. Each directory in a batch has its own result, including cases where the directory is gone and the Git registration or checkout record still needs attention.

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

The pool also checks the private Git directory that disappears with the checkout. Both object IDs in each HEAD reflog entry, `ORIG_HEAD`, its reflog and `FETCH_HEAD` contribute recovery objects. Their additional targets have immutable refs under `refs/yesman/worktree-history/<cleanupId>/<objectId>`. The original bytes of those files and `COMMIT_EDITMSG` are recoverable with `git cat-file blob refs/yesman/worktree-metadata/<cleanupId>/<file>/<blobId>`. Use `git for-each-ref refs/yesman/worktree-metadata/<cleanupId>/` to list them. Before removal the pool verifies the objects, all recovery refs and the current private files. A private named ref, unknown state, incomplete reflog entry, unreadable object, changed pin, or a history exceeding 1 MiB per file or 256 distinct history objects keeps the checkout available for inspection.

A clean Git index can still contain `resolve-undo` records for a resolved conflict. While those records exist, the pool keeps the whole checkout so `git checkout -m` can reconstruct the original paths, modes and file versions. Inspection and final removal both check the private index. An unreadable or unsupported index, an invalid checksum, a Git diagnostic or an index larger than the 64 MiB verification limit also keeps the checkout.

On resuming, check whether the old path exists. If absent, reopen the task when necessary and read the report and lifecycle record. `cleanupHead` identifies the exact commit preserved at removal. Older records use `completion.head`. Call `yesman_checkout` with the recorded repository and exact commit as `ref` for a detached review, or as `base` for a new branch, and verify HEAD. A fresh checkout gets a fresh ID and task binding. Removed records and worker sessions remain available as evidence and are indexed in the role context and `yesman_repos`.

The service starts from registered instances and checks the authority for the current cleanup path. It never adopts existing directories or infers ownership from a name. Repeated cleanup after successful removal is harmless. An absent path with a remaining Git registration is retained for recovery instead of pruning unrelated registrations. If an explicit checkout request finds an earlier same-owner record but both its directory and all Git registrations are already gone, it retires only that history record and creates a fresh instance, including for older calls that omitted purpose. This never completes an unfinished task, adopts an existing unknown tree, or transfers another owner's resource. A replacement directory is never deleted. Legacy resources need separate provenance review; they are not automatically adopted by this policy.

Tests use isolated local Git repositories/workspaces and mocked GitHub merge responses. They exercise the real checkout/remove handlers, durable records, role contexts, exact reconstruction, squash source preservation through fetch/prune/GC, ownership handoff, active-owner reservations and retention cases without invoking external models.
