# Changelog

Each release's section is what installed copies show as its release notes.

## 0.1.64

- **Delete Yes Man…** reports the exact path of a Git blocker as soon as the review finds it, even in a large workspace. After addressing the blocker, use **Check again** to run a fresh review.
- **Clean up worktrees…** limits how long the preview waits for checks. It keeps worktrees whose eligibility could not be verified. You can retry the checks, cancel the preview, or review and confirm removal of just the eligible worktrees.

## 0.1.63

- **Clean up worktrees…** can permanently remove eligible idle temporary checkouts after you review the list and confirm, including dropped-task and older checkouts with uncommitted files. The preview identifies saved conflicts that would be discarded. Worktrees in use, linked to an open pull request, or marked to keep stay in place.
- The **Worktrees** list and refresh skip the workspace’s `.yesman/tmp` directory so large temporary build trees do not slow down discovery elsewhere in the workspace.
- **Force Delete…** can remove verified dangling Git links left by Bazel inside the workspace after you confirm the listed paths. It removes the links themselves and leaves external Git data intact.

## 0.1.62

- Lead and Worker get a short project brief at the start of a new Pi run. The Lead gets relevant Topic and task context for linked submissions. Workers can pick up a focused brief from their dispatch.
- Both roles can use read-only search across Knowledge, Working Logs, Topics, Board, and scheduled Tasks in the current workspace. Search returns short excerpts. Open the relevant section on demand or share its reference with another agent in the same workspace.
- Pi continues to manage each agent’s conversation, session resumption, and automatic compaction. Chat History lookup is planned separately.

## 0.1.61

- **Worktrees** now looks for local Git checkouts when you open workspace Info. It lists checkout paths, including nested checkouts, and tells you when the scan is incomplete.
- **Delete Yes Man…** now shows the paths blocking deletion. When a local checkout points to Git data outside the workspace and the deletion review finds no other blockers, **Force Delete…** lists what will be removed and asks for a separate confirmation. It can delete uncommitted and untracked files in the workspace. The external Git store and shared repository pool are preserved. Git stores inside the workspace and registered worktrees that still occupy it must be handled separately.

## 0.1.60

- Yes Man uses the Board task that created a temporary worktree to decide when it can be cleaned up, even if the task was reassigned. When the task is marked **Done**, eligible checkouts can be reclaimed automatically or through **Clean up worktrees…**.
- Cleanup keeps checkouts with open pull requests, active agents, or open writable Files editors. It also honors explicit keep choices. The cleanup preview shows why each checkout will stay and displays the Board status when known.
- Removing a checkout preserves its branch, tags, and recoverable Git history.

## 0.1.59

- Yes Man automatically retries cleanup of returned temporary worktrees as they become eligible, including after a restart.
- In the Info panel, right-click **Worktrees** and choose **Clean up worktrees…** to preview a batch, or right-click a row and choose **Remove Worktree…** to review one checkout.
- Cleanup keeps branches, tags, and recoverable Git history. It leaves worktrees in place when they are in use, have local changes or saved conflicts, or cannot be verified.
- **Delete Yes Man…** replaces Archive in the sidebar. The confirmation shows the workspace name. If you proceed, Yes Man stops its agents and permanently deletes its conversations, tasks, Knowledge, Working Logs, and workspace files. Handle its worktrees separately. Shared repositories, branches, and tags are preserved.
- In **Settings › Storage › Stored workspaces**, you can review and delete an old workspace archive or finish an interrupted deletion one entry at a time.

## 0.1.58

- Configure worker model and effort pools in **Settings › yesman**. Set dispatch instructions for Operations & Research, Backend Development, Frontend & Design, and Default.
- Yes Man uses the task label to select a worker from the matching pool. If that pool has no available member, it tries Default, then uses the current workspace lead’s model and effort.
- Change an idle workspace lead’s model and effort independently. Reopening a worker restores the selection saved for its session.

## 0.1.57

- Respond directly in a **Needs You** card. Write a reply, choose one of the offered options with an optional note, or confirm that you completed a requested action.
- Saved replies to handoffs remain available to the lead after a restart. Retrying the same reply avoids duplicate notifications.

## 0.1.56

- Keep Markdown table columns readable in Knowledge, Working Logs, and Files previews. Wide tables scroll horizontally in place.

## 0.1.55

