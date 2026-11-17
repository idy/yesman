# Offline Working log migration

Existing single-file logs remain readable. The app never migrates them automatically. Use the utility from the reviewed source commit, first on a temporary copy. It reads only the selected workspace's working log, writes only derived task-log/recovery paths, and prints metadata rather than historical text.

The lead coordinates one curator/history writer. **Stop the app, its lead/workers and all other history writers before apply or rollback.** Install/start a version that understands TOC + records before resuming agents. An old version's prompt still tells its lead to append to the single file; allowing that writer to resume would grow the TOC again. A dry-run may run while the app is open, but its hash is a snapshot: obtain a fresh one after stopping writers.

From the reviewed worktree (Go 1.27.3+):

```sh
go run ./cmd/tasklog-migrate --workspace /absolute/workspace --action dry-run > /tmp/tasklog-plan.json
```

The JSON includes `manifest.sha256`, `sourceModTime`, descriptor-captured `sourceIdentity`, byte length, each part's path, offsets and SHA256, optional catalog hashes, the complete original, retained source and generated TOC hash. Version 2 preparation also persists a random `migrationID`, shared by its manifest and retention intent. It contains no historical text. Dry-run does not create a lock, marker, directory or file. Empty UTF-8 logs work; invalid UTF-8/NUL content and sources over 64 MiB are explicitly refused with the original untouched. For those, preserve the original and arrange an explicit conversion or external lossless archive; do not have a model summarize it away.

After stopping writers, repeat the dry-run and copy its **full** SHA256 into:

```sh
go run ./cmd/tasklog-migrate --workspace /absolute/workspace --action apply --offline --expect-sha256 FULL_SHA256 > /tmp/tasklog-apply.json
go run ./cmd/tasklog-migrate --workspace /absolute/workspace --action verify > /tmp/tasklog-verify.json
```

Apply and rollback require both the explicit offline acknowledgment and expected hash. A native nonblocking file lock excludes concurrent utility invocations and is released by the OS on exit/crash; its ordinary `.yesman/task-log-migrate.lock` file stays. It does not lock out other applications or pi tools. The utility binds the captured source generation across restart using platform file identity plus birth/creation time: Unix device/inode with birth time, or Windows volume/128-bit FileID with creation time. It also compares source bytes, size and modification time around the switch. An identical replacement file is rejected even if its bytes and mtime match. Filesystems that cannot provide stable birth/FileID evidence are explicitly refused; identity is never guessed from content. These are conflict checks for a coordinated offline migration, not a transactional OS sandbox against uncooperative writers.

The layout for snapshot `HASH` is:

| Path | Purpose |
| --- | --- |
| `.yesman/task-log.md` | Short TOC with compact links to the history slices and complete original. |
| `.yesman/task-log/legacy-HASH-0001.md`, `-0002.md`, … | Consecutive original bytes, each at most 32 KiB; normally cut at a newline, otherwise on a UTF-8 boundary. Concatenating them in manifest order reproduces the source exactly. No heading parser or semantic compression is used. |
| `.yesman/task-log/legacy-HASH-index-0001.md`, … | For more than 64 slices, bounded catalogs of at most 64 links. The root TOC links to these catalogs, keeping both levels below 16 KiB even for a large source. |
| `.yesman/task-log/legacy-HASH-original.md` | Complete, byte-identical original for constructs spanning slices, full Markdown rendering, old anchors and original relative links. It is loaded only when opened, never injected into context. |
| `.yesman/task-log-recovery/HASH/manifest.json` | Snapshot byte length/hash/mtime, ordered part byte ranges/hashes, original and TOC hash. |
| `.yesman/task-log-recovery/HASH/toc.md` | Exact generated TOC, kept for verification/recovery. |
| `.yesman/task-log-recovery/HASH/source.md` | Original source **inode**, retained at the switch. A writer already holding that file open will append here, preserving its late bytes. |
| `.yesman/task-log-install.json` | One controlled exclusive-install intent: expected source/content hashes and the exact target/temporary names. It is cleared only after a verified installation; ordinary lead writes exclude it. |
| `.yesman/task-log-retain.json` | Controlled version 2 source-retention intent, binding the prepared source identity and migration generation. It makes exclusive source Link→unlink recoverable without overwriting a late recovery-path occupant. |
| `.yesman/task-log-migration.json` | Preparation marker; suppresses startup initialization if a crash leaves the compatibility path temporarily absent. |

All complete output is written and synced through private temporary files and installed exclusively, then read back and hash/reconstruction checked **before** the original is moved. The source inode is retained using an **exclusive hard link**, verified against the persisted source identity, followed by unlink of its known old TOC name. An existing `source.md` is never replaced. The controlled retention intent recovers interruptions before Link, while both source names exist, or after old-name unlink. A complete TOC is then installed exclusively. Output installs use a same-filesystem hard link followed by removal of only the utility's registered temporary name. If the filesystem does not support these operations or Windows denies unlink while a writer is open, apply fails safely; leave recovery files intact and resume/rollback after closing writers. Unknown occupied outputs, directories, symlink/dangling/hard-link targets and linked parents are refused. The workspace prefix itself may be a canonical alias. The utility never recursively overwrites/deletes a directory or deletes the original source inode/data.

