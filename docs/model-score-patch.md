# User score CSV patch

In **Settings › yesman › Models**, the score CSV controls let you import or replace one patch, export it, save a blank template, and disable or re-enable it. The patch supplies scores for models you configure in pi and add to Models; it never registers a provider, adds a catalog model or crew member, or changes the default model.

The patch is `model-score-patch.csv` in the selected yesman data directory (`YESMAN_HOME` when set, otherwise the folder chosen in Storage). It is independent of the installed app and the embedded AA snapshot. Each launch reads this file and resolves it against that build's current snapshot. Import immediately replaces the active patch after validation; a new Lead context reads the effective scores on its next context request. Existing prompts already sent to a model are not rewritten. Disabling keeps the file and remembers the choice in `settings.json`.

## Format

Export **Blank template…** for this header-only UTF-8 CSV:

```csv
provider,model_id,effort,intelligence_index,tok_per_s,coding_mean_pct,cost_per_task_usd,time_min_per_task
```

The headers are stable and required; their order may change. Extra or duplicate headers are rejected. The exact, case-sensitive tuple of `provider`, **full** `model_id`, and `effort` identifies a row. Slashes and colons in model IDs remain part of the ID. This matching is independent of AA's name normalization and route-name fallback.

`effort` must be `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, or `default`. `default` means the option's effort is unspecified (the empty configured effort). It never matches a named effort and does not mean `off`. Do not leave the effort cell blank. A score patch does not make an unavailable model or unsupported effort runnable.

| Metric column | Unit | Accepted values |
| --- | --- | --- |
| `intelligence_index` | Raw Intelligence Index, not percent or rank | 0–100 |
| `tok_per_s` | Output tokens per second | Finite number ≥0 |
| `coding_mean_pct` | Mean coding evaluations, percent | 0–100 |
| `cost_per_task_usd` | USD per Intelligence Index task | Finite number ≥0 |
| `time_min_per_task` | Minutes per Intelligence Index task | Finite number ≥0 |

Nonempty metrics override only their fields. An explicit `0` stays zero. Blank metrics inherit the **current** AA score, or the existing configured user estimate when no AA record matches, otherwise they stay unavailable. A partial CSV-only score may have unavailable intelligence; that is distinct from intelligence zero and sorts after available intelligence. A row with all five metrics blank has no effect. A header-only patch removes all overrides.

AA lookup retains its existing rules: exact effort first; a sole unlabelled score can be shared only if there are no named-effort records. A partial patch does not fill missing AA fields from a user estimate when an AA record exists. Settings and Lead context distinguish user CSV values, inherited AA values and inherited estimates, retain unspecified AA effort, and qualify inherited AA zero duration as a source record whose actual time is unverified. User CSV values are not described as AA measurements. The five figures keep their original units and four significant digits.

## Replacing and recovering

Import replaces the entire patch. Export the current patch before replacing it if you want a backup. CSV is limited to 1 MiB and 2,000 data rows, with keys at most 512 bytes each. Duplicate exact keys, invalid UTF-8, wrong headers, invalid effort and invalid numbers are rejected with row/column information. A failed import leaves the last valid file and in-memory state unchanged. The selected source file is read only; importing the active saved file itself is refused, so export and edit a separate copy first.

The app validates all rows before writing a private temporary file in the same data directory and renaming it over the saved patch. App updates replace the embedded snapshot only: overrides survive, inherited values refresh, new built-in rows become available, and CSV-only rows remain. Importing the general TOML configuration does not carry the patch or change its disabled state. Move or back up the selected data folder to carry the patch with it, or use Export current.

If the saved CSV is edited externally while the app is closed and is invalid on launch, the file stays untouched, Settings reports the error, and the app uses its normal AA/estimate fallback. There is no hidden backup cache. Recover by importing a valid exported copy. External file edits while the app runs are not watched; use Import/Replace for explicit updates.

## Validation

`internal/crew/patch_test.go` covers exact keys, bounds, blanks/zero and a changed embedded snapshot using the same persisted patch bytes. `crew_patch_test.go` covers the real Settings and Lead resolution path, atomic save failure, invalid-import rollback, export, disable/re-enable, and restart in temporary profiles. `internal/server/crew_test.go` covers effective provenance and units.

With Playwright installed, run `YESMAN_PLAYWRIGHT=/path/to/playwright/index.mjs node scripts/score-patch-browser.mjs`. It uses the actual Go score service and production page in Chromium and WebKit, with a synthetic catalog and private temporary profile. The native file-dialog transport is substituted; no live app, user profile, external AA/model service or credential is used. Set `YESMAN_SCORE_PATCH_OUT` for screenshots and `TMPDIR` for task-private temporary files.