- Yes Man can play a sound when the lead confirms that a project's requested work is complete, even with the app window closed while Yes Man is running.
- Turn on completion sounds in **Settings › General › Sounds**. Choose Soft bell, Warm pluck, Double tone, or Digital sparkle. Use **Listen** to preview any sound even when automatic alerts are off. The setting applies to all workspaces and is off by default.
- On Linux, playback uses the default ALSA output and requires `libasound.so.2`.

## 0.1.54

- Browse Topics in a flat list with Open, Resolved and Archived filters. The discussion and composer appear in the selected topic’s detail pane. Opening a topic from Chat keeps any active topic editor and its draft accessible.
- Browse Knowledge in collections that follow its folders. Notes outside collections are still available from the navigation.
- The Working Logs Contents entry now aligns with the dated groups.

## 0.1.53

- Fixed the alignment of text and the attachment and send buttons in the Topics reply box to match Chat. In narrow windows, the reply box now adjusts its height when the discussion scrollbar reduces the space for text.

## 0.1.52

- With the built-in Pi runtime, compatible messages sent while an agent is working join a single pending bubble, with text and images in the order you sent them. Pi takes the combined message at its usual checkpoint. Messages sent after that go separately.
- Use **Cancel and edit** to bring back the full pending message and its original images. **Stop** keeps a message Pi is still holding available to recall. The separate early-processing button has been removed.
- After an interruption or restart, **Recover draft** brings retained text and images back to the composer so you can review them and explicitly send them again.

## 0.1.51

- Add a user score CSV patch in Settings › yesman › Models, with import/replace, export, a blank template and reversible disable. Keep the patch in the selected data directory across app upgrades.
- Apply overrides by exact provider, full model ID and reasoning effort. Blank fields inherit the current built-in Artificial Analysis scores or existing estimate fallback; explicit zero stays zero. The patch supplies scores without adding models or crew members.
- Show the same effective scores in Models and Lead context, with each field identified as user CSV, inherited AA or inherited estimate. Keep unavailable values distinct from zero.
- Validate the complete CSV before saving it atomically. Invalid or duplicate rows leave the last valid patch unchanged, and disabling retains the file for later use.

## 0.1.50

- Add embedded Artificial Analysis scores for GPT-6.2 Astra and GPT-6.2 Sol at low, medium, high, xhigh and max reasoning effort. The crew settings and the lead's model context use the same scores.

## 0.1.49

- Add Restart and continue in Settings › About, the command palette and the tray. Restart to update uses the same recovery path.
- Continue eligible interrupted agents from their existing sessions and Board goals after an explicit restart, while keeping stopped, sleeping and human-waiting agents paused and checking workspace and worker identities before recovery.
- Reconcile workers' reports across the restart: hand on a report known not sent, preserve reports already accepted, and show uncertain delivery without sending it again. Original user messages and images are never automatically resent.
- Show the last restart's outcomes in About, including unfinished or failed recovery that needs an explicit message. Preserve those holds on later ordinary opens.
- Keep ordinary Quit separate from explicit restart recovery. Updating from an earlier version without this feature has no restart checkpoint to restore; existing bounded task-continuation checks still apply. Recovery does not guarantee exactly-once effects outside the app.

## 0.1.48

- Browse Files, Knowledge, Working log, Topics and Needs You with content on the left and navigation on the right. Narrow panes switch between navigation and the selected detail with a Back control.
- Navigate Knowledge notes and their headings in a tree. Topics are grouped by Open, Resolved and Archived, with branches for each topic's summary, conclusions, questions, sources and conversation.
- Browse Working log records by day, newest day first, keeping the TOC's order within each day. Dates come from the TOC or file names; undated records have their own group, and records open individually as needed.
- Answer a Needs You question or mark a handoff done or not needed directly in its detail pane. Open in the conversation takes you to where the request was raised.
- Keep an active Needs You answer and its text selection visible when narrowing the window. Show unavailable-workspace errors in narrow panes and provide a keyboard-accessible way back to the conversation.

## 0.1.47

- Default agents' temporary files, command-output files and Go/npm caches to the current workspace's `.yesman/tmp`, with retained verification evidence under `.yesman/evidence`.
- Route workers' built-in Write and Edit tools through Yes Man, limited to their own scratch and evidence folders and eligible managed author or release worktrees. Check the current process, workspace and checkout identity, and serialize managed file checks and updates.
- Keep existing temporary files and caches, home directories, credentials and configuration in place. Shell commands, other tools and extensions retain their operating-system permissions; this update does not enable an operating-system sandbox.

## 0.1.46

