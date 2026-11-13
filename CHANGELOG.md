# Changelog

Each release's section is what installed copies show as its release notes.

## Unreleased

- Each yesman has a board: a task list its agents keep with the new `yesman_board` tool. Open it from the titlebar beside the conversation, where the files show. Open tasks keep their order, a blocked one says why, finished ones fold into Done, and a task's owner opens that worker's conversation.
- The lead delegates instead of working, so it stays free to talk: it has no shell and writes only knowledge notes, and hands changes, commands and investigations to workers. Workers keep their own board tasks up to date.
- The sidebar shows what each working agent is doing now, such as the command it runs or the file it reads, and how long its run has gone on. Idle workers show their task.
- The lead hears when a worker's run is stopped by you or its process fails, not only when it finishes.

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
