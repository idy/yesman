# Workspace documents

Each yesman keeps two kinds of shared documents. The board tracks current work; these files survive restarts alongside it.

| Document | Keep here | Examples |
| --- | --- | --- |
| Knowledge: `.yesman/knowledge/`, cataloged in `INDEX.md` | Reusable development conventions, user preferences, architecture decisions and known pitfalls | Use the project's Phosphor icon helper and local font assets; preserve stable internal IDs when display names change; follow the project's release/version policy. |
| Task log: `.yesman/task-log.md` | Dated historical facts about tasks and PRs | What changed and why, task/PR links, commit/head hashes, CI and validation results, release timelines, outcomes and follow-up work or temporary blockers at the time. |
| Board: `.yesman/board.json`, managed with `yesman_board` | Current plan and status | Who owns a task, what is blocked now and what happens next. |

A Knowledge note should help with future work even after its originating task is forgotten. “Use the Phosphor helper and local font assets” belongs there. “PR #42 at head abc123 passed three-platform CI and awaits release” belongs in the task log or board. Finishing a task alone does not justify adding Knowledge. Keep its index a concise catalog of durable notes, without task handoffs or progress summaries. Update an existing topic instead of duplicating it.

The lead maintains Knowledge from reusable discoveries and appends task-log entries from worker reports. Workers keep their own board tasks current and report changes, PR/commit links, validation, results, follow-up work and reusable discoveries to the lead. They do not concurrently rewrite the shared log. The lead may edit these documents with `edit` and `write`; code changes, commands, tests and investigations still go to workers.

The task log records historical evidence, not current instructions. Read relevant entries on demand and check the board for current status. Only the Knowledge index is included automatically in agent context; the full task log and Knowledge note bodies are not. Keep secrets out of both documents. A task-log entry can link to an applicable Knowledge note without repeating its icon or development rules.

Append entries using a date/task heading and a short list. Omit fields that do not apply. For example:

```markdown
## 2026-01-15 — Fix keyboard navigation (t3)

- Task: restore arrow-key navigation in the file picker.
- Changes: kept selection and focus aligned after filtering.
- PR: owner/repo#42 (https://github.com/owner/repo/pull/42), fix/picker, head abc123.
- Validation: unit checks and Chromium/WebKit keyboard checks passed.
- Outcome: merged; released in the project's next scheduled patch on 2026-01-16.
- Follow-up: none at completion.
```

New workspaces start with a Knowledge index describing this policy and an empty task log with an entry template. Startup adds a task log to existing workspaces only if its path is absent. An existing file, directory or symbolic link at that path is left untouched. Existing Knowledge, its index, board, worker metadata and sessions are preserved. The app does not interpret, rewrite, delete or automatically migrate old notes; the lead can coordinate any deliberate cleanup separately.

Open **Knowledge** or **Task log** in the info panel, choose either from the files pane's scope menu, or use **More → Open Knowledge Notes / Open Task Log**. Both use the existing files pane and Markdown reader; Task log opens as a read-only document in the workspace view. Agents read the same files directly when relevant. The sidebar's context menu also offers **Reveal Knowledge** and **Reveal Task Log** to locate these files on disk. No separate log database or agent tool is needed.