- Keep your original messages and images in the conversation without a second Earlier submission records list, its counters or permanent success labels.
- Show useful pending and error states with their available actions, and let pending items leave as pi takes them.
- Bring a hidden message back when you explicitly retry its unchanged draft, including from Topics. Keep the available complete original and Copy accessible after a page reload, and show a new failed attempt again.

## 0.1.45

- Keep an unfinished Board goal distinct from a worker finishing its run. Check tasks individually after reports, failures, Board changes, startup and the lead settling, and notify the lead when no running worker carries the work, even while other workers are active.
- Record why a blocked task waits: other tasks, a Needs You request, an external condition or an explicit user pause. Notify the lead when a recorded wait is ready; leave older blocked tasks with no recorded wait for the lead to assess.
- Preserve explicit lead and worker Stops across app restarts, report pause-save failures, and fence delayed reports and writes against newer user actions or replacement workers and workspaces. Resuming still requires an explicit message or authorized action.
- Keep reminder deduplication and finite retry and no-progress budgets across restarts. Notices ask the lead to continue authorized work; they do not complete tasks, start workers themselves, grant approvals or replay earlier user input or uncertain external effects.

## 0.1.44

- Show the macOS app copy and signing type checked by Settings › Grant, with clearer guidance when an ad-hoc build reports no Accessibility, Screen Recording or Input Monitoring access. Keep the reported state unchanged: replacing a build can invalidate an older grant, and the check cannot determine why access is missing.
- Add a separate System Audio Recording Only row, independent of Microphone and Screen Recording. On macOS 14.2 or later it shows Manual · cannot detect and opens the relevant System Settings pane; earlier versions show Not applicable. The row performs no recording and requests no access; macOS asks when a recording task actually uses that permission.
- Wrap long app paths within Grant so the full path and permission controls remain accessible, and keep English and Simplified Chinese category names and Settings action labels consistent.
- Full Disk Access still requires manual verification, and Automation remains specific to each target app. Opening System Settings does not establish authorization. This update does not reset or repair macOS permission records, change certificate signing, or establish that desktop automation access has recovered.

## 0.1.43

- Combine consecutive messages sent from the composer while its latest message is still waiting for pi, updating the same conversation bubble in submission order.
- Preserve the original text and image order, highlight appended content briefly, and let Cancel and edit restore the combined pending message.
- Keep later messages separate once pi starts preparing or takes the pending input, when the queue lane changes or combining would exceed message limits, and for Topics, scheduled runs and worker reports.
- Include managed pi 1.5.2-yesman.5 for pending-input append support. External runtimes without that support continue sending each input separately.

## 0.1.42

- Add Tasks for requests scheduled once, daily, weekly or at an interval in a chosen time zone. Plans run while Yes Man is open and the computer is awake; missed times appear together for a manual catch-up instead of replaying automatically.
- Show Tasks as a fifth Info entry with the enabled count, next time and attention state. Create, edit, turn off and archive plans, inspect run history, and follow links between a run, its Board task and the lead conversation.
- Send each run to the lead as an ordinary follow-up, preserving a reply in progress. Skip automatic overlaps while the previous run is being delivered or its Board task remains open; explicit Run now and catch-up actions ask for confirmation when work overlaps.
- Keep run and submission identities through supported retry, restart and workspace lifecycle paths. Delivery states follow pi's receipt or native queue evidence; uncertain delivery is shown as unknown and is never automatically resent as a new request. These records do not guarantee exactly-once execution.
- Show the Board task's recorded status and note as the outcome. The lead is asked to record results in Working log; the scheduler keeps its own run records and does not write that log. Lead-created plans stay off until the user turns them on.

## 0.1.41

- Add Topics to keep questions and discussions, with summaries, confirmed conclusions, open questions and linked sources. Search, filter, edit, resolve, archive and restore topics in their own pane.
- Send text and images from a topic into the yesman's existing lead conversation, preserving the original images and topic association through the supported queue, retry and restart paths. Association follows recorded submission identity; pasted topic labels and messages with missing provenance remain unassociated.
- Bring Knowledge, Working Logs, Needs You and Topics together as four Info entries with their actual counts and errors. Keep the conversation usable beside Topics in narrow windows, with drafts and keyboard focus preserved.
- Protect topic edits from conflicting revisions and delayed requests from a retired lead or workspace. Conclusions stay explicitly recorded; Topics does not invent answers or call a model in the background.

## 0.1.40

