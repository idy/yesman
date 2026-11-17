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
- **Reuse and cleanup.** An incarnation is never reused. If a start fails before the process runs, only that start's own reservation is removed. Nothing else is deleted automatically.
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
- worktrees whose lifecycle record names the caller's current generation as owner, with purpose author or release, not removed, and whose private Git instance is the recorded checkout.

A path, a directory name, `owner.json` or a task name grants nothing. Review checkouts, other directories, `.git`, yesman's metadata, other agents and other workspaces are refused.

**When.** The write is admitted with the process credential's change gate (`BeginChange`) inside the workspace's `Use`, and held until the write is done. A write of a stopped or replaced process either lands before Stop returns, in that process's own directories, or not at all.

**How.** All I/O goes through `os.Root`, so a link, or a parent swapped for one, cannot take a write outside. Files are opened without truncation and without waiting on a FIFO. They must be regular files with one link. An edit's write carries the hash of what it read and is refused if the file changed since. Reads are bounded at 32 MiB, and mode and line endings are kept.

The lead keeps its own narrower rule for Knowledge and the Working log.

## Not covered

These write with the agent's own operating-system permissions, anywhere it can:

- shell commands and the programs they start;
- other tools and MCP tools;
- extensions' own file system or child-process calls;
- the user's `!`/`!!` commands.

An operating-system sandbox for shell commands is a separate design; it is not enabled.
