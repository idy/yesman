# Worktree completion and recovery

Agent runs finish independently of PRs and releases. A completed review round can retire its detached checkout while a PR is open, has findings handed off, or waits for a release. Author branches require an exact merged PR source HEAD and a finished task. Release branches also require their release task to be **done**, after publication and verification.

## Returning a checkout with its task

After stopping processes and checking that the directory has no remaining consumer, the owner or lead returns it in the normal `yesman_board` update. Save the conclusion and any publication or push evidence in the task note or a report it references. Use an explicit empty `worktrees: []` when the directories are still needed. The current extension asks for this disposition when closing a task with managed temporary checkouts. Older clients can still finish tasks. Their unstructured notes and rejected cleanup attempts grant no new authority.

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

The task and its returns are saved together. An unmerged PR can be returned for later collection. `sourceBranch` names the actual published head ref if it differs from the registered local branch. Cite the push or PR creation in the evidence. The service preserves both names and checks the source repository, published ref and full commit against the merged PR. It also checks the local and published refs for another open PR in the repository or its upstream. A release return additionally supplies `publication`, the published GitHub release URL. The service resolves its tag to the verified PR source or merge commit. The publisher verifies the delivery and records those results in the note.

The service discovers saved returns on startup after restart recovery and after relevant board or management changes. One background worker processes identified instances. Owner retirement wakes its waiting jobs. Outstanding local or remote checks retry with backoff from one minute to one hour while the app runs. Before removal it rechecks the same workspace hire, task result, owner and delegate generations and process reservations. Reopening or reassigning a task withdraws its board return. Open, paused or dropped tasks, another open assignment of an owner, a changed checkout root, and older checkouts without a recorded directory identity remain available. Verified legacy completions on eligible instances can be retried. A pre-existing release completion without publication proof stays on the manual path.

A saved Board certification belongs to one task close and one authenticated return. The first manual request captures that close, owner and checkout disposition before remote verification. The Board stores a generation for each task that changes when its status, owner, owner incarnation or pause changes. Reopening and dropping a task again therefore starts a distinct round even when both closes have an empty `DoneAt`. The service checks the captured decision again before saving completion, granting a handoff or removing the directory. A note edit can proceed during the lookup. An explicit keep requires the owner or lead to return the checkout on the Board before agent or background collection. A manual proposal matching an existing Board return uses its certification and round. Reopening the task, choosing to keep the checkout, or replacing the return withdraws that certification. Manual retries, delegated retries and the service read the current disposition again under the final local lock. An owner or lead can return the same source in a new round with a new result note. After verification the registry preserves the prior certification and its round in `priorReturns`. Crash recovery can record a previously completed removal after withdrawal by checking the absent directory, every Git registration and the exact recovery ref.

Closing the last open task of an owner or delegate wakes a checkout that was waiting on that local work. Pending remote checks keep their backoff. The single worker orders discovery and collection requests by when they became due. One slow remote check can delay other requests for up to that attempt's timeout. Configured workspace aliases and paths reported by Git share a pool lease using the physical parent directory and checkout name.

A detached review round that hands off findings before its board task finishes can still use the explicit tool. Both roles receive the return and recovery instructions in their run context.

## Cleanup from the Worktrees panel

Open the context menu on **Worktrees** in the Info panel and choose **Clean up worktrees…** to preview eligible local directories. Expand **Keep** to see why other directories will stay. Bulk cleanup requires a current saved completion, checks the merged pull request again for a branch, and verifies publication for a release checkout. A Board decision to keep a checkout excludes it from bulk cleanup.

Open the context menu on a directory or pull-request row and choose **Remove Worktree…** to review just that checkout. This also works for a clean, unused checkout of a completed or dropped task whose branch was never published. The dialog shows its full directory and current commit. A branch with an open pull request stays in place. If the Board explicitly kept an otherwise eligible checkout, the dialog explains that confirming this single removal overrides that decision. A changed task round, decision or checkout requires a fresh preview. Close programs using the directory before confirming. Escape or Cancel keeps it. The menu key or Shift+F10 opens either context menu from the keyboard.

Each confirmation checks the workspace hire, original checkout and directory identity, current commit, board and owners again. The Files pane keeps directories with unsaved edits. Tracked changes, untracked or ignored files, hidden index flags, unknown directory entries, locks, submodules, protected branches and unfinished or paused tasks keep a checkout in place. Historical registrations with matching private markers and both Git links can be verified even if they predate the recorded directory inode. A preview on a file system without directory birth times holds the verified checkout directory open through confirmation and compares its device and inode. A checkout with a recorded creation identity still requires that identity. A missing registration or ambiguous link stays available for inspection.