- Add a compact Working log contents page and separate records for each task, pull request or release. Open records on demand in a read-only view, then return to Contents or your previous reading position.
- Keep lasting guidance in Knowledge and detailed task and release history in Working log. Existing log paths and links remain compatible, and legacy single-file logs stay readable.
- Provide an explicit offline migration utility that preserves the complete original history and links, with verification and recovery support. Existing logs are never migrated automatically.

## 0.1.39

- Display the desktop application and its main process as Yes Man on macOS, Windows and Linux, including the window, native application metadata and new installer shortcuts.
- Keep the existing installation locations, bundle identifier, update feeds and signing key, workspace identities and user data paths. The `yesman` command and older executable paths remain compatible; Windows command-line calls retain their arguments, streams and exit status.
- Preserve automatic updates from the previous application name, including restart through the legacy entry points, subsequent updates and failure recovery. Existing user-created shortcuts and pins keep their paths; the new Windows installer refreshes its standard shortcuts.

## 0.1.38

- Add optional Settings › GitHub App configuration for agent-created pull requests, with a separate encrypted key vault and explicit repository and workspace selection. App identity starts disabled when no App configuration exists.
- Configure your own App, test the current connection and scope, then explicitly enable App identity. Saving a changed key or scope disables it until a fresh connection test and explicit enablement.
- Route supported PR creation in selected workspaces through the configured App identity. Git pushes and other GitHub operations keep their existing credentials; unavailable keys, services or installations in App mode stop creation without silently switching to a personal author. Keep App credentials out of agents and managed runtime dependency installation.
- The actual idy/yesman installation's Bot author, avatar and PR events have not yet been verified; this release does not mean that account is connected. Users can configure and explicitly enable their own App, while this feature's compatibility with the real GitHub service remains unverified.

## 0.1.37

- Open Needs You beside the conversation as a Board-style pane for the yesman shown and its current hire. Use the Info card or command palette to view its requests; the global sidebar entry and all-yesmen page are removed. Opening a request goes to its agent and question without answering it, while preserving unsent drafts and images. Request-memory notices, unreadable-file errors and recently handled requests stay available in the pane.
- Keep room for the message box beside Board or Needs You: the Info panel steps aside in narrow windows and returns when the pane closes. Preserve focus when opening, closing and refreshing the pane.
- Show an X on the queued message's Cancel and edit button, keeping its labels and existing cancellation and full text-and-image restore behavior.
- Use the application's context menus throughout the window, retaining existing row menus and offering selection, link and text-editing actions for their targets. On macOS, Control-click opens only the menu; editor Find fields keep their own editing actions. Menu Paste can attach PNG images in the message box within the existing limits, and a clipboard with nothing usable pastes nothing. Passwords and secret values remain protected.

## 0.1.36

- Fold consecutive runs that end only with a model error into one closed row that shows the count and the error text. Expand it to see each distinct error with its provider, model and count, copy the error text, and open each run's time and original work. A person's message, a reply, a partial reply or a stop ends the fold, and text that only mentions an error never counts as one. History and submission records are unchanged.
- Mark an agent whose last finished run ended with a model error with a red exclamation mark, a label and a tooltip in the sidebar and header; a yesman's entry follows its lead's own state. Working, waiting, Needs You and asking keep their own marks. Stopping the run, or a later run that ends any other way, clears the mark while the conversation keeps its error rows. The mark is not kept across an app restart. A crashed agent process shows the same red mark.
- Show an error recorded without a message as "The model returned an error without a message." and fold such errors together.

## 0.1.35

- Add a global Needs You inbox for explicit requests that need human action across yesmen and agents. Ordinary worker questions stay with their lead; blocked Board tasks and queued messages do not add inbox items.
- Bring Knowledge, Working Logs and Needs You together in the current yesman's Info card. Knowledge and Working Logs open in the app's file viewer, and Needs You opens that yesman's requests; the sidebar inbox covers every yesman.
- Open the matching agent and request while preserving an unsent draft. Viewing a request leaves it pending until it is answered, completed or dismissed through its available action.
- Show when long-term request memory approaches capacity. Start a new memory only after explicit confirmation, preserving current requests. A forgotten request can return if its tool call runs again, and each later reset can allow it to return again. Keep keyboard focus and confirmation dialogs tied to the correct view and request.
- Prevent named pipes in workspace metadata from blocking the app. Report oversized historical metadata for the affected workspace while keeping healthy workspaces available.
- Give earlier submission records a neutral heading with their count, and show their recorded outcomes in the expanded details. Keep current pending, failed and recoverable messages explicit, with their existing actions and original text and images.

## 0.1.34