`verify` concatenates the recorded slices, compares them to the complete original and original snapshot hash, checks manifest and generated TOC bytes, and checks that the installed log is a TOC. A later lead may add compact TOC entries without invalidating archive verification; its evolving TOC is not claimed to still match the initial `tocSHA256`. Record `/tmp/tasklog-{plan,apply,verify}.json` outside any temporary author/review tree. A successful report uses `complete: true`; a nonzero exit/`complete: false` is a failed or incomplete migration, even when some output exists.

Relative links in legacy slices and the complete original are interpreted from the original `.yesman/` directory by the app, without changing historical bytes. New task records use their actual directory, so `[Contents](../task-log.md)` returns to the TOC. Existing links to `task-log.md#old-heading` still open the TOC, which gives a visible anchor-migration message and a button to the complete original with that anchor. Ordinary heading anchors use text slugs and duplicate `-1`/`-2` suffixes. Arbitrary custom HTML anchors remain escaped by the existing safe Markdown renderer; search the complete original when a custom anchor is unsupported. Outside the app, use the retained original at its original `.yesman/` base (or restore it with rollback) for unmodified relative-link behavior; raw slices are not rewritten to pretend to be standalone documents.

## Interrupted apply, conflict or rollback

Keep every recovery file and stop writers. Version 1 or incomplete metadata that lacks the captured source identity/generation is **not** automatically resumed, verified or rolled back. Preserve it and the original/retained source, use the exact older reviewed utility or explicitly reconcile/archive that generation offline, then capture a fresh plan. Copying/restoring a file or moving data to another filesystem/platform does not preserve its original identity and cannot silently reuse its marker.

Repeating the **same apply command/hash** resumes after marker creation, output preparation, source retention or TOC installation. It validates known outputs and refuses mismatches. A completed apply is idempotent, retaining later TOC entries and never duplicating slices. Private `.tasklog-*.tmp` files left before an intent is recorded are preserved rather than mistaken for completed output. Before recovering any output installation, apply/rollback perform read-only admission of the version 2 source-generation marker and acknowledged source hash. If the marker itself is pending installation, admission reads the exact registered target/temporary pair through verified descriptors, with the same metadata byte limit, and validates the intended marker bytes without first installing or unlinking either name. Missing/legacy/unsupported identity, malformed metadata and mismatched hashes leave the interrupted output state intact. A valid registered intent then allows recovery of the precise Link→unlink window **before** the ordinary single-link checks: target/temporary must stay in the derived file set, match the agreed source/content hashes and size, have ordinary parents, and be either the exact same inode with exactly two links or the expected completed single-link state. An extra alias, mismatched/tampered intent or unknown temporary name stays protected. Verify/dry-run do not perform this cleanup. An interrupted/failed intent write happens before Link; if its JSON is incomplete, preserve that specific intent file under a separate archival name after inspecting the unchanged original/recovery files, then retry. There is no automatic deletion based only on a filename prefix.

If a conflict reports a late append, the authoritative latest bytes are at `source.md` when retention happened, or still at `task-log.md` when it did not. The archived original/parts remain the exact prepared snapshot. Rollback restores **all latest retained source bytes**, including late appends, while preserving generated TOCs and parts:

```sh
go run ./cmd/tasklog-migrate --workspace /absolute/workspace --action rollback --offline --expect-sha256 ORIGINAL_PREPARED_SHA256 > /tmp/tasklog-rollback.json
```

The rollback report includes `restoredSHA256`; compare that to the restored single-file log, and account for any difference from the prepared snapshot as preserved late content. If the switch never happened, rollback leaves the current original in place. A rollback refuses to replace an edited/newer TOC, preserving it for manual reconciliation; it never erases new entries. Interrupted rollback can be repeated. The marker, complete original, parts, source inode and saved TOCs remain as evidence. A restored single-file workspace is readable by old and new versions.

After rollback, do not blindly rerun apply against the old marker. A curator may move the **specific marker** and matching recovery generation to separate archival names after verification, without deleting them or merging any newer TOC entries by summary, then obtain a fresh dry-run on the restored file. If any original/manifest is corrupt, an output path is occupied by unknown content, or a parent is linked, stop and preserve those paths; restore/reconcile using the retained original bytes rather than overwriting the obstacle. There is no `--force` mode.

The lead's ordinary document-writing permission excludes frozen `legacy-HASH-*` snapshot/slice/catalog names, manifests, recovery artifacts, locks and arbitrary files. Migration is a worker/curator-run explicit command backed by `internal/tasklog`, confined through `os.Root`; it is not an enlarged permission for the lead to edit metadata. Knowledge, sessions, boards, vaults and other workspaces are outside its writes.