Git saves the exact source, recoverable private history and metadata as described in [Recovery and retained history](#recovery-and-retained-history). A checkout with saved conflict stages or an unverifiable index stays in place. The desktop records the removal independently of the task’s completion. Each directory in a batch has its own result. If Git removed a directory but its record could not be updated, the result reports both outcomes.

## Archive or restore one author worktree

From **Info → Worktrees**, hover over or tab into an author checkout's row and choose **Archive worktree**. Read the current path, destination, branch and full HEAD. Close any editor or preview using the tree, check that other tasks and processes have finished using it, and confirm. Open **Worktree archives → Restore worktree** in the same panel to preview and confirm the inverse. Both actions are available in the desktop window only.

Archive moves the complete directory to `archive/worktree-<checkout-id>` under the data folder. It keeps the index, tracked edits, untracked and ignored files, private Git metadata, branch, commits and disk usage. The checkout then appears only in the archive group. Its original name is reserved for restoration. Desktop removal and the background reclaimer honor the reservation, including while a move is pending. After a restore, automatic and bulk cleanup require a fresh Board return. A dismissed workspace takes the record of its archives with it, and their registered paths stay in the data folder's archive.

Admission requires the original author record, private nonce, directory inode and creation time, matching Git common directory and bidirectional registration. The associated task must be finished. Yes Man checks the recorded worker, the current task owner and any cleanup delegate for active work. A task can have changed hands and the branch or HEAD can have changed since checkout. The current values are verified and stored under `archive` in `.yesman/worktrees.json`. Historical branch, HEAD, ownership and PR completion retain their original meanings. The user confirms non-use by other tasks and external processes. The desktop issues a one-use confirmation for the displayed checkout and task state. A new preview supersedes it, and it expires after ten minutes. Archive and restore preview and confirmation read the current default branch from `origin`. Keep the remote reachable when moving the directory. Recovery after a completed rename and pre-move cancellation use the saved local identity.

This version supports ordinary absolute Git links on the same volume, with a local branch and an identifiable filesystem root. Primary/default branches, submodules, custom per-worktree configuration, unfinished Git operations, foreign Git locks, nested pool worktrees, path collisions, missing identities and ambiguous registrations are refused. Resolve an occupied restore path separately and request a new preview. An existing file, directory or link is never replaced by the worktree.

### Recovering an interrupted move

The app saves an `archiving` or `restoring` record before modifying Git or moving files. It locks the linked worktree through Git with the transaction's unique reason. It then uses the operating system's same-volume no-replace rename and atomically updates the one verified private `gitdir` backpointer. The absolute `.git` link in the worktree already points at the correct private directory. Git's lock protects the registration from pruning if the process exits between the rename and the backpointer update. Git's ordinary `worktree move` uses an overwriting rename and updates the registration after it, so those two steps require separate protection here.

After restarting, open **Worktree archives** and choose **Finish archive** or **Finish restore** for that checkout. Verify the displayed physical path, branch and HEAD and confirm. The app checks both paths and durably republishes the authenticated intent before continuing. It keeps the worker generations verified at admission, so removing a finished worker still permits recovery. It also reserves each relevant name, including a replacement worker, and checks for active processes and open tasks. Repeated clicks cannot move a replacement or reuse a confirmation from an earlier round trip. Finish or cancel pending moves before dismissing the workspace.

If a move stopped before the rename, for example because its destination directory was read-only, **Cancel pending move** asks you to confirm the current location. It releases the transaction’s own Git lock after verifying that the directory and its registration are both still at the source. The destination may contain an unrelated file or directory when you cancel. Cancellation verifies the original directory, its private Git registration and its transaction lock, then leaves both paths in place. Once the directory has moved, choose **Finish** to complete the registration. A final journal write or sync failure reports the verified physical location. Refresh **Worktrees** and follow the recovery action shown if a pending entry returns after restart.

If the identity, contents of a registration file, or location no longer matches, preserve the tree, its bare pool repository and the workspace metadata. Inspect `git -C <pool-repo> worktree list --porcelain`, the checkout's `.git` file and its private directory's `gitdir`, `yesman-instance` and `locked` files alongside the saved `archive` record. Clear an unrelated path conflict or correct storage access, then retry in the panel. Leave a mismatched or missing marker for manual investigation. Avoid Git prune, force, manual unlocking or recertifying an unrelated PR during recovery.

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

On resuming, check whether the old path exists. If absent, read the report/lifecycle record, reopen the task when necessary, and call `yesman_checkout` with the recorded repository and **exact full HEAD** as `ref`. Verify HEAD before continuing. A fresh checkout gets a fresh ID and no completion, so an old cleanup event cannot remove a new review round at the same path. Removed records and worker sessions remain available as evidence; they are also indexed in role context and `yesman_repos`.

The service works from the registered checkout and its saved completion proof. It never adopts existing directories or infers ownership from a name. Repeated cleanup after successful removal is harmless. An absent path with a remaining Git registration is retained for recovery instead of pruning unrelated registrations. If an explicit checkout request finds an earlier same-owner record but both its directory and all Git registrations are already gone, it retires only that history record and creates a fresh instance, including for older calls that omitted purpose. This never completes an unfinished task, adopts an existing unknown tree, or transfers another owner's resource. A replacement directory is never deleted. Legacy resources need separate provenance review; they are not automatically adopted by this policy.

Tests use isolated local Git repositories/workspaces and mocked GitHub merge responses. They exercise the real checkout/remove handlers, durable records, role contexts, exact reconstruction, squash source preservation through fetch/prune/GC, ownership handoff, active-owner reservations and retention cases without invoking external models.