- Show human submissions with their actual status and available actions, using original text and images while retained or the saved preview and image count otherwise. Fold older unmatched submission records into one collapsed section; uncertain delivery stays unconfirmed.
- Use Send now on a selected pending follow-up to move it to Steering while preserving the other queued inputs. It takes effect at the next eligible input checkpoint after the current response and tool batch.
- Cancel and edit eligible pending inputs to restore retained original text and images without overwriting a draft. Recover a recalled input after a missed reply while its original remains available, use the configurable composer recall shortcut, or copy complete message text. A whole app restart cannot recreate lost originals.
- Fix accepted queued inputs silently stalling during slow preparation. They run once when preparation completes; Stop or cancellation before intake keeps the original pending input recoverable.
- Prepare an isolated, verified runtime for supported queue actions. First preparation needs Node.js, npm and network access; a verified cache then needs only Node.js. Settings shows preparation and retry status, and unsupported external runtimes expose only the actions they support.
- Align repository refresh buttons in the final column, keeping worktree information to their left and readable in narrow windows.
- Prevent delayed requests from stopped, exited or archived agents from changing a replacement Board or its completion ownership. Exited processes lose authorization immediately; stopping an agent process waits for its already accepted Board writes to finish.

## 0.1.33

- Add Settings › Model usage to compare actual usage by provider, full model ID and recorded effort: worker participation, current done board-task attribution, responses, and input, output, cache and total tokens. Filter by yesman, agent role and period.
- Show measured output tokens per second across timed completed responses, weighted by their response durations and including first-token latency. Keep these measurements separate from Artificial Analysis benchmarks; missing effort or timing stays unknown.
- Explain coverage and attribution: only current workspaces and retained logs are included, and tasks follow the owner's latest response that ended by completion. A done board task does not establish independent verification or a model's causal contribution; these totals are not a billing statement or a reconstruction of all past usage.

## 0.1.32

- Add application shortcuts with Command on macOS and Ctrl on Windows and Linux. Open the searchable command palette with Cmd/Ctrl+K or Cmd/Ctrl+Shift+P to find actions, yesmen and workers, see their keys and availability, and run the same actions as their buttons and menus.
- Customize bindings in Settings › Shortcuts: search actions, record keys, resolve conflicts, remove bindings or restore defaults. Changes persist across restarts and travel with configuration imports and exports; titlebar buttons and the existing files menu show the current keys.
- Keep dialogs, menus, text editing, the file editor and input-method composition in control of their keys. Shortcuts act on the conversation shown and retain existing confirmations. These shortcuts work inside yesman's focused window; they are not global OS hotkeys and need no Accessibility or Input Monitoring permission.

## 0.1.31

- Add Settings › Grant to view this copy of yesman's OS permission status, refresh it without prompting, and choose individual Request or Open Settings actions.
- On macOS, cover Bluetooth, notifications, Full Disk Access, microphone, camera, Accessibility, Screen Recording, Input Monitoring and Automation. Full Disk Access and Automation use manual settings guidance; opening settings does not mean access was granted.
- On Windows, provide fixed settings entries for Bluetooth, notifications, microphone and camera. On Linux, explain desktop, device, session and portal policy limits. Independently launched terminals, runtimes and helpers may need their own authorization.

## 0.1.30

- Keep your messages and images, ordinary replies and questions visible in full, while folding thinking, tool activity and recorded background collaboration into expandable work lines in their original order.
- Render thinking with Markdown and show delivery states from actual submission records beneath linked messages. Work-line status describes only the steps it contains; it does not confirm individual message consumption or that every worker has finished.
- Keep older or ambiguous messages visible when their source cannot be established, including their images.

## 0.1.29

- Accept display names such as GizMoth, names with spaces and Chinese text when creating a yesman from the welcome page or Hire dialog, preserving their spelling and capitalization.
- Assign each new yesman a stable internal workspace name automatically, avoiding conflicts without overwriting existing workspaces. Display names do not have to be unique.
- Keep existing workspace identities, paths, conversations, boards and worktree data unchanged.

## 0.1.28

- Let review agents reclaim their own clean, reconstructible temporary worktrees after saving the review outcome and exact commit, with cleanup delegated by the lead when needed.
- Reclaim merged development and release worktrees only after checking the actual pull request source commit, completed tasks and release work, and no active use, including squash merges.
- Keep active, primary and main worktrees, dirty worktrees, those with untracked or ignored files, and unknown resources. Preserve branches, commits, tags, checkpoints and recovery refs.
- Give each managed worktree its own instance identity so an old cleanup request cannot remove a replacement created at the same path.

