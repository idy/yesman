# Embedded score snapshots

Source: [Artificial Analysis](https://artificialanalysis.ai). These are embedded,
maintained snapshots, not live API/provider measurements. Missing cells remain
missing; numeric zero remains a source-recorded zero. Source ranks are retained
metadata, not a ranking calculated by Yes Man.

## 2026-11-18 GPT-6.2 addition

`aa-scores-2026-11-18.csv` preserves all 50 records from
`aa-top50-scores-2026-11-14.csv` byte-for-byte, then appends the ten GPT-6.2 Astra
and GPT-6.2 Sol rows supplied by the project maintainer for this update. Each
family has low, medium, high, xhigh and max effort records. No effort is inferred.

The input was the user-maintained `aa-top50-scores-2026-11-18.csv`:

- SHA-256: `2d9860dd0c8745e2f6dcc4500d9decd09ccb1321e46799b1abd78fe8dbe557f9`.
- Input size: 4,502 bytes; 50 rows and the same 15-column header as the old file.
- Supplied for inclusion in Yes Man on 2026-11-18. The filename supplies the
  snapshot date; the neighboring source notes retain an older introductory
  collection date and legacy column descriptions. The precise upstream
  collection time has not been independently established.
- The source notes identify AA's model leaderboard and model pages. Relevant
  family pages: [GPT-6.2 Astra](https://artificialanalysis.ai/models/gpt-6-2-astra)
  and [GPT-6.2 Sol](https://artificialanalysis.ai/models/gpt-6-2-sol).

The ten added CSV lines are copied exactly, including numeric precision, blank
cells, and their supplied ranks. The other 40 supplied rows are outside this
update. A full replacement would remove 12 previous model/effort records and
change unrelated performance and missing/zero data. The resulting 60-row file is
therefore a maintained combination, **not a fresh top-50 leaderboard**: existing
records retain their 2026-11-14 values/ranks, and only GPT-6.2 records come from
the supplied 2026-11-18 input. Ranks can repeat across these dates. The app's
snapshot date identifies this combination's update date.

### Fields used by Yes Man

| CSV column | Meaning and unit |
| --- | --- |
| `intelligence_index` | AA Intelligence Index, raw score |
| `tok_per_s` | Output speed, tokens/second |
| `coding_mean_pct` | Supplied mean of available coding evaluation percentages |
| `cost_per_task_usd` | USD per Intelligence Index task |
| `time_min_per_task` | Minutes per Intelligence Index task |

The supplied GPT-6.2 coding means cover FrontierCode-AA 2.0 and Terminal-Bench
4.0; their SciCode cells are blank. Older rows can include SciCode as a third
contributor. These means have different evaluation coverage and are not an
official model Coding Index or the agent/harness-specific Coding Agent Index.
Yes Man retains the supplied means without recalculating them. The additional
token/seconds/sub-benchmark/Elo columns remain source metadata; they are not
substituted into the five fields above. No token price is used as USD/task and
no benchmark/request elapsed seconds are substituted for min/task.

The source file was provided and authorized by the maintainer for this update;
it is not a new direct AA scrape or authenticated API export obtained by Yes
Man. AA attribution and terms remain separate from the repository's MIT
application-code license; no AA endorsement or new AA-issued license is claimed.
The historical snapshot is retained unchanged.
