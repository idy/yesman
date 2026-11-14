# Changelog

Each release's section is what installed copies show as its release notes.

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