## 0.1.27

- Show message submission and Steering or Follow-up receipt states, with an unknown state when pi's acknowledgment cannot be confirmed.
- Reuse a stable submission ID for retries, keeping the complete text and image draft available after an interrupted or failed send.
- Restore saved receipt states after refresh. Receipts describe submission or queue acknowledgments; pi does not confirm consumption of individual queued messages.

## 0.1.26

- Keep reusable development rules, preferences and architecture guidance in Knowledge, with dated task and PR history in a separate `.yesman/task-log.md` maintained by the lead from worker reports.
- Include the Knowledge index in agent context automatically, and read task-log entries only when needed.
- Open the task log as a rendered, read-only page from the info panel and file menus, within the workspace's All files view.
- Initialize missing task logs for new and existing workspaces without replacing existing documents. Preserve occupied paths on Windows too, report filesystem errors, and restrict the lead's task-log writes to the workspace's regular, unlinked log file.

## 0.1.25

- Unify app-owned navigation, action and status icons with locally bundled Phosphor icon fonts, available offline.
- Use a solid Stop icon, with consistent sizes for Send, Stop, folder and compact status icons.
- Keep one caret on the Model and Effort menus while preserving local icon fallbacks for native selectors.

## 0.1.24

- Show each configured model's Intelligence Index, output speed, coding score, cost per task and time per task in Settings › yesman › Models, with their original units.
- Keep Artificial Analysis sources, source effort and your own estimates clear. Distinguish unavailable figures from zero values, and mark zero task durations whose actual time is unconfirmed by the source.
- Keep removed models and changed estimates from returning after a delayed catalog refresh.

## 0.1.23

- Align the Model and Effort labels and dropdowns in Add a model. Keep long options within the form and stack the fields in narrow windows.

## 0.1.22

- Move the attachment button to the left of the message field. Keep one action on the right: Send while idle, or Stop during a run. Enter still sends a message during a run, and Shift+Enter adds a new line.
- Keep the message field ready for the next message after clicking Send in WebKit, while respecting focus moved to another control or conversation.
- Reduce shadow banding behind modal dialogs with a more compact shadow.
- Remove the stray period below the saved configuration path in the Export completion message.

## 0.1.21

- Right-click a yesman and choose Rename to change its display name. The name is saved for future launches and updates throughout the app.
- Renaming keeps the same workspace folder and agent identities, so existing conversations, active workers, boards and worktrees stay connected.

## 0.1.20

- Show active workers above completed, stopped and failed workers in the sidebar, keeping the existing order within each group. Reactivated workers return to the active group.
- Keep keyboard focus on the same sidebar entry during updates and after closing its menu, including yesmen also shown under Needs you.
- Keep Secrets descriptions readable at medium widths and names and values together in narrow windows, with actions moving to their own row when needed.

## 0.1.19

- Keep Secrets list value fields at a fixed width, and constrain long values and editors within the list in narrow windows.

## 0.1.18

- Settings › yesman lists the crew: the models agents run as, each one of pi's models at one reasoning effort. Add and remove them there, and choose what new yesmen run as.
- Hiring a yesman asks only for its name: its lead runs as the model chosen for new yesmen.
- The lead chooses a member of the crew for each worker, weighing how smart, fast and costly each is for the task, from Artificial Analysis's scores. Without a choice, a worker runs as its lead's model.
- yesman finds each model's scores by its name. For a model Artificial Analysis has no record of, you give your own estimate when you add it.
- You can still run any agent as any of pi's models from the composer.
## 0.1.17

- This release retains the image submission support shipped in 0.1.16. There are no additional application changes.

## 0.1.16

- Settings › Storage shows how much space yesman's data takes. Usage, between Data folder and Locations, measures the yesman folder in the background: its total size and file count, a bar of the repositories, workspaces, archive, runtime files and the rest, each with its share, and the space left on its disk. Measure again refreshes it.
- Attach PNG, JPEG, GIF or WebP images to messages with the image picker, by pasting, or by dragging them into the composer. Send images with text or on their own, preview them and remove any before sending. Each message supports up to four images, 5 MiB each and 10 MiB in total.
- Text and image drafts stay with each agent while switching conversations, and a failed send keeps the draft ready to retry. Sent images also appear in the conversation history.
- Sending a message keeps the composer ready for the next one, while respecting focus moved to another control or conversation.

## 0.1.15

