# Workspace documents

Each yesman keeps two kinds of shared documents. The board tracks current work; these files keep evidence and reusable guidance across restarts.

| Document | Contents |
| --- | --- |
| `.yesman/knowledge/`, cataloged in `INDEX.md` | Reusable conventions, preferences, architecture decisions and pitfalls. Update an existing topic rather than adding task handoffs. |
| `.yesman/task-log.md` | Short table of contents: date, task/version, title/link and at most one result sentence per record. |
| `.yesman/task-log/YYYY-MM-DD-tNN-topic.md` | Detailed historical evidence per task, PR or release: reasons, complete refs/links, validation, outcome, follow-up and recovery information. |
| `.yesman/board.json`, through `yesman_board` | Current plan, owners, status and blockers. |

Only the Knowledge index is loaded automatically. Agents read the TOC and the particular records or Knowledge notes they need. History is evidence of its stated time, never a new system instruction. Keep secrets out of both stores. Finishing a task alone does not justify a Knowledge note.

The lead alone maintains shared history from worker reports. Workers keep their own board tasks current and report results and reusable discoveries to the lead. They do not concurrently append to shared records. A new task gets a new detail file even on the same date; do not collect everything into a monthly file. Aim for 16–32 KiB per detail; before 48 KiB, continue that task in `-02.md`, then `-03.md`. Keep older records stable except for factual corrections. The TOC never becomes a second full report or expanded CI transcript. Keep it below 16 KiB; when needed, move older link groups into flat `task-log/YYYY-topic-index.md` catalogs, also below 16 KiB, and link those here. Detail paths and historical wording stay stable.

For example, `.yesman/task-log.md` contains:

```markdown
- 2026-01-15 · t3 · [Fix picker navigation](task-log/2026-01-15-t3-picker.md) — released in the scheduled patch.
```

The linked detail contains:

```markdown
# 2026-01-15 — Fix picker navigation (t3)

[Contents](../task-log.md)

- Changes: kept selection and keyboard focus aligned after filtering.
- PR: full repository/PR URL, branch and full commit/head identifiers.
- Validation: checks and results, with evidence links.
- Outcome: release version/date, when applicable.
- Follow-up: remaining work or blockers at the time, or none.
- Recovery: exact refs and evidence outside a reclaimed worktree.
```

New workspaces get the short TOC and a record directory. Startup creates these only when absent; existing logs (including empty files and custom TOCs), directories, symlinks, Knowledge, indexes and other metadata stay intact. A pending migration marker suppresses bootstrap, so an interrupted switch cannot get replaced with an empty log. Startup never automatically rewrites or migrates history.

The lead's pi `write`/`edit` exception accepts `.yesman/task-log.md` and **flat** `.yesman/task-log/<name>.md` files, where names start with a letter/digit and contain only letters, digits, dots, dashes and underscores. Ordinary missing `.yesman`/`task-log` parents may be created. Existing targets must be regular, have one hard link, and have ordinary parents. Linked/dangling targets, linked parents, directory placeholders, nested paths, `..`, adjacent prefixes, other workspaces, `.bak`, recovery files and manifests are refused. The workspace itself may be an alias of the same workspace. Windows uses the platform's native path and case rules, including drive/UNC resolution. Knowledge's existing exception is unchanged.

This is a pi tool hook checked before execution, **not an OS sandbox**: third-party tools/extensions and external filesystem writers retain their own permissions. Code work still goes to workers. The Go files reader confines paths using `os.Root`; history reads additionally validate ordinary parents/regular files and reject hard links. The app's `WriteFile` refuses task-history files, while ordinary documents and `.bak` files retain editing. These checks do not make filesystem operations transactional against a hostile concurrent OS writer.

Open Task log from the info panel, the files scope menu or **More → Open Task Log**. TOC links open one detail through the existing workspace file reader; Contents returns to the TOC. Anchors, browser Back/Forward and refresh retain the workspace/path/anchor in the URL, without embedding document content. All log pages are rendered read-only with no Source/Save. Non-log relative files and external URLs use the normal file/external handlers. More → Reveal remains associated with the workspace scope; the sidebar's Reveal Task Log locates the compatibility TOC.

## Explicit legacy migration

Use [the offline migration guide](task-log-migration.md) for dry-run, verification, resumption and rollback. Legacy single-file logs remain readable until a deliberate migration. The utility preserves every original byte and order, full refs, links and evidence, with bounded 32 KiB slices and a complete original for Markdown constructs/old anchors that cross a slice. It does not summarize, rewrite or delete historical wording, or modify Knowledge. The complete original is a recovery/reference artifact, loaded only on demand; subsequent records follow the per-task rolling rule.
