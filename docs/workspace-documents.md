# Workspace documents

The user-facing log is **Working Logs／工作日志**. Its stable compatibility paths remain `.yesman/task-log.md` and `.yesman/task-log/`; existing API, command and URL identifiers retain `task-log`. This display-name change does not rename files, rewrite historical wording or change Board task identities.

Each yesman keeps two kinds of shared documents. The board tracks current work; these files keep evidence and reusable guidance across restarts.

| Document | Contents |
| --- | --- |
| `.yesman/knowledge/`, cataloged in `INDEX.md` | Reusable conventions, preferences, architecture decisions and pitfalls. Update an existing topic rather than adding task handoffs. |
| `.yesman/task-log.md` | Short table of contents: date, task/version, title/link and at most one result sentence per record. |
| `.yesman/task-log/YYYY-MM-DD-tNN-topic.md` | Detailed historical evidence per task, PR or release: reasons, complete refs/links, validation, outcome, follow-up and recovery information. |
| `.yesman/board.json`, through `yesman_board` | Current plan, owners, status and blockers. |
| `.yesman/needs-you.json`, through `yesman_needs_you` | What only the user can do or decide, until they act; closed by the user (done, not needed) or resolved by the lead. The task it blocks stays on the board. |
| `.yesman/topics.json`, kept with `yesman_topics` and the Topics pane | The user's questions and discussions worth finding again: a summary of the question, the conclusions the user confirmed, the questions still open and their sources. |

Topics are about the user's questions, not the work: a topic cites a task or a Knowledge note, or names the Working log record it relates to, instead of repeating it, and is not added for routine reports or notifications. Only the lead writes topics through the API; the user edits them in the app. See the README's Topics section.

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

The lead's pi `write`/`edit` exception accepts `.yesman/task-log.md` and **flat** `.yesman/task-log/<name>.md` files, where names start with a letter/digit and contain only letters, digits, dots, dashes and underscores. Ordinary missing `.yesman`/`task-log` parents may be created. Existing targets must be regular, have one hard link, and have ordinary parents. The utility's `legacy-HASH-original.md`, numbered slices and `legacy-HASH-index-NNNN.md` catalogs are frozen evidence and excluded from ordinary lead writes; link any factual correction from a new dated record. Linked/dangling targets, linked parents, directory placeholders, nested paths, `..`, adjacent prefixes, other workspaces, `.bak`, recovery files and manifests are refused. The workspace itself may be an alias of the same workspace. Windows uses the platform's native path and case rules, including drive/UNC resolution. Knowledge's existing exception is unchanged.

These are restrictions on that task-log path exception; the existing Knowledge exception and other OS/tool access are unchanged. This is a pi tool hook checked before execution, **not an OS sandbox**: third-party tools/extensions and external filesystem writers retain their own permissions. Code work still goes to workers. The Go files reader confines paths using `os.Root`; history opens traverse ordinary parent directory handles and validate the actual regular-file descriptor, link count and path identities. Content is consumed from that same verified descriptor and rechecked after reading; it is never reopened after a separate preflight. The read-only history set covers `.md` documents below `.yesman/task-log/`, including existing nested/Unicode/space names from custom TOCs; new lead-created records still use the narrower flat naming policy above. The app's `WriteFile` refuses these history documents, while ordinary documents and `.bak` files retain editing. These checks do not make filesystem operations transactional against a hostile concurrent OS writer.

Open Working log from the info panel, the files scope menu or **More → Open Working log**. TOC links open one detail through the existing workspace file reader; Contents returns to the TOC. Anchors, browser Back/Forward and refresh retain the workspace/path/anchor in the URL, without embedding document content. History entries retain scroll offsets, and bounded per-page session positions restore a previously read TOC when Contents returns; explicit fragments take priority. All log pages are rendered read-only with no Source/Save. Beside a log page, the navigation lists the records by day (newest day first, the TOC's order within a day). A record's day is the leading `YYYY-MM-DD` of its TOC line, else of its file name, and must be a real calendar date; file times are never used. Records with neither, such as the utility's legacy originals, slices and catalogs, stay in an explicit No date group. The navigation reads only the TOC and the names of the flat files in `.yesman/task-log/`, never record bodies, and changes nothing. Non-log relative files and external URLs use the normal file/external handlers. More → Reveal remains associated with the workspace scope; the sidebar's Reveal Working log locates the compatibility TOC.

## Explicit legacy migration

Use [the offline migration guide](task-log-migration.md) for dry-run, verification, resumption and rollback. Legacy single-file logs remain readable until a deliberate migration. The utility preserves every original byte and order, full refs, links and evidence, with bounded 32 KiB slices and a complete original for Markdown constructs/old anchors that cross a slice. It does not summarize, rewrite or delete historical wording, or modify Knowledge. The complete original is a recovery/reference artifact, loaded only on demand; subsequent records follow the per-task rolling rule.