- Worktrees whose pull request is merged fold into one row at the end of the info panel's Worktrees list, so the work in progress stays on top. The row opens to show them, and a merged worktree the files pane shows stays in the list.
- Quitting yesman stops every agent for good: a worker's report to its lead can no longer start the lead again while the app quits.

## 0.1.14

- Settings › Secrets keeps API keys, tokens and other secrets, and Settings › Variables keeps settings such as an account or a region. Every command of every agent gets each as an environment variable; agents read what each is for with the new `yesman_env` tool.
- Edit them as a form, or as YAML in the editor. Each has a value and a description that tells agents what it is for.
- Secrets are encrypted with sops and age, with a random password kept in the system's keychain (Keychain, Credential Manager or Secret Service), which unlocks them when yesman starts. Without one, you choose the password and type it after each launch. A secret's value never reaches an agent's context: output shows it as `[secret NAME]`. The sops command can edit the same file with its age key.
- The configuration file (Settings › Import and export) now carries the variables and the secrets, still encrypted, with their password: importing it on another computer sets them up and unlocks them. Keep the file private.

## 0.1.13

- An info panel sits between the conversation and the files. Knowledge opens the agents' notes, rendered and read-only. Worktrees lists each worktree with the lines it changed, uncommitted edits too, or, when its branch is in a pull request, that pull request with its state and checks. Storage folds into one row with the disk the workspace takes, and opens to its largest parts.
- A worktree opens in the files pane, as its files or its changes. Changes shows the changed files as a tree beside each file's diff, and stays quick with thousands of files: the tree arrives a folder at a time, and a diff is read when you choose it and drawn as you scroll.
- The tree can show folders alone: choose a folder to list what changed in it, to see what the agents changed without reading every diff.
- The files pane's bar is one row: the part shown, which switches to all files, the knowledge or another worktree, then the path, each folder a menu of its contents, holding only what changed in Changes.

## 0.1.12

- Each yesman has a board: a task list its agents keep with the new `yesman_board` tool. Open it from the titlebar beside the conversation, where the files show. Open tasks keep their order, a blocked one says why, finished ones fold into Done, and a task's owner opens that worker's conversation.
- The lead delegates instead of working, so it stays free to talk: it has no shell and writes only knowledge notes, and hands changes, commands and investigations to workers. Workers keep their own board tasks up to date.
- Each worker has a compact, single-line sidebar row: status, a number such as a1, its subject in words, and its run time. Completed workers show a grey check; running workers show a pulsing blue dot. The board uses the same worker numbers.
- A worker that finished and reported closes and shows greyed in its place. Right-click a yesman to hide or show its done workers. Messaging a done worker starts it again with its session.
- The lead hears when a worker's run is stopped by you or its process fails, not only when it finishes, and restarts failed workers itself.
- Workers no longer turn to you. A question a worker's tools ask goes to its lead, which answers it, and a worker shows as asking its yesman instead of needing you. Only a lead asks you things or notifies you.
- The lead sees the work through: it decides what it can, finishes everything that does not wait on you, and then brings the open decisions together. If it ends a run with tasks open and nothing running, yesman reminds it once.
- The finished notification waits until the whole yesman has settled.

## 0.1.11

- Settings › pi manages pi's packages: the extensions, skills, prompt templates and themes every agent loads. Info shows the pi yesman runs, a summary of its packages and pi's own settings file. Installed lists each package with its version, source and what it provides, says when npm has a newer version, and updates or removes it. Browse searches the packages of pi's gallery, most popular first, and shows a package's README, links and preview before you install it. Install from source takes any npm package, git repository or folder pi takes.
- yesman installs, updates and removes packages with pi's own commands, one at a time in the background, for you as a whole: they load into every yesman agent and into pi wherever you run it. A change that fails stays listed with pi's or npm's reason until you try it again or dismiss it.
- pi loads packages when it starts, so the page says which agents still run with the packages as they were, and puts the idle ones to sleep. They load the change and resume their sessions when you next talk to them.
- The pi row moves from Settings › General to the pi page.

## 0.1.10

- yesman speaks Simplified Chinese as well as English. Choose the language in Settings › General, or Match the system to follow the languages of your system settings. The window, its menus, the tray and notifications switch at once.
- Storage has a page of its own in Settings: choose the yesman folder there, and reveal where the repositories, workspaces, archive and runtime files are.

## 0.1.9

