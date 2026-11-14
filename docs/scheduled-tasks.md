# Scheduled tasks

Tasks (定时任务) keep a yesman's scheduled tasks: requests the user saves for its lead, each due once, every day, every week or every few minutes, hours or days. They run while the app runs, in the tray too, and only then: yesman installs no system service, login item or timer, and asks for no permission. The code is [`internal/schedule`](../internal/schedule); the pane is [`web/schedules.js`](../web/schedules.js).

Saving a plan turned on is the user's authorization to send its request at its times, as if they typed it then: the lead handles it with the usual tools and rules, and must still ask before anything the user has not authorized. A plan holds natural-language instructions only; there is no scheduled shell command. Nothing creates plans by itself. The lead's `yesman_schedules` tool lists them and, only when the user asks for one, saves a draft that stays off until the user turns it on in Tasks. Workers have no access.

## Where they are kept

| Path | What |
| --- | --- |
| `.yesman/schedules/plans.json` | Configuration: `version`, `next`, and each plan's `id` (`s1`, `s2`, …, never reused), `title` (≤120), `instructions` (≤4000), `schedule`, `timezone`, `enabled`, `revision`, `activeSince`, `createdAt`/`updatedAt`/`archivedAt`, `createdBy`. Fields it does not know are kept. A file it cannot read, or of a later version, is left unchanged: Tasks shows the error and runs nothing of it. |
| `.yesman/schedules/runs/<id>.json` | One record per run, missed range or skipped occurrence: plan, revision, kind, occurrence key and time, state, board task, submission attempts, last seen board outcome, and the title and instructions it sent. No transcript, images or secrets. |

At most 50 plans are on or off at once, 200 with archived ones (the oldest archived one is forgotten first), and 30 records are kept per plan besides runs still under way or whose board task is open. Links or files in place of these folders are not written through.

## When a plan is due

- **Once** at a date and time, **every day** at a time, **every week** on chosen days at a time: wall-clock times in the plan's own IANA time zone (`Asia/Shanghai`, `UTC`; never "Local"). The app embeds the time zone database, so Windows and Linux resolve the same names. Changing the computer's time zone does not move a plan.
- **Every N minutes, hours or days** (15 minutes to 7 days) is elapsed time from when the schedule was saved or turned on.
- Daylight saving time: a time that does not exist (clocks go forward) runs later by the gap, 02:30 becoming 03:30; a time that occurs twice (clocks go back) runs only the first time. A day with no such time at all is skipped. Each local occurrence has a key, its local date and time, and runs at most once.
- Saving or turning on a plan never runs times already past. A once plan saved on with its time past is refused; save it off and use Run now.

The scheduler wakes at the next due time or every 30 seconds and compares the wall clock. An occurrence seen within 10 minutes of its time runs (half the interval for shorter intervals). Times the app was closed, asleep or stalled for, or skipped by a clock jump, become **one missed record** with their count and range; they are never replayed by themselves. If the newest is still on time it runs and the older ones are missed. A missed record's **Run it now** catches up once, whichever and however often it is clicked. Setting the clock back never runs a recorded occurrence again: the plan waits until the clock passes its last recorded time.

While a plan's last run is still open, its delivery under way or its board task todo, doing or blocked, the next occurrence is **skipped** with a record naming that task, rather than piled on the lead. Run now is the user's explicit request and is allowed then, after a confirmation, unless the last run is still being sent.

## A run

1. **Claim.** Under the workspace's lock the plan is checked on and unchanged, then its record is created exclusively (`O_EXCL`). Two ticks, two schedulers or a repeated click produce one record. Turning the plan off before this prevents the run; after it, the run goes on and is shown — turning a plan off or archiving it never aborts the lead or touches its queue.
2. **Board task.** One task per run, linked by `schedule: {plan, run}`, which only the scheduler sets. Adding it is idempotent by run, so recovery never adds a second.
3. **Submission ID**, recorded before dispatch.
4. **Submit** through the agent manager's receipt path as a **follow-up**: an answering lead finishes first; it is never steered or aborted for a scheduled run. The receipt's `origin: {kind: "schedule", plan, run}` is side metadata; pi's command and the text are unchanged and carry no private IDs. The lead reads the plan's title and ID, its schedule, the planned time, its board task and the instructions.
5. **Delivery state**, from pi's answer only: **sent** (accepted), **queued** (accepted into pi's follow-up queue), **not sent** (refused, or proven never to reach pi), or **unknown**. pi's acceptance (`ack`) stays recorded when the receipt later turns unconfirmed after a settle. A queued run whose lead process ended before reading it becomes unknown.

Each step is recorded before the next, inside the workspace's use lease, so a crash leaves a state the next start resumes or resolves:

| Left at | On the next start |
| --- | --- |
| claimed, board task maybe added | The board task is found by run, not added again; sent if still on time, else missed |
| submission ID recorded, no receipt | The ID is provably unregistered (newer than the receipt floor and retention): sent under it if still on time, else **not sent** with **Send again** |
| receipt exists (acknowledgment lost, quit mid-wait) | Resolved from the receipt; never sent again |
| receipt expired or evicted | **Unknown**, never sent again |

**Send again** is offered only when a run was definitely not delivered, and keeps its board task with a new submission ID. **Check again** rereads the receipt and sends nothing. **Run again as a new run** is the user's new intent: another run, board task and ID, after a warning that the lead may get it twice. Nothing is ever resent under a new ID by itself. This is at most one dispatch per submission ID and one run and board task per occurrence — not exactly-once execution across crashes, which no receipt can prove.

## What a run achieved

The result is the run's **board task**, which the lead and its workers keep with `yesman_board`: in progress, blocked, done or dropped, with its note. The run shows it, and keeps the last status and note if the task is later removed. Nothing is inferred from the lead's replies, a settled run or a worker finishing. The message asks the lead to mark the task with the outcome and to record the result in the task log (Working log) as usual; that is guidance, not something yesman enforces. The scheduler never writes the task log; its own facts are the run records in Tasks.

## Processes and lifecycle

One app per data directory runs the scheduled tasks: it holds an OS lock on `.runtime/schedules.lock`, which the system drops if the app ends. Another app shows them read-only and takes over when the lock is free; it carries on runs left under way, resolving them from their receipts. Without a lock no scheduler carries another's run. Dismissing a yesman retires its schedules before its workspace moves, and every write checks the workspace generation, so nothing reaches the archive or a new yesman with the same name. Quitting stops scheduling before the agents stop.
