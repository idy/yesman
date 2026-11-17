# Agents' temporary files

Task output stays inside the yesman's workspace. Two different mechanisms do this, with different strength:

- **Default locations.** These are environment variables, not a boundary.
- **The managed write entry.** This is an enforced boundary, but only for the write and edit tools.

Neither is a sandbox.

## Default locations

Before starting each pi process, the Manager reserves directories for it (`internal/agent/workspace_temp.go`):

| Path | Variables |
|---|---|
| `.yesman/tmp/_agent/<agent>/<incarnation>/` | `TMPDIR`, `TMP`, `TEMP`, `GOTMPDIR`, `YESMAN_TMP` |
| `.yesman/evidence/_agent/<agent>/<incarnation>/` | `YESMAN_EVIDENCE` |
| `.yesman/tmp/_cache/go-build`, `go-mod`, `npm` | `GOCACHE`, `GOMODCACHE`, `npm_config_cache` |

- **Directory checks.** Each incarnation directory is created exclusively, with mode 0700, through `os.Root`. Every parent must be a plain directory. A link or a non-directory refuses the start, so programs never fall back to the system temporary directory.
- **Reuse and cleanup.** An incarnation is never reused. Only a reservation no process was started with is removed, for example when pi's executable is missing, and only if it is still the directory created. Once the operating system has started the process, its directories stay, even if pi then fails to initialize or Stop cancels the start: they may hold that start's diagnostics. Nothing else is deleted automatically.
- **Shared caches.** The caches are shared by the workspace's agents and kept across runs.
- **Precedence.** The variables are set last, replacing every case variant of the name, so they win over the app's environment. Each shell command applies them again after the user's Settings variables (`pi/yesman.ts` `shellEnvironment`). Names such as `TMP` are not reserved in Settings, so existing configurations still load.
- **What they reach.** Because they are set in pi's own environment, they also govern pi's own output files: the full output of a truncated command, MCP binary resources and codemode images, which pi writes to `os.tmpdir()`.
- **Missing or replaced directory.** The extension records the scratch directory's identity when it loads. If the directory is later missing or replaced, shell commands are refused rather than recreating it. The next start gets a new incarnation.
- **Left alone.** `HOME`, `USERPROFILE`, XDG configuration and every credential and configuration location are unchanged.

These are defaults only. A command can set the variables again or name any path. A tool that ignores them is not covered, for example `GOCACHEPROG`, an explicit `npm --cache`, or an SDK with its own temp setting.

## Managed write and edit

A worker's `write` and `edit` are pi's own tools; their file operations are done by the app through `POST /v1/files` (`internal/server/files.go`).

**What may be written.** Grants come only from trusted state:

- the calling process's own scratch and evidence directories, still the very directories reserved for it;
- worktrees whose lifecycle record names the caller's current generation as owner, with purpose author or release, not removed, whose directory is still the one yesman created for that checkout, and whose private Git instance is the recorded checkout.

A path, a directory name, `owner.json` or a task name grants nothing. Review checkouts, other directories, `.git`, yesman's metadata, other agents and other workspaces are refused.

The grant is chosen from the path first. A scratch or evidence write reads no checkout's metadata. A worktree write reads only the chosen checkout's: its `.git` file, the `gitdir` backpointer and the instance marker, each a small regular file opened through its parent directory's handle without following a link or reparse point and without waiting on a FIFO, so a broken or unrelated checkout cannot hold up or redirect a write. The pool reads the same files the same way when it lists and removes checkouts.

**Checkout directory identity.** When yesman makes a checkout, the pool opens the new directory right after Git creates it, under the same locks, checks through that handle that it carries the new instance, and measures the directory's identity: its inode number (the NTFS file index on Windows, which includes a sequence number) and its creation time. The identity is saved in the checkout's lifecycle record with the instance and the owner's generation (`worktrees.json`, `root`). It is never measured later: a record without one, because the checkout was made before identities were kept or its file system reports no creation time, gives no write/edit grant, and the record is not filled in. To use write and edit there, make a new checkout with `yesman_checkout` (purpose author or release, for your open task); shell commands are unaffected, and old records, their history and Git's registrations are left as they are. The device number is not kept, because macOS numbers devices when they are mounted; the directory must instead be on the workspace's own file system.

**When.** The write is admitted with the process credential's change gate (`BeginChange`) inside the workspace's `Use`, and held until the write is done. A write of a stopped or replaced process either lands before Stop returns, in that process's own directories, or not at all.

A worktree write also holds the pool's lock of that worktree directory (`Pool.WithWorktree`). The pool takes the same lock, exclusively, while it adds or removes a checkout there. Under it the write opens the worktree directory, requires the opened directory to have the recorded identity and to carry the recorded Git instance, and writes through that open directory. So a removal waits for a write in progress, a write after a removal or a new checkout at the same path is refused for the old record, and any other directory at the checkout's path, whatever files it holds, is not written for it.

Lock order: the workspace's checkout lock, the workspace's `Use`, the agent's change lease, the pool's repository lock, the worktree lock. The Manager's own lock is never held while a file operation or a pool lock is waited on.

**How.** Every name below the workspace is opened relative to its parent's open directory, without following links:

- **macOS and Linux:** `openat` with `O_NOFOLLOW`, one component at a time, holding each directory open. The kernel refuses a link at any component, so a link, or a parent swapped for one, inside or outside the grant, is never traversed.
- **Windows:** `NtCreateFile` relative to the parent's handle, with `OBJ_DONT_REPARSE` (the open fails at a reparse point) and `FILE_OPEN_REPARSE_POINT` (a reparse point would be opened itself, never its target). The handle that comes back must not carry `FILE_ATTRIBUTE_REPARSE_POINT` and must be the kind asked for, directory or file. Symbolic links, junctions and other reparse points are refused by the open itself.

The primitives are in `internal/safefs`, shared by the write entry and the pool.

Files are opened without truncation and without waiting on a FIFO. They must be regular files with one link. Reads are bounded at 32 MiB, and mode and line endings are kept.

**Edits.** An edit's write carries the hash of what it read. It changes only that existing file: it creates neither the file nor a missing directory, and is refused if the file changed or was removed since it was read, leaving the removal in place. Only a write without a read, pi's `write` tool, creates the file and its directories.

The lead keeps its own narrower rule for Knowledge and the Working log.

**Limits.** These checks are about which object this write opens. They do not stop another program of the same user from changing files, including a checkout's files or its registration, and the directory identity is file-system metadata: a program able to make a directory with the same inode number and creation time is not told apart. A checkout moved to another file system, or across volumes, loses its identity match and needs a new checkout.

## Not covered

These write with the agent's own operating-system permissions, anywhere it can:

- shell commands and the programs they start;
- other tools and MCP tools;
- extensions' own file system or child-process calls;
- the user's `!`/`!!` commands.

An operating-system sandbox for shell commands is a separate design; it is not enabled.