- Export yesman's configuration from Settings › Import and export: the appearance, the fetch schedule and every GitHub repository in the pool, as a TOML file. Workspaces, knowledge and conversations are not included.
- Import a configuration file to see what it would change, then Merge or Replace. Merge clones the repositories the pool lacks and keeps everything else. Replace also resets the settings the file leaves out and removes the repositories it does not list.
- Replace never removes a repository that work depends on: one with a worktree checked out of it, in a workspace or the archive, or a branch with commits no remote has. It says which repositories it kept and why. Clones of repositories the file leaves out stop, unless an agent is waiting for one.
- The Settings pages sit more compactly under Settings in the sidebar, with smaller icons on a rail aligned with the Settings icon.

## 0.1.8

- A compact message field without the shortcut hint underneath. Text and the smaller, unboxed Send and Stop icons share a row, with the last line and icons aligned when the message grows.
- A wider, taller model menu shows each model's context limit and supports arrow keys and type-ahead.
- Model and effort choices sit on the right below the input box, alongside a context meter. Open it to see the estimated context used, model limit and free space, plus separate session token totals, cache usage and message/tool counts from pi.
- The context viewer refreshes as the conversation changes, supports the keyboard and both appearances, and keeps unavailable context usage distinct from an empty window.

## 0.1.7

- Choose where yesman keeps its data from General → Storage. Browse to or enter a folder, then restart to use it, including folders that already contain your data.
- Copy existing data yourself before switching folders. Launching with `YESMAN_HOME` still overrides the saved location.
- Storage has a compact list of fixed locations for repositories, the workspace parent, archive, runtime files and configuration, with Reveal buttons and full paths on hover.
- Worker records are cleaned up reliably after a rejected start on Windows, including while the sidebar refreshes.

## 0.1.6

- Add several repositories at once: in Settings › Repositories, name a GitHub user or organization and tick the repositories you want. Text after a slash filters them, and private repositories your GitHub CLI login sees are listed too.
- Adding returns at once. Repositories clone in the background, three at a time, and show in the list as waiting or cloning, the way a fetch does. A clone that fails stays listed with its error until you try it again or dismiss it.
- Repositories an agent adds show as cloning while it waits for them.
- Fetch all repositories of the pool at once, or have yesman fetch them by itself every minute, 5, 15 or 60 minutes, set in Settings › Repositories. A repository whose last fetch failed says so in its row until a fetch succeeds.

## 0.1.5

- Choose an agent's model and reasoning effort in a separate row below the input box. Models come from pi's configured catalog, with grouping only when there is more than one provider; effort lists only the levels the selected model supports, including Extra high and Max when available.
- Change selections between runs without losing the conversation or your draft. Each agent keeps its own selection in its pi session once it has messages, and restores it when it resumes.

## 0.1.4

- Click a folder in the path above a file to list that folder, and go into its folders or open its files from there. The file's own name lists the folder it is in. The arrow keys move through the list.
- With the files pane open, its tabs sit in the window's titlebar, so the pane runs the full height of the window.

## 0.1.3

- A new sky-blue app icon: a yak turning back to answer you.
- A matching monochrome yak in the titlebar, menu bar and system tray, tinted for the light or dark appearance.
- A simpler titlebar without the sidebar-collapse button. The sidebar stays visible, and the brand clears the window controls only while they are shown.

## 0.1.2

- The workspace panel is now a files pane: the yesman's workspace as a tree, and its files in tabs. It follows changes as agents make them.
- Edit text and code in the pane, with syntax highlighting, search, and undo. Cmd/Ctrl+S saves, and Auto Save saves as you type. A file that changed on disk since you opened it is never saved over without asking.
- Markdown shows rendered, with its source a click away; images show at their size.
- Large files stay quick: the editor draws only the lines in view, files over 5 MB open read-only at their start, and big folders list their first 2,000 entries.
- Go to a file by name with Cmd/Ctrl+P, or from the filter above the tree.
- Drag the pane's edge, or the tree's, to resize them.
- Links to workspace files in a conversation open in the pane.
- The pane's menu checks out repositories, opens the knowledge notes and dismisses the yesman.

## 0.1.1

The first version installed copies of 0.1.0 update to by themselves, to check that updating works. Nothing else changes.

## 0.1.0

The first release.

- Hire a yesman for a project: a workspace of its own with a lead agent you talk to, workers it starts, and notes they share.
- Check worktrees out of a shared pool of GitHub repositories.
- Settings for the appearance, the repositories and the versions yesman runs with.
- yesman checks for new versions, installs them in the background, and offers to restart into them from the titlebar.
