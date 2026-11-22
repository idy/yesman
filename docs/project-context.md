# Project context

Yes Man updates its project brief when Pi prepares a new run. A submission from the Topics composer can focus that Topic and its linked Board task. Scheduled submissions can focus the Board task for that run. The managed Pi runtime carries the input identity for this binding. Other runtimes use the brief without input-specific links when they lack that metadata. Messages consumed within an existing run share its brief.

The brief keeps the role and workspace rules, with up to 5,400 additional bytes for current project details. Use `yesman_board`, `yesman_topics`, `yesman_agents` and `yesman_repos` when you need the full source or its existing management actions. The Lead can optionally attach a compact `dispatch` to `yesman_worker_start` with `boardTask`, `goal`, `constraints`, `deliverables` and `sources`. Keep the complete kickoff in `task`. The dispatch persists with the Worker record. Pi continues to manage each agent's conversation and automatic compaction.

## Find and read project material

Both Lead and Worker can use these read-only tools.

1. Call `context_search` with a keyword or phrase, such as `{"source":"knowledge","query":"界面字体"}`. Optional `id` selects an exact item. For example, `{"source":"topics","id":"tp3"}` can locate a resolved or archived Topic. Keyword search includes open Topics by default.
2. Check the title, date, item status and excerpt. The result labels confirmed conclusions, interim summaries and open questions separately. If you need more of a field, call `context_read` with its `ref`. You can pass that ref to another agent in the same workspace.
3. Pass a search cursor with the same filters, or a read cursor with the same ref. If the matching search results or targeted item changed, search again. A moved or removed document is missing at its former reference.

| Source | What it covers | Date filter |
| --- | --- | --- |
| `knowledge` | Markdown notes and the index | Date in a filename or heading, when present |
| `working_logs` | The TOC, legacy single-file log and Markdown records | Date in a filename, heading or dated list entry, when present |
| `topics` | Titles, confirmed conclusions, open questions, summaries and project citations | UTC date updated |
| `board` | Current task title, status, waits, detail, note and owner | UTC date updated |
| `tasks` | Saved schedule plans and retained run records | UTC date updated for a plan, UTC occurrence date for a run |

`from` and `to` are inclusive `YYYY-MM-DD` dates. Exact ID matches rank first, then title matches, then body matches. Search returns up to five hits and 3,500 encoded bytes per page. Read returns the relevant section or field in up to 4,100 encoded bytes by default, or up to 8,000 with `large: true`. A byte limit can shorten a page containing Chinese or escaped text. Search is bounded and can report partial coverage. `no_match` means a complete search found nothing. `source_unavailable` means the requested source or original could not be verified. `denied` means the reference or caller was refused, `changed` calls for a fresh search, and `deleted` means the original is missing. `source: "history"` reports that Chat History integration is pending.

Treat retrieved text as source material. A reference identifies a version and a location in the current workspace. Each read checks the running agent's credential and workspace again. Project reads skip linked documents and do not follow citations to other locations.
